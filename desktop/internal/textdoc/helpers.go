package textdoc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"nomo/desktop/internal/applog"
)

func utf8Valid(data []byte) bool { return utf8.Valid(data) }

func documentKindFromPath(path string) (DocumentKind, error) {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "txt":
		return KindText, nil
	case "json":
		return KindJSON, nil
	}
	return "", newError("unsupported-document-kind", "分段编辑器只允许打开 .txt 和 .json 文件")
}

// absoluteNormalizedPath 把相对路径基于工作目录转为绝对路径，并词法消除 . 与 ..（不解析符号链接）。
func absoluteNormalizedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", asError(err)
	}
	return absolute, nil
}

// preparedHistoryOperation 是撤销/重做在锁外预先算好的结果；original 持有一个引用。
type preparedHistoryOperation struct {
	tree         pieceTree
	original     *originalFile
	journalEdits []Edit
	replacement  *journalReplacementFile
	indexEdits   []lineIndexEdit
}

func prepareHistoryOperation(s *session, snap *snapshot, operation historyOperation, lineEnding LineEnding) (*preparedHistoryOperation, error) {
	if operation.replacement != nil {
		original, err := openOriginal(operation.replacement.Path, 0)
		if err != nil {
			return nil, err
		}
		replacement := *operation.replacement
		return &preparedHistoryOperation{
			tree:         treeFromOriginal(original.bodyLen),
			original:     original,
			journalEdits: []Edit{},
			replacement:  &replacement,
		}, nil
	}
	edits := sortedEdits(operation.edits)
	if err := validateEditBatch(snap, edits); err != nil {
		return nil, err
	}
	type preparedEdit struct {
		from, to uint64
		offset   *uint64
		inserted []byte
	}
	prepared := make([]preparedEdit, 0, len(edits))
	journalEdits := make([]Edit, 0, len(edits))
	indexEdits := make([]lineIndexEdit, 0, len(edits))
	for _, edit := range edits {
		inserted := normalizeInsertedText(edit.InsertedText, lineEnding)
		deleted, err := snap.readRange(edit.FromByte, int(edit.ToByte-min(edit.ToByte, edit.FromByte)))
		if err != nil {
			return nil, err
		}
		var offset *uint64
		if len(inserted) > 0 {
			value, err := s.added.append(inserted)
			if err != nil {
				return nil, err
			}
			offset = &value
		}
		if !utf8.Valid(inserted) {
			return nil, newError("invalid-utf8", "历史补丁不是有效 UTF-8")
		}
		journalEdits = append(journalEdits, Edit{FromByte: edit.FromByte, ToByte: edit.ToByte, InsertedText: string(inserted)})
		indexEdits = append(indexEdits, lineIndexEdit{
			fromByte: edit.FromByte, toByte: edit.ToByte, insertedBytes: uint64(len(inserted)),
			insertedLines: countLineBreaks(inserted), deletedLines: countLineBreaks(deleted),
		})
		prepared = append(prepared, preparedEdit{from: edit.FromByte, to: edit.ToByte, offset: offset, inserted: inserted})
	}
	tree := snap.tree
	for i := len(prepared) - 1; i >= 0; i-- {
		edit := prepared[i]
		var p *piece
		if edit.offset != nil {
			value := addedPiece(*edit.offset, uint64(len(edit.inserted)), countLineBreaks(edit.inserted))
			p = &value
		}
		var err error
		if tree, err = tree.replaceRange(edit.from, edit.to, p); err != nil {
			return nil, err
		}
	}
	return &preparedHistoryOperation{
		tree:         tree,
		original:     snap.original.retain(),
		journalEdits: journalEdits,
		indexEdits:   indexEdits,
	}, nil
}

func validateEditTransactionSize(edits []Edit) error {
	var total uint64
	overflow := false
	for _, edit := range edits {
		removed := edit.ToByte - min(edit.ToByte, edit.FromByte)
		next := total + removed + uint64(len(edit.InsertedText))
		if next < total {
			overflow = true
			break
		}
		total = next
	}
	if overflow || total > maxEditTransactionBytes {
		return newErrorf("edit-transaction-too-large", "单次增量编辑不得超过 %d 字节；大范围操作请使用后台任务", maxEditTransactionBytes)
	}
	return nil
}

// validateEditBatch 要求编辑已排序、在范围内、不重叠，且端点不拆分 UTF-8 字符或 CRLF。
func validateEditBatch(snap *snapshot, edits []Edit) error {
	for _, edit := range edits {
		if edit.FromByte > edit.ToByte || edit.ToByte > snap.len() {
			return newErrorf("invalid-edit-range", "编辑范围越界：%d..%d, len=%d", edit.FromByte, edit.ToByte, snap.len())
		}
		if err := validateTextBoundary(snap, edit.FromByte); err != nil {
			return err
		}
		if err := validateTextBoundary(snap, edit.ToByte); err != nil {
			return err
		}
	}
	for i := 1; i < len(edits); i++ {
		previous, current := edits[i-1], edits[i]
		sameInsertPosition := previous.FromByte == previous.ToByte && current.FromByte == current.ToByte && previous.FromByte == current.FromByte
		if previous.ToByte > current.FromByte || sameInsertPosition {
			return newError("overlapping-edits", "同一批编辑范围重叠或顺序不确定")
		}
	}
	return nil
}

func validateTextBoundary(snap *snapshot, offset uint64) error {
	if offset == 0 || offset == snap.len() {
		return nil
	}
	b, ok, err := snap.byteAt(offset)
	if err != nil {
		return err
	}
	if ok && b&0b1100_0000 == 0b1000_0000 {
		return newErrorf("utf8-boundary-split", "字节位置 %d 位于 UTF-8 字符内部", offset)
	}
	inside, err := isInsideCRLF(snap, offset)
	if err != nil {
		return err
	}
	if inside {
		return newErrorf("crlf-boundary-split", "字节位置 %d 位于 CRLF 内部", offset)
	}
	return nil
}

func publishRecoveryBaseline(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return asError(err)
	}
	if err := removeIfExists(target); err != nil {
		return asError(err)
	}
	// source 与 recovery 位于同一状态根目录；硬链接只增加目录项，不扫描正文。
	if err := os.Link(source, target); err != nil {
		return newErrorf("recovery-baseline-publish-failed", "发布不可变恢复 baseline 失败：%v", err)
	}
	return asError(syncParentDir(target))
}

func ensureBaselineUnchanged(s *session, path string) error {
	s.baselineMu.Lock()
	expected := s.baseline
	s.baselineMu.Unlock()
	actual, err := readIdentity(path)
	if err != nil {
		return err
	}
	if !actual.equal(expected) {
		return newError("external-file-changed", "保存前检测到外部文件已变化")
	}
	return nil
}

func ensureOpenIdentityUnchanged(path string, file *os.File, expected baselineIdentity) error {
	handleIdentity, err := readFileIdentity(path, file)
	if err != nil {
		return err
	}
	pathIdentity, err := readIdentity(path)
	if err != nil {
		return err
	}
	if !handleIdentity.equal(expected) || !pathIdentity.equal(expected) {
		return newError("file-changed-during-open", "读取首窗或建立不可变 baseline 期间源文件发生变化，请重试")
	}
	return nil
}

// normalizeSaveTarget 解析保存目标的真实路径；目标不存在时解析其父目录。
func normalizeSaveTarget(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", newErrorf("save-target-invalid", "解析保存目标失败：%v", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !isNotExist(err) {
		return "", newErrorf("save-target-invalid", "解析保存目标失败：%v", err)
	}
	name := filepath.Base(absolute)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "", newError("save-target-invalid", "保存目标缺少文件名")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", newErrorf("save-directory-missing", "解析保存目录失败：%v", err)
	}
	return filepath.Join(parent, name), nil
}

func cleanupUnreferencedHistoryAssets(state *sessionState, journal *editJournal) {
	var removed []string
	for _, asset := range state.history.cleanupCandidates(journal.referencedAssets()) {
		if err := removeIfExists(asset); err != nil {
			applog.Warn("SegmentedDocument", "清理已淘汰历史资产失败："+err.Error())
			continue
		}
		removed = append(removed, asset)
	}
	state.history.forgetAssets(removed)
}

// replayRecord 把一条日志记录重放到树上：替换文件整体追加进 Added，编辑倒序应用。
func replayRecord(tree pieceTree, added *addedStore, record journalRecord) (pieceTree, error) {
	if record.ReplacementFile != nil {
		file, err := os.Open(record.ReplacementFile.Path)
		if err != nil {
			return pieceTree{}, newErrorf("journal-replacement-missing", "恢复任务结果失败：%v", err)
		}
		defer file.Close()
		offset, length, err := added.appendReader(file)
		if err != nil {
			return pieceTree{}, err
		}
		return tree.replaceAllWithAdded(offset, length, record.ReplacementFile.LineBreaks), nil
	}
	next := tree
	edits := sortedEdits(record.Edits)
	for i := len(edits) - 1; i >= 0; i-- {
		edit := edits[i]
		var p *piece
		if data := []byte(edit.InsertedText); len(data) > 0 {
			offset, err := added.append(data)
			if err != nil {
				return pieceTree{}, err
			}
			value := addedPiece(offset, uint64(len(data)), countLineBreaks(data))
			p = &value
		}
		var err error
		if next, err = next.replaceRange(edit.FromByte, edit.ToByte, p); err != nil {
			return pieceTree{}, err
		}
	}
	return next, nil
}

// materializeRecoveryDocument 把冲突日志重放为一份用户可读的恢复文档：优先写在源文件旁，
// 不可写时退回应用目录 recovered/。返回恢复文档路径。
func materializeRecoveryDocument(sourcePath, stateRoot string, conflict *recoveryConflict) (string, error) {
	recovery := conflict.snapshot
	if recovery == nil {
		return "", newErrorf("recovery-snapshot-missing", "旧恢复日志缺少可物化快照，已保留在 %s", conflict.journalPath)
	}
	original, err := openOriginal(recovery.Path, recovery.BomLen)
	if err != nil {
		return "", err
	}
	defer original.release()
	added, err := createAddedStore(filepath.Join(stateRoot, "sessions", nextSessionID()+".recovery.added"))
	if err != nil {
		return "", err
	}
	defer added.remove()
	tree := treeFromOriginal(original.bodyLen)
	revision := recovery.Revision
	for _, record := range conflict.records {
		if record.Revision <= recovery.Revision {
			continue
		}
		if record.BaseRevision != revision {
			return "", newError("recovery-snapshot-revision-gap", "恢复快照后的 journal revision 不连续")
		}
		if tree, err = replayRecord(tree, added, record); err != nil {
			return "", err
		}
		revision = record.Revision
	}
	recovered := &snapshot{revision: revision, tree: tree, original: original, added: added}
	timestamp := time.Now().UnixMilli()
	ext := filepath.Ext(sourcePath)
	stem := strings.TrimSuffix(filepath.Base(sourcePath), ext)
	extension := strings.TrimPrefix(ext, ".")
	if extension == "" {
		extension = "txt"
	}
	if stem == "" {
		stem = "document"
	}
	path, err := writeRecoveredDocument(filepath.Dir(sourcePath), stem, extension, timestamp, recovered, recovery.BomLen, false)
	if err == nil {
		return path, nil
	}
	applog.Warn("SegmentedDocument", "源目录无法写入恢复文档，改用应用目录："+err.Error())
	return writeRecoveredDocument(filepath.Join(stateRoot, "recovered"), stem, extension, timestamp, recovered, recovery.BomLen, false)
}

// writeRecoveredDocument 先写临时文件，再以“硬链接到不冲突的新名字”发布，绝不覆盖已有文件；
// 硬链接不受支持时退回 O_EXCL 新建并复制。
func writeRecoveredDocument(directory, stem, extension string, timestamp int64, snap *snapshot, bomLen uint64, forceHardLinkFailure bool) (string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", asError(err)
	}
	tempPath := filepath.Join(directory, fmt.Sprintf(".%s.recovered-%d-%d.tmp", stem, timestamp, os.Getpid()))
	if err := removeIfExists(tempPath); err != nil {
		return "", asError(err)
	}
	output, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", asError(err)
	}
	writeErr := func() error {
		if bomLen == uint64(len(utf8BOM)) {
			if _, err := output.Write(utf8BOM); err != nil {
				return err
			}
		}
		if _, err := io.Copy(output, snap.reader()); err != nil {
			return err
		}
		return output.Sync()
	}()
	output.Close()
	if writeErr != nil {
		_ = removeIfExists(tempPath)
		return "", asError(writeErr)
	}
	for attempt := 0; attempt < 10_000; attempt++ {
		recoveredPath := filepath.Join(directory, fmt.Sprintf("%s.recovered-%d-%d.%s", stem, timestamp, attempt, extension))
		linkErr := errors.ErrUnsupported
		if !forceHardLinkFailure {
			linkErr = os.Link(tempPath, recoveredPath)
		}
		if linkErr == nil {
			if err := removeIfExists(tempPath); err != nil {
				return "", asError(err)
			}
			return recoveredPath, asError(syncDir(directory))
		}
		if errors.Is(linkErr, os.ErrExist) {
			continue
		}
		target, err := os.OpenFile(recoveredPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			_ = removeIfExists(tempPath)
			return "", asError(err)
		}
		copyErr := func() error {
			source, err := os.Open(tempPath)
			if err != nil {
				return err
			}
			defer source.Close()
			if _, err := io.Copy(target, source); err != nil {
				return err
			}
			return target.Sync()
		}()
		target.Close()
		if copyErr != nil {
			_ = removeIfExists(recoveredPath)
			_ = removeIfExists(tempPath)
			return "", asError(copyErr)
		}
		if err := removeIfExists(tempPath); err != nil {
			return "", asError(err)
		}
		return recoveredPath, asError(syncDir(directory))
	}
	_ = removeIfExists(tempPath)
	return "", newError("recovery-path-exhausted", "无法为恢复文档分配无覆盖文件名")
}

// recoveryKey 是路径的稳定散列：优先使用真实路径；文件已删除时用真实父目录 + 文件名，
// 避免 macOS /var 与 /private/var 这类符号链接前缀改变键。
func recoveryKey(path string) string {
	normalized := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		normalized = resolved
	} else if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		normalized = filepath.Join(parent, filepath.Base(path))
	}
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:])
}

func baselineCacheKey(identity baselineIdentity) string {
	return identity.Path + ":" + strconv.FormatUint(identity.PhysicalLen, 10) + ":" +
		strconv.FormatUint(identity.ModifiedNanos, 10) + ":" + optionalString(identity.FileID) + ":" + optionalString(identity.ChangeStamp)
}

func externalChangeToken(change ExternalChangeKind, current *baselineIdentity, baseline baselineIdentity) string {
	identity := baseline
	if current != nil {
		identity = *current
	}
	payload := strings.Join([]string{
		string(change), identity.Path, strconv.FormatUint(identity.PhysicalLen, 10),
		strconv.FormatUint(identity.ModifiedNanos, 10), optionalString(identity.FileID), optionalString(identity.ChangeStamp),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// cleanupCopyFallbacks 删除 tasks 目录中的复制兜底 .txt（keep 除外）；该目录还保存替换/撤销资产，不能整体清空。
func cleanupCopyFallbacks(directory, keep string) error {
	entries, err := os.ReadDir(directory)
	if isNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if path == keep || filepath.Ext(path) != ".txt" || !entry.Type().IsRegular() {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// cleanupSessionTransients 清空 sessions 目录：持久恢复 baseline 已硬链接到 recovery，这里只剩崩溃遗留。
func cleanupSessionTransients(directory string) error {
	entries, err := os.ReadDir(directory)
	if isNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// copySnapshotForSave 流式复制快照；failAfter ≥ 0 时（仅测试）在写入该字节数后模拟磁盘写满。
func copySnapshotForSave(reader io.Reader, writer io.Writer, failAfter int64) error {
	buffer := make([]byte, 64*1024)
	var written int64
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			allowed := n
			if failAfter >= 0 {
				allowed = int(min(int64(n), max(failAfter-written, 0)))
			}
			if allowed > 0 {
				if _, err := writer.Write(buffer[:allowed]); err != nil {
					return err
				}
				written += int64(allowed)
			}
			if allowed < n {
				return errors.New("No space left on device (test injection)")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func uniqueTempPath(parent, target string) string {
	name := filepath.Base(target)
	if name == "" || name == "." {
		name = "segmented-document"
	}
	return filepath.Join(parent, fmt.Sprintf(".%s.%d.%d.tmp", name, os.Getpid(), time.Now().UnixNano()))
}
