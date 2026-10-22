package textdoc

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

const (
	indexChunkBytes = 256 * 1024
	// checkpointStrideBytes 是稀疏行检查点间距；1 GB 文档约 1000 个检查点。
	checkpointStrideBytes = 1024 * 1024
	lineIndexCacheVersion = 3
)

// lineIndexEdit 是一次编辑对行索引的影响，坐标相对编辑前的 revision。
type lineIndexEdit struct {
	fromByte      uint64
	toByte        uint64
	insertedBytes uint64
	insertedLines uint64
	deletedLines  uint64
}

type lineIndexDelta struct {
	baseRevision uint64
	revision     uint64
	edits        []lineIndexEdit
}

type lineIndexBuildCheckpoint struct {
	byteOffset       uint64
	line             uint64
	jsonLexicalState JSONLexicalState
	previousCR       bool
}

// lineCheckpoint 是行号映射的稀疏检查点，也随行索引缓存持久化（字段名沿用 snake_case）。
type lineCheckpoint struct {
	ByteOffset       uint64           `json:"byte_offset"`
	Line             uint64           `json:"line"`
	JSONLexicalState JSONLexicalState `json:"json_lexical_state"`
	JSONStateValid   bool             `json:"json_state_valid"`
	// PreviousCR：块尾 CR 尚不能判定是 CRLF 还是孤立 CR，后续补扫从此状态继续。
	PreviousCR bool `json:"previous_cr"`
}

// UnmarshalJSON 让缺失的 json_state_valid 默认为 true，兼容旧版本缓存。
func (c *lineCheckpoint) UnmarshalJSON(data []byte) error {
	type plain lineCheckpoint
	decoded := plain{JSONStateValid: true}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = lineCheckpoint(decoded)
	return nil
}

// lineIndex 记录行检查点、UTF-8 合法性与换行风格。实例发布到会话状态后视为不可变：
// 任何修改都先 clone 再整体替换，读者可以在锁外安全使用旧实例。
type lineIndex struct {
	Revision         uint64           `json:"revision"`
	IndexedBytes     uint64           `json:"indexed_bytes"`
	TotalBytes       uint64           `json:"total_bytes"`
	EstimatedLines   uint64           `json:"estimated_lines"`
	Completed        bool             `json:"completed"`
	Checkpoints      []lineCheckpoint `json:"checkpoints"`
	UTF8Valid        bool             `json:"utf8_valid"`
	LineEnding       LineEnding       `json:"line_ending"`
	JSONValidThrough uint64           `json:"json_valid_through"`
	LineEndingExact  bool             `json:"line_ending_exact"`
}

type lineIndexCache struct {
	Version     uint32    `json:"version"`
	BaselineKey string    `json:"baselineKey"`
	Index       lineIndex `json:"index"`
}

func (l *lineIndex) clone() *lineIndex {
	next := *l
	next.Checkpoints = slices.Clone(l.Checkpoints)
	return &next
}

// loadLineIndexCache 读取与 baseline 匹配且已完成的缓存；不存在或不匹配时返回 nil。
func loadLineIndexCache(path, baselineKey string) (*lineIndex, error) {
	data, err := os.ReadFile(path)
	if isNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, asError(err)
	}
	var cache lineIndexCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, newErrorf("line-index-cache-corrupt", "行索引缓存无法解析：%v", err)
	}
	if cache.Version != lineIndexCacheVersion || cache.BaselineKey != baselineKey || !cache.Index.Completed {
		return nil, nil
	}
	if cache.Index.LineEnding == "" {
		cache.Index.LineEnding = LineEndingLF
	}
	return &cache.Index, nil
}

func (l *lineIndex) saveCache(path, baselineKey string) error {
	if !l.Completed {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return asError(err)
	}
	data, err := json.Marshal(lineIndexCache{Version: lineIndexCacheVersion, BaselineKey: baselineKey, Index: *l})
	if err != nil {
		return newErrorf("line-index-cache-write-failed", "行索引缓存无法序列化：%v", err)
	}
	temp := path[:len(path)-len(filepath.Ext(path))] + ".index.tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return asError(err)
	}
	return asError(os.Rename(temp, path))
}

func pendingLineIndex(totalBytes, revision uint64) *lineIndex {
	return &lineIndex{
		Revision:    revision,
		TotalBytes:  totalBytes,
		Checkpoints: []lineCheckpoint{{JSONLexicalState: defaultJSONState(), JSONStateValid: true}},
		LineEnding:  LineEndingLF,
	}
}

// applyDelta 把 revision 化的编辑映射到已完成索引的检查点；只调整有界元数据，不扫描正文。
func (l *lineIndex) applyDelta(delta lineIndexDelta) (*lineIndex, error) {
	if !l.Completed || l.Revision != delta.baseRevision {
		return nil, newErrorf("line-index-delta-base-mismatch", "行索引 delta 基线不匹配：index=%d, delta=%d", l.Revision, delta.baseRevision)
	}
	next := l.clone()
	for i := len(delta.edits) - 1; i >= 0; i-- {
		if err := next.applyEdit(delta.edits[i]); err != nil {
			return nil, err
		}
	}
	next.Revision = delta.revision
	next.IndexedBytes = next.TotalBytes
	next.Completed = true
	next.LineEndingExact = len(delta.edits) == 0 && next.LineEndingExact
	return next, nil
}

func (l *lineIndex) applyEdit(edit lineIndexEdit) error {
	removed := edit.toByte - min(edit.toByte, edit.fromByte)
	byteDelta := int64(edit.insertedBytes) - int64(removed)
	lineDelta := int64(edit.insertedLines) - int64(edit.deletedLines)
	checkpoints := make([]lineCheckpoint, 0, len(l.Checkpoints))
	for _, checkpoint := range l.Checkpoints {
		if checkpoint.ByteOffset <= edit.fromByte {
			checkpoints = append(checkpoints, checkpoint)
			continue
		}
		if checkpoint.ByteOffset < edit.toByte {
			continue
		}
		var err error
		if checkpoint.ByteOffset, err = addSigned(checkpoint.ByteOffset, byteDelta); err != nil {
			return err
		}
		if checkpoint.Line, err = addSigned(checkpoint.Line, lineDelta); err != nil {
			return err
		}
		// 纯删除时优先保留 from 处原有检查点，避免 JSON 状态被删除区尾状态覆盖。
		if checkpoint.ByteOffset == edit.fromByte && len(checkpoints) > 0 && checkpoints[len(checkpoints)-1].ByteOffset == edit.fromByte {
			continue
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	l.Checkpoints = checkpoints
	var err error
	if l.TotalBytes, err = addSigned(l.TotalBytes, byteDelta); err != nil {
		return err
	}
	if l.EstimatedLines, err = addSigned(l.EstimatedLines, lineDelta); err != nil {
		return err
	}
	l.JSONValidThrough = min(l.JSONValidThrough, edit.fromByte)
	l.LineEndingExact = false
	return nil
}

func addSigned(value uint64, delta int64) (uint64, error) {
	if delta < 0 && uint64(-delta) > value {
		return 0, newError("line-index-delta-overflow", "行索引 delta 导致字节或行号越界")
	}
	return uint64(int64(value) + delta), nil
}

func (l *lineIndex) progress() float64 {
	if l.TotalBytes == 0 || l.Completed {
		return 1
	}
	return float64(l.IndexedBytes) / float64(l.TotalBytes)
}

// nearestLineCheckpoint 返回不晚于 byte 的最近检查点：偏移、行号与块尾 CR 状态。
func (l *lineIndex) nearestLineCheckpoint(byteOffset uint64) (uint64, uint64, bool) {
	position := sort.Search(len(l.Checkpoints), func(i int) bool { return l.Checkpoints[i].ByteOffset > byteOffset })
	if position == 0 {
		if len(l.Checkpoints) == 0 {
			return 0, 0, false
		}
		position = 1
	}
	checkpoint := l.Checkpoints[position-1]
	return checkpoint.ByteOffset, checkpoint.Line, checkpoint.PreviousCR
}

// withLineCheckpoints 返回合并了前台补扫检查点的新索引，不提升 IndexedBytes，也不伪造 JSON/UTF-8 进度。
func (l *lineIndex) withLineCheckpoints(checkpoints []scannedCheckpoint) *lineIndex {
	next := l.clone()
	for _, scanned := range checkpoints {
		if scanned.byteOffset > next.TotalBytes {
			continue
		}
		index, found := slices.BinarySearchFunc(next.Checkpoints, scanned.byteOffset, func(c lineCheckpoint, target uint64) int {
			return compareUint64(c.ByteOffset, target)
		})
		if found {
			next.Checkpoints[index].Line = scanned.line
			next.Checkpoints[index].PreviousCR = scanned.previousCR
			continue
		}
		next.Checkpoints = slices.Insert(next.Checkpoints, index, lineCheckpoint{
			ByteOffset:       scanned.byteOffset,
			Line:             scanned.line,
			JSONLexicalState: defaultJSONState(),
			PreviousCR:       scanned.previousCR,
		})
	}
	return next
}

// withBuildProgress 返回发布了后台构建进度的新索引；只在跨过稀疏检查点时调用，复制成本有界。
func (l *lineIndex) withBuildProgress(indexedBytes, estimatedLines uint64, checkpoint lineIndexBuildCheckpoint) *lineIndex {
	next := l.clone()
	next.IndexedBytes = max(next.IndexedBytes, indexedBytes)
	next.EstimatedLines = estimatedLines
	next.JSONValidThrough = max(next.JSONValidThrough, indexedBytes)
	entry := lineCheckpoint{
		ByteOffset:       checkpoint.byteOffset,
		Line:             checkpoint.line,
		JSONLexicalState: checkpoint.jsonLexicalState,
		JSONStateValid:   true,
		PreviousCR:       checkpoint.previousCR,
	}
	index, found := slices.BinarySearchFunc(next.Checkpoints, checkpoint.byteOffset, func(c lineCheckpoint, target uint64) int {
		return compareUint64(c.ByteOffset, target)
	})
	if found {
		next.Checkpoints[index] = entry
	} else {
		next.Checkpoints = slices.Insert(next.Checkpoints, index, entry)
	}
	return next
}

// jsonCheckpoint 返回不晚于 byte 的最近一个 JSON 状态有效的检查点；超出已校验范围时无结果。
func (l *lineIndex) jsonCheckpoint(byteOffset uint64) (uint64, JSONLexicalState, bool) {
	if byteOffset > l.IndexedBytes || byteOffset > l.JSONValidThrough {
		return 0, JSONLexicalState{}, false
	}
	position := sort.Search(len(l.Checkpoints), func(i int) bool { return l.Checkpoints[i].ByteOffset > byteOffset })
	for i := position - 1; i >= 0; i-- {
		if l.Checkpoints[i].JSONStateValid {
			return l.Checkpoints[i].ByteOffset, l.Checkpoints[i].JSONLexicalState, true
		}
	}
	return 0, JSONLexicalState{}, false
}

// buildLineIndex 全文扫描快照，统计行数、UTF-8 合法性、换行风格与 JSON 词法检查点。
// shouldCancel 返回 true 时放弃并返回 (nil, nil)；onProgress 在每个新检查点发布进度。
func buildLineIndex(sessionID string, snap *snapshot, sink EventSink, onProgress func(uint64, uint64, lineIndexBuildCheckpoint), shouldCancel func() bool) (*lineIndex, error) {
	index := pendingLineIndex(snap.len(), snap.revision)
	var offset, lines uint64
	nextCheckpoint := uint64(checkpointStrideBytes)
	jsonState := defaultJSONState()
	var utf8Tail []byte
	utf8Valid := true
	previousCR := false
	var lf, crlf, loneCR uint64
	for offset < snap.len() {
		if shouldCancel() {
			return nil, nil
		}
		data, err := snap.readRange(offset, indexChunkBytes)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			break
		}
		utf8Tail, utf8Valid = updateUTF8Validation(utf8Tail, data, utf8Valid)
		updateLineEndings(data, &previousCR, &lf, &crlf, &loneCR, &lines)
		advanceJSONState(&jsonState, data)
		offset += uint64(len(data))
		var published *lineIndexBuildCheckpoint
		if offset >= nextCheckpoint {
			// 检查点必须与已统计的真实块尾对齐，不能把块尾行数冒充为块内偏移。
			index.Checkpoints = append(index.Checkpoints, lineCheckpoint{
				ByteOffset: offset, Line: lines, JSONLexicalState: jsonState, JSONStateValid: true, PreviousCR: previousCR,
			})
			published = &lineIndexBuildCheckpoint{byteOffset: offset, line: lines, jsonLexicalState: jsonState, previousCR: previousCR}
			nextCheckpoint = offset + checkpointStrideBytes
		}
		index.IndexedBytes = offset
		index.EstimatedLines = lines
		index.JSONValidThrough = offset
		if onProgress != nil && published != nil {
			onProgress(index.IndexedBytes, index.EstimatedLines, *published)
		}
		emitIndexProgress(sessionID, snap.revision, index, false, sink)
	}
	index.IndexedBytes = snap.len()
	if previousCR {
		loneCR++
		lines++
	}
	index.EstimatedLines = lines
	if last := len(index.Checkpoints) - 1; last < 0 || index.Checkpoints[last].ByteOffset != snap.len() {
		index.Checkpoints = append(index.Checkpoints, lineCheckpoint{
			ByteOffset: snap.len(), Line: lines, JSONLexicalState: jsonState, JSONStateValid: true,
		})
	} else {
		index.Checkpoints[last].Line = lines
		index.Checkpoints[last].PreviousCR = false
	}
	index.UTF8Valid = utf8Valid && len(utf8Tail) == 0
	index.LineEnding = classifyLineEnding(lf, crlf, loneCR)
	index.JSONValidThrough = snap.len()
	index.LineEndingExact = true
	index.Completed = true
	if shouldCancel() {
		return nil, nil
	}
	return index, nil
}

// advanceJSONState 推进 JSON 词法状态：只区分字符串内外及转义。
func advanceJSONState(state *JSONLexicalState, data []byte) {
	for _, b := range data {
		switch {
		case state.Mode != JSONModeString:
			if b == '"' {
				state.Mode = JSONModeString
				state.Escaped = false
			}
		case state.Escaped:
			state.Escaped = false
		case b == '\\':
			state.Escaped = true
		case b == '"':
			state.Mode = JSONModeDefault
			state.Escaped = false
		}
	}
}

// updateUTF8Validation 流式校验 UTF-8：块尾不完整字符暂存到 tail，与下一块拼接后再判断。
func updateUTF8Validation(tail, data []byte, valid bool) ([]byte, bool) {
	if !valid {
		return tail, false
	}
	combined := append(slices.Clone(tail), data...)
	validUpTo, definite := utf8Prefix(combined)
	if validUpTo == len(combined) {
		return tail[:0], true
	}
	if definite {
		return nil, false
	}
	return slices.Clone(combined[validUpTo:]), true
}

func updateLineEndings(data []byte, previousCR *bool, lf, crlf, loneCR, lines *uint64) {
	for _, b := range data {
		if b == '\n' {
			if *previousCR {
				*crlf++
			} else {
				*lf++
			}
			*lines++
			*previousCR = false
			continue
		}
		if *previousCR {
			*loneCR++
			*lines++
		}
		*previousCR = b == '\r'
	}
}

func emitIndexProgress(sessionID string, revision uint64, index *lineIndex, completed bool, sink EventSink) {
	if sink == nil {
		return
	}
	id := fmt.Sprintf("index-%s-%d", sessionID, revision)
	sink(Event{Index: &IndexProgressEvent{
		SessionID:      sessionID,
		TaskID:         id,
		RequestID:      id,
		Revision:       revision,
		IndexedBytes:   index.IndexedBytes,
		TotalBytes:     index.TotalBytes,
		EstimatedLines: index.EstimatedLines,
		Completed:      completed,
	}})
}

type scannedCheckpoint struct {
	byteOffset uint64
	line       uint64
	previousCR bool
}

func compareUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
