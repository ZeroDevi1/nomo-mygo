package textdoc

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"nomo/desktop/internal/applog"
)

// journalVersion 是恢复日志格式版本；与 Tauri 版本写出的日志互通。
const journalVersion = 1

// baselineIdentity 是源文件身份：长度与纳秒 mtime 之外，再用文件 ID 识别原子替换、
// 用变更戳识别同一文件的原地改写。字段名与旧日志的 camelCase 保持一致。
type baselineIdentity struct {
	Path          string  `json:"path"`
	PhysicalLen   uint64  `json:"physicalLen"`
	ModifiedNanos uint64  `json:"modifiedNanos"`
	FileID        *string `json:"fileId,omitzero"`
	ChangeStamp   *string `json:"changeStamp,omitzero"`
}

func (b baselineIdentity) equal(other baselineIdentity) bool {
	return b.Path == other.Path && b.PhysicalLen == other.PhysicalLen && b.ModifiedNanos == other.ModifiedNanos &&
		optionalEqual(b.FileID, other.FileID) && optionalEqual(b.ChangeStamp, other.ChangeStamp)
}

func optionalEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// readIdentity 通过句柄读取路径身份，Windows 才能获得稳定的文件索引与 ChangeTime。
func readIdentity(path string) (baselineIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return baselineIdentity{}, asError(err)
	}
	defer file.Close()
	return readFileIdentity(path, file)
}

// readFileIdentity 从已打开句柄读取身份，使探测字节与 baseline 绑定到同一个文件对象。
func readFileIdentity(path string, file *os.File) (baselineIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return baselineIdentity{}, asError(err)
	}
	nanos := info.ModTime().UnixNano()
	if nanos < 0 {
		return baselineIdentity{}, newError("file-identity-failed", "文件修改时间早于 Unix epoch")
	}
	fileID, err := platformFileID(file, info)
	if err != nil {
		return baselineIdentity{}, err
	}
	changeStamp, err := platformChangeStamp(file, info)
	if err != nil {
		return baselineIdentity{}, err
	}
	return baselineIdentity{
		Path:          path,
		PhysicalLen:   uint64(info.Size()),
		ModifiedNanos: uint64(nanos),
		FileID:        fileID,
		ChangeStamp:   changeStamp,
	}, nil
}

// journalRecord 是一次已提交 revision 的补丁：普通编辑或整文替换文件。
type journalRecord struct {
	BaseRevision    uint64                  `json:"baseRevision"`
	Revision        uint64                  `json:"revision"`
	Edits           []Edit                  `json:"edits"`
	ReplacementFile *journalReplacementFile `json:"replacementFile,omitzero"`
}

type journalReplacementFile struct {
	Path       string `json:"path"`
	LineBreaks uint64 `json:"lineBreaks"`
}

// recoverySnapshot 指向 persisted revision 的不可变正文副本，恢复时从它重放补丁。
type recoverySnapshot struct {
	Path     string `json:"path"`
	Revision uint64 `json:"revision"`
	BomLen   uint64 `json:"bomLen"`
}

type recoveryConflict struct {
	journalPath string
	snapshot    *recoverySnapshot
	records     []journalRecord
}

// journalPayload 是日志的一行负载，按 type 区分 header 与 edit。字段顺序决定校验和输入，
// 必须与 serde 内部标签枚举的输出一致：type 在前，其后按声明顺序。
type journalPayload struct {
	Type              string            `json:"type"`
	Version           *uint32           `json:"version,omitzero"`
	Baseline          *baselineIdentity `json:"baseline,omitzero"`
	PersistedRevision *uint64           `json:"persisted_revision,omitzero"`
	RecoverySnapshot  *recoverySnapshot `json:"recovery_snapshot,omitzero"`
	Record            *journalRecord    `json:"record,omitzero"`
}

type journalEnvelope struct {
	Checksum string         `json:"checksum"`
	Payload  journalPayload `json:"payload"`
}

func headerPayload(baseline baselineIdentity, persistedRevision uint64, snapshot *recoverySnapshot) journalPayload {
	version := uint32(journalVersion)
	return journalPayload{Type: "header", Version: &version, Baseline: &baseline, PersistedRevision: &persistedRevision, RecoverySnapshot: snapshot}
}

func editPayload(record journalRecord) journalPayload {
	return journalPayload{Type: "edit", Record: &record}
}

// editJournal 是会话的崩溃恢复日志：头部记录 baseline 身份，随后逐行追加带校验和的补丁。
// 由会话的 journal 锁保护，非并发安全。
type editJournal struct {
	path              string
	baseline          baselineIdentity
	persistedRevision uint64
	records           []journalRecord
	flushedRecords    int
	// flushedFileLen 是最后一次 fsync 后的确定文件长度，重试写入前先截断回这里。
	flushedFileLen   int64
	recoverySnapshot *recoverySnapshot
	headerDirty      bool
}

func recoveryConflictForMissing(path string) (*recoveryConflict, error) {
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return nil, nil
	}
	contents, err := readJournal(path)
	if err != nil {
		return nil, err
	}
	return &recoveryConflict{journalPath: path, snapshot: contents.snapshot, records: contents.records}, nil
}

func emptyJournal(path string, baseline baselineIdentity) *editJournal {
	return &editJournal{path: path, baseline: baseline}
}

// openJournal 读取已有日志：基线一致时恢复未持久化补丁，不一致时返回冲突（数据保持原位，
// 只有用户可读恢复文档物化成功后才清理内部资产）。
func openJournal(path string, baseline baselineIdentity) (*editJournal, uint64, []journalRecord, *recoveryConflict, error) {
	journal := &editJournal{path: path, baseline: baseline}
	var recovered []journalRecord
	var conflict *recoveryConflict
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		contents, err := readJournal(path)
		if err != nil {
			return nil, 0, nil, nil, err
		}
		if contents.baseline.equal(baseline) {
			journal.persistedRevision = contents.persistedRevision
			for _, record := range contents.records {
				if record.Revision > contents.persistedRevision {
					recovered = append(recovered, record)
				}
			}
			journal.records = contents.records
			journal.flushedRecords = len(contents.records)
			journal.flushedFileLen = contents.validLen
			journal.recoverySnapshot = contents.snapshot
		} else {
			conflict = &recoveryConflict{journalPath: path, snapshot: contents.snapshot, records: contents.records}
		}
	}
	return journal, journal.persistedRevision, recovered, conflict, nil
}

func (j *editJournal) record(record journalRecord) { j.records = append(j.records, record) }

func (j *editJournal) hasRecords() bool { return len(j.records) > 0 }

func (j *editJournal) needsRecoverySnapshot() bool {
	if j.recoverySnapshot == nil {
		return true
	}
	info, err := os.Stat(j.recoverySnapshot.Path)
	return err != nil || !info.Mode().IsRegular()
}

func (j *editJournal) recoverySnapshotPath() string { return withExtension(j.path, "snapshot") }

func (j *editJournal) installRecoverySnapshot(snapshot recoverySnapshot) {
	j.recoverySnapshot = &snapshot
	j.headerDirty = true
}

func (j *editJournal) referencedAssets() map[string]struct{} {
	assets := map[string]struct{}{}
	for _, record := range j.records {
		if record.ReplacementFile != nil {
			assets[record.ReplacementFile.Path] = struct{}{}
		}
	}
	return assets
}

// flush 把未落盘记录追加并 fsync；头部变化时整文件重写后原子替换。
func (j *editJournal) flush() error {
	if len(j.records) == j.flushedRecords && !j.headerDirty {
		if info, err := os.Stat(j.path); err == nil && info.Mode().IsRegular() {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return asError(err)
	}
	if j.headerDirty && j.flushedFileLen > 0 {
		written, err := writeJournalFile(withExtension(j.path, "journal.tmp"), j.path,
			headerPayload(j.baseline, j.persistedRevision, j.recoverySnapshot), j.records)
		if err != nil {
			return err
		}
		j.flushedFileLen = written
		j.flushedRecords = len(j.records)
		j.headerDirty = false
		return nil
	}
	file, err := os.OpenFile(j.path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return asError(err)
	}
	defer file.Close()
	// 上次写失败可能留下半条 JSON；重试必须先回到最后一次 sync 的确定边界。
	if err := file.Truncate(j.flushedFileLen); err != nil {
		return asError(err)
	}
	var buffer bytes.Buffer
	if j.flushedFileLen == 0 {
		if err := writePayload(&buffer, headerPayload(j.baseline, j.persistedRevision, j.recoverySnapshot)); err != nil {
			return err
		}
	}
	for _, record := range j.records[j.flushedRecords:] {
		if err := writePayload(&buffer, editPayload(record)); err != nil {
			return err
		}
	}
	if _, err := file.WriteAt(buffer.Bytes(), j.flushedFileLen); err != nil {
		return asError(err)
	}
	if err := file.Sync(); err != nil {
		return asError(err)
	}
	if err := syncParentDir(j.path); err != nil {
		return asError(err)
	}
	j.flushedFileLen += int64(buffer.Len())
	j.flushedRecords = len(j.records)
	j.headerDirty = false
	return nil
}

// prune 在保存成功后剪掉已落盘的记录：仍有更高 revision 时保留它们与恢复快照，
// 否则删除日志与过期资产。protectedAssets 是撤销历史仍引用的替换文件。
func (j *editJournal) prune(persistedRevision uint64, baseline baselineIdentity, nextPath string, protectedAssets map[string]struct{}, bomLen uint64) error {
	previousPath := j.path
	previousSnapshot := j.recoverySnapshot
	hasUnpersisted := false
	for _, record := range j.records {
		if record.Revision > persistedRevision {
			hasUnpersisted = true
			break
		}
	}
	retentionFloor := persistedRevision
	if previousSnapshot != nil {
		retentionFloor = min(previousSnapshot.Revision, persistedRevision)
	}
	var retained []journalRecord
	retainedAssets := map[string]struct{}{}
	for _, record := range j.records {
		if hasUnpersisted && record.Revision > retentionFloor {
			retained = append(retained, record)
			if record.ReplacementFile != nil {
				retainedAssets[record.ReplacementFile.Path] = struct{}{}
			}
		}
	}
	var obsolete []string
	for _, record := range j.records {
		if hasUnpersisted && record.Revision > retentionFloor || record.ReplacementFile == nil {
			continue
		}
		path := record.ReplacementFile.Path
		_, protected := protectedAssets[path]
		_, kept := retainedAssets[path]
		if !protected && !kept {
			obsolete = append(obsolete, path)
		}
	}
	nextSnapshotPath := withExtension(nextPath, "snapshot")
	if len(retained) == 0 {
		removeObsoleteAsset(previousPath)
		if previousPath != nextPath {
			removeObsoleteAsset(nextPath)
		}
		if previousSnapshot != nil {
			removeObsoleteAsset(previousSnapshot.Path)
		}
		if previousSnapshot == nil || previousSnapshot.Path != nextSnapshotPath {
			removeObsoleteAsset(nextSnapshotPath)
		}
		for _, path := range obsolete {
			removeObsoleteAsset(path)
		}
		j.path = nextPath
		j.records = nil
		j.baseline = baseline
		j.persistedRevision = persistedRevision
		j.flushedRecords = 0
		j.flushedFileLen = 0
		j.recoverySnapshot = nil
		j.headerDirty = false
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(nextPath), 0o755); err != nil {
		return asError(err)
	}
	var snapshot recoverySnapshot
	if previousSnapshot != nil && fileExists(previousSnapshot.Path) {
		if previousSnapshot.Path != nextSnapshotPath {
			if err := copyFileDurable(previousSnapshot.Path, nextSnapshotPath); err != nil {
				return err
			}
		}
		snapshot = recoverySnapshot{Path: nextSnapshotPath, Revision: previousSnapshot.Revision, BomLen: previousSnapshot.BomLen}
	} else {
		// 保存期间出现更高 revision 且尚未建快照时，以刚落盘的 revision 为恢复起点。
		if err := copyFileDurable(baseline.Path, nextSnapshotPath); err != nil {
			return err
		}
		snapshot = recoverySnapshot{Path: nextSnapshotPath, Revision: persistedRevision, BomLen: bomLen}
	}
	written, err := writeJournalFile(withExtension(nextPath, "journal.tmp"), nextPath,
		headerPayload(baseline, persistedRevision, &snapshot), retained)
	if err != nil {
		return err
	}
	if previousPath != nextPath {
		removeObsoleteAsset(previousPath)
	}
	if previousSnapshot != nil && previousSnapshot.Path != nextSnapshotPath {
		removeObsoleteAsset(previousSnapshot.Path)
	}
	for _, path := range obsolete {
		removeObsoleteAsset(path)
	}
	j.path = nextPath
	j.records = retained
	j.baseline = baseline
	j.persistedRevision = persistedRevision
	j.flushedRecords = len(retained)
	j.flushedFileLen = written
	j.recoverySnapshot = &snapshot
	j.headerDirty = false
	return nil
}

// remove 删除日志及其恢复资产；先删日志本身，确保“放弃修改”不会因清理失败而再次恢复。
func (j *editJournal) remove() error {
	if err := removeIfExists(j.path); err != nil {
		return asError(err)
	}
	removeObsoleteAsset(withExtension(j.path, "journal.tmp"))
	if j.recoverySnapshot != nil {
		removeObsoleteAsset(j.recoverySnapshot.Path)
		j.recoverySnapshot = nil
	}
	for _, record := range j.records {
		if record.ReplacementFile != nil {
			removeObsoleteAsset(record.ReplacementFile.Path)
		}
	}
	j.records = nil
	j.flushedRecords = 0
	j.flushedFileLen = 0
	j.headerDirty = false
	return nil
}

func cleanupRecoveryConflict(conflict *recoveryConflict) {
	paths := []string{conflict.journalPath}
	if conflict.snapshot != nil {
		paths = append(paths, conflict.snapshot.Path)
	}
	for _, record := range conflict.records {
		if record.ReplacementFile != nil {
			paths = append(paths, record.ReplacementFile.Path)
		}
	}
	for _, path := range paths {
		if err := removeIfExists(path); err != nil {
			applog.Warn("SegmentedDocument", "清理已物化的冲突恢复资产失败："+err.Error())
		}
	}
}

// writeJournalFile 把头部与记录完整写入临时文件并 fsync，再原子替换 target，返回写入字节数。
func writeJournalFile(tempPath, target string, header journalPayload, records []journalRecord) (int64, error) {
	var buffer bytes.Buffer
	if err := writePayload(&buffer, header); err != nil {
		return 0, err
	}
	for _, record := range records {
		if err := writePayload(&buffer, editPayload(record)); err != nil {
			return 0, err
		}
	}
	file, err := os.Create(tempPath)
	if err != nil {
		return 0, asError(err)
	}
	if _, err := file.Write(buffer.Bytes()); err != nil {
		file.Close()
		return 0, asError(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return 0, asError(err)
	}
	file.Close()
	if err := replaceFile(tempPath, target); err != nil {
		return 0, asError(err)
	}
	if err := syncParentDir(target); err != nil {
		return 0, asError(err)
	}
	return int64(buffer.Len()), nil
}

func copyFileDurable(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return asError(err)
	}
	temp := withExtension(target, "snapshot.tmp")
	if err := removeIfExists(temp); err != nil {
		return asError(err)
	}
	input, err := os.Open(source)
	if err != nil {
		return asError(err)
	}
	defer input.Close()
	output, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return asError(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return asError(err)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return asError(err)
	}
	output.Close()
	if err := replaceFile(temp, target); err != nil {
		return asError(err)
	}
	return asError(syncParentDir(target))
}

func removeObsoleteAsset(path string) {
	if err := removeIfExists(path); err != nil {
		applog.Warn("SegmentedDocument", fmt.Sprintf("清理过期恢复资产失败（%s）：%v", path, err))
	}
}

type journalContents struct {
	baseline          baselineIdentity
	persistedRevision uint64
	records           []journalRecord
	validLen          int64
	snapshot          *recoverySnapshot
}

// readJournal 逐行读取并校验日志；末尾不完整的一行（崩溃中断）被忽略，已校验的前缀仍可恢复。
func readJournal(path string) (journalContents, error) {
	file, err := os.Open(path)
	if err != nil {
		return journalContents{}, asError(err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var payloads []journalPayload
	var validLen int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return journalContents{}, asError(readErr)
			}
			break
		}
		var envelope journalEnvelope
		if err := json.Unmarshal(bytes.TrimRight(line, "\r\n"), &envelope); err != nil {
			return journalContents{}, newErrorf("journal-corrupt", "恢复日志无法解析：%v", err)
		}
		payloadBytes, err := json.Marshal(envelope.Payload)
		if err != nil {
			return journalContents{}, newErrorf("journal-corrupt", "恢复日志无法校验：%v", err)
		}
		if checksum(payloadBytes) != envelope.Checksum {
			return journalContents{}, newError("journal-checksum-failed", "恢复日志校验失败")
		}
		payloads = append(payloads, envelope.Payload)
		validLen += int64(len(line))
	}
	if len(payloads) == 0 || payloads[0].Type != "header" || payloads[0].Baseline == nil {
		return journalContents{}, newError("journal-corrupt", "恢复日志缺少头部")
	}
	header := payloads[0]
	version := uint32(0)
	if header.Version != nil {
		version = *header.Version
	}
	if version != journalVersion {
		return journalContents{}, newErrorf("journal-version-unsupported", "不支持的恢复日志版本：%d", version)
	}
	contents := journalContents{baseline: *header.Baseline, validLen: validLen, snapshot: header.RecoverySnapshot}
	if header.PersistedRevision != nil {
		contents.persistedRevision = *header.PersistedRevision
	}
	for _, payload := range payloads[1:] {
		if payload.Type == "edit" && payload.Record != nil {
			contents.records = append(contents.records, *payload.Record)
		}
	}
	return contents, nil
}

// writePayload 写入一行 {checksum, payload}；校验和是 payload 紧凑 JSON 的 SHA-256。
func writePayload(writer io.Writer, payload journalPayload) error {
	if payload.Record != nil && payload.Record.Edits == nil {
		payload.Record.Edits = []Edit{}
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return newError("journal-serialize-failed", err.Error())
	}
	line, err := json.Marshal(journalEnvelope{Checksum: checksum(payloadBytes), Payload: payload})
	if err != nil {
		return newError("journal-write-failed", err.Error())
	}
	if _, err := writer.Write(append(line, '\n')); err != nil {
		return asError(err)
	}
	return nil
}

func checksum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// withExtension 等价于 Rust Path::with_extension：替换（或追加）最后一个扩展名。
func withExtension(path, extension string) string {
	base := filepath.Base(path)
	if dot := strings.LastIndexByte(base, '.'); dot > 0 {
		path = path[:len(path)-len(base)+dot]
	}
	return path + "." + extension
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
