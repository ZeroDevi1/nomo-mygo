package textdoc

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"nomo/desktop/internal/applog"
)

const (
	taskChunkBytes   = 64 * 1024
	nearbyMatchLimit = 16
)

var nextTaskSequence atomic.Uint64

type taskControl struct {
	sessionID string
	cancel    *atomic.Bool
}

// taskContext 是一个后台任务的不可变上下文；snapshot 持有一个 original 引用，任务结束时释放。
type taskContext struct {
	manager      *Manager
	sessionID    string
	taskID       string
	baseRevision uint64
	kind         TaskKind
	snapshot     *snapshot
	documentKind DocumentKind
	lineEnding   LineEnding
	cancel       *atomic.Bool
	sink         EventSink
}

type taskOutcome struct {
	processedBytes    uint64
	matchCount        uint64
	resultRevision    *uint64
	resultByteLength  *uint64
	persistedRevision *uint64
	dirty             *bool
	outputPath        *string
	nearbyMatches     []TaskMatch
	currentMatch      *TaskMatch
	message           *string
}

var errTaskCancelled = newError("task-cancelled", "后台任务已取消")

// StartTask 启动后台任务并立即返回任务 ID；进度与结果经 sink 推送。
// 写类任务（全部替换、JSON 格式化）要求会话可写，并在提交时再次校验 revision。
func (m *Manager) StartTask(request StartTaskRequest, sink EventSink) (*StartTaskResult, error) {
	if !request.Task.validKind() {
		return nil, newErrorf("task-invalid-kind", "未知的后台任务类型：%s", request.Task.Type)
	}
	s, err := m.session(request.SessionID)
	if err != nil {
		return nil, err
	}
	if request.Task.writes() {
		if _, err := s.ensureWritable(); err != nil {
			return nil, err
		}
	}
	documentKind, lineEnding, readonly, snap, err := m.taskSnapshot(request.SessionID, request.BaseRevision)
	if err != nil {
		return nil, err
	}
	if request.Task.writes() && readonly {
		snap.release()
		return nil, newError("document-readonly", "当前编码或磁盘权限只允许只读打开")
	}
	taskID := "task-" + strconv.FormatInt(time.Now().UnixNano(), 16) + "-" + strconv.FormatUint(nextTaskSequence.Add(1), 16)
	cancel := &atomic.Bool{}
	m.tasksMu.Lock()
	m.tasks[taskID] = &taskControl{sessionID: request.SessionID, cancel: cancel}
	m.tasksMu.Unlock()
	context := &taskContext{
		manager: m, sessionID: request.SessionID, taskID: taskID, baseRevision: request.BaseRevision,
		kind: request.Task.Type, snapshot: snap, documentKind: documentKind, lineEnding: lineEnding, cancel: cancel, sink: sink,
	}
	context.emit(TaskRunning, &taskOutcome{}, nil)
	go func() {
		defer snap.release()
		defer m.finishTask(taskID)
		outcome, err := runTask(context, request.Task)
		if err == nil {
			context.emit(TaskCompleted, outcome, outcome.message)
			return
		}
		failure := structured(err)
		state := TaskFailed
		switch failure.Code {
		case "task-cancelled":
			state = TaskCancelled
		case "revision-conflict":
			state = TaskConflict
		}
		context.emit(state, &taskOutcome{}, &failure.Message)
	}()
	return &StartTaskResult{TaskID: taskID}, nil
}

// CancelTask 请求取消任务；任务不存在（已结束）时 Cancelled 为 false。
func (m *Manager) CancelTask(taskID string) *CancelTaskResult {
	m.tasksMu.Lock()
	defer m.tasksMu.Unlock()
	control, ok := m.tasks[taskID]
	if ok {
		control.cancel.Store(true)
	}
	return &CancelTaskResult{TaskID: taskID, Cancelled: ok}
}

func (m *Manager) finishTask(taskID string) {
	m.tasksMu.Lock()
	delete(m.tasks, taskID)
	m.tasksMu.Unlock()
}

// taskSnapshot 等待（至多 2 秒）当前 revision 的精确索引完成，再返回任务快照：
// 任务需要确定的换行风格来规范化替换文本与格式化输出。
func (m *Manager) taskSnapshot(sessionID string, baseRevision uint64) (DocumentKind, LineEnding, bool, *snapshot, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return "", "", false, nil, err
	}
	deadline := time.Now().Add(taskSnapshotIndexTimeout)
	for {
		snap, index, err := s.snapshotWithIndex()
		if err != nil {
			return "", "", false, nil, err
		}
		if snap.revision != baseRevision {
			snap.release()
			return "", "", false, nil, revisionConflict(baseRevision, snap.revision)
		}
		if index.Completed && index.Revision == snap.revision && index.LineEndingExact {
			meta := s.metadata()
			return s.kind, meta.lineEnding, meta.readonly(), snap, nil
		}
		snap.release()
		if !time.Now().Before(deadline) {
			return "", "", false, nil, newError("index-pending", "全文索引尚未完成，请在 completed 事件后重试")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (m *Manager) taskOutputPath(taskID, extension string) string {
	return filepath.Join(m.root, "tasks", taskID+"."+extension)
}

// publishCopyFallback 在锁内原子发布复制兜底文件并淘汰旧文件，防止并发复制互删刚发布的结果。
func (m *Manager) publishCopyFallback(temporary, final string) error {
	m.copyFallbMu.Lock()
	defer m.copyFallbMu.Unlock()
	if err := os.Rename(temporary, final); err != nil {
		return asError(err)
	}
	if err := cleanupCopyFallbacks(filepath.Join(m.root, "tasks"), final); err != nil {
		// 新结果已原子发布；清理失败只能保留旧文件，不能把可用路径误报成失败。
		applog.Warn("SegmentedDocument", "清理旧复制兜底失败，保留已发布的新文件："+err.Error())
	}
	return nil
}

type taskCommitState struct {
	revision          uint64
	byteLength        uint64
	persistedRevision uint64
	dirty             bool
}

// commitTaskOutput 把任务产出的完整新正文作为 revision+1 提交；撤销/重做以前后两个文件整体替换实现。
// 锁内只交换 Piece 元数据，不复制全文。
func (m *Manager) commitTaskOutput(sessionID string, baseRevision uint64, outputPath string, lineBreaks uint64, undoPath string, undoLineBreaks uint64, cancel *atomic.Bool) (*taskCommitState, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	output, err := openOriginal(outputPath, 0)
	if err != nil {
		return nil, err
	}
	undo, err := openOriginal(undoPath, 0)
	if err != nil {
		output.release()
		return nil, err
	}
	undoLen := undo.bodyLen
	undo.release()
	length := output.bodyLen
	historyWeight := undoLen + length
	undoReplacement := &journalReplacementFile{Path: undoPath, LineBreaks: undoLineBreaks}
	redoReplacement := &journalReplacementFile{Path: outputPath, LineBreaks: lineBreaks}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	fail := func(err error) (*taskCommitState, error) {
		output.release()
		return nil, err
	}
	if _, err := s.ensureWritable(); err != nil {
		return fail(err)
	}
	if cancel.Load() {
		return fail(errTaskCancelled)
	}
	s.stateMu.RLock()
	current := s.state.revision
	s.stateMu.RUnlock()
	if current != baseRevision {
		return fail(revisionConflict(baseRevision, current))
	}
	s.cache.clear()
	s.journalMu.Lock()
	s.stateMu.Lock()
	if s.state.revision != baseRevision {
		actual := s.state.revision
		s.stateMu.Unlock()
		s.journalMu.Unlock()
		return fail(revisionConflict(baseRevision, actual))
	}
	if cancel.Load() {
		s.stateMu.Unlock()
		s.journalMu.Unlock()
		return fail(errTaskCancelled)
	}
	s.state.history.pushTransaction(historyOperation{replacement: undoReplacement}, historyOperation{replacement: redoReplacement}, historyWeight)
	previousOriginal := s.state.original
	s.state.tree = treeFromOriginal(length)
	s.state.original = output
	s.state.revision++
	invalidateLineIndexForReplacement(&s.state)
	s.journal.record(journalRecord{BaseRevision: baseRevision, Revision: s.state.revision, Edits: []Edit{}, ReplacementFile: redoReplacement})
	cleanupUnreferencedHistoryAssets(&s.state, s.journal)
	result := &taskCommitState{revision: s.state.revision, byteLength: length, persistedRevision: s.state.persistedRevision, dirty: s.state.revision != s.state.persistedRevision}
	s.stateMu.Unlock()
	s.journalMu.Unlock()
	previousOriginal.release()
	scheduleJournalFlush(s)
	startLineIndex(s, true, false)
	return result, nil
}

func runTask(context *taskContext, task Task) (*taskOutcome, error) {
	options := matchOptions{caseSensitive: task.CaseSensitive, wholeWord: task.WholeWord}
	switch task.Type {
	case TaskSearch:
		return runSearch(context, task.Query, options, task.WrapAround, task.AnchorByte, task.Direction)
	case TaskReplaceAll:
		return runReplaceAll(context, task.Query, task.Replacement, options)
	case TaskSelectAllCopy:
		return runSelectAllCopy(context)
	case TaskJSONValidate:
		return runJSONValidate(context)
	default:
		return runJSONFormat(context)
	}
}

// matchPage 收集一页匹配：前向保留锚点后的前 16 条，反向保留锚点前最近的 16 条，内存不随文件增长。
type matchPage struct {
	items    []TaskMatch
	backward bool
}

func (p *matchPage) push(match TaskMatch) {
	if p.backward {
		if len(p.items) == nearbyMatchLimit {
			p.items = p.items[1:]
		}
		p.items = append(p.items, match)
	} else if len(p.items) < nearbyMatchLimit {
		p.items = append(p.items, match)
	}
}

func runSearch(context *taskContext, query string, options matchOptions, wrapAround bool, anchor *uint64, direction *SearchDirection) (*taskOutcome, error) {
	if query == "" {
		return nil, newError("task-invalid-query", "搜索文本不能为空")
	}
	if (anchor != nil) != (direction != nil) || anchor != nil && *anchor > context.snapshot.len() {
		return nil, newError("task-invalid-anchor", "搜索分页必须同时提供有效的 anchorByte 和 direction")
	}
	backward := direction != nil && *direction == SearchBackward
	outcome := &taskOutcome{}
	page := &matchPage{backward: backward}
	wrapPage := &matchPage{backward: backward}
	record := func(found TaskMatch) {
		outcome.matchCount++
		eligible := anchor == nil || (backward && found.StartByte < *anchor) || (!backward && found.StartByte > *anchor)
		if eligible {
			page.push(found)
		}
		if wrapAround && anchor != nil {
			wrapPage.push(found)
		}
	}
	scanner := newStreamingMatchScanner([]byte(query), options)
	var offset uint64
	for offset < context.snapshot.len() {
		if context.cancel.Load() {
			return nil, errTaskCancelled
		}
		chunk, err := context.snapshot.readRange(offset, taskChunkBytes)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}
		for _, b := range chunk {
			if found, ok := scanner.push(b); ok {
				record(found)
			}
		}
		offset += uint64(len(chunk))
		outcome.processedBytes = offset
		context.emit(TaskRunning, outcome, nil)
	}
	if found, ok := scanner.finish(); ok {
		record(found)
	}
	if len(page.items) == 0 && wrapAround && anchor != nil {
		page = wrapPage
	}
	outcome.nearbyMatches = page.items
	if len(page.items) > 0 {
		current := page.items[0]
		if backward {
			current = page.items[len(page.items)-1]
		}
		outcome.currentMatch = &current
	}
	return outcome, nil
}

// lineCountingWriter 写任务输出文件并累计 LF 字节数（与日志中替换文件的 lineBreaks 语义一致）。
type lineCountingWriter struct {
	file       *os.File
	buffer     *bufio.Writer
	lineBreaks uint64
}

func createTaskOutput(path string) (*lineCountingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, asError(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, asError(err)
	}
	return &lineCountingWriter{file: file, buffer: bufio.NewWriterSize(file, taskChunkBytes)}, nil
}

func (w *lineCountingWriter) write(data []byte) error {
	w.lineBreaks += uint64(bytes.Count(data, []byte{'\n'}))
	_, err := w.buffer.Write(data)
	return asError(err)
}

// finish 刷新缓冲并 fsync，返回 LF 计数。
func (w *lineCountingWriter) finish() (uint64, error) {
	defer w.file.Close()
	if err := w.buffer.Flush(); err != nil {
		return 0, asError(err)
	}
	if err := w.file.Sync(); err != nil {
		return 0, asError(err)
	}
	return w.lineBreaks, nil
}

func (w *lineCountingWriter) abort() { w.file.Close() }

func cleanupTaskOutput(path string) {
	if err := removeIfExists(path); err != nil {
		applog.Warn("SegmentedDocument", "清理后台任务临时文件失败："+err.Error())
	}
}

func copySnapshotRange(context *taskContext, start, end uint64, output *lineCountingWriter) error {
	for cursor := start; cursor < end; {
		if context.cancel.Load() {
			return errTaskCancelled
		}
		chunk, err := context.snapshot.readRange(cursor, int(min(end-cursor, taskChunkBytes)))
		if err != nil {
			return err
		}
		if len(chunk) == 0 {
			return newError("task-snapshot-short-read", "替换任务读取源文档时提前结束")
		}
		if err := output.write(chunk); err != nil {
			return err
		}
		cursor += uint64(len(chunk))
	}
	return nil
}

// runReplaceAll 流式生成替换后的完整正文与原文副本（供撤销），再整体提交为一个可撤销事务。
func runReplaceAll(context *taskContext, query, replacement string, options matchOptions) (outcome *taskOutcome, err error) {
	if query == "" {
		return nil, newError("task-invalid-query", "替换文本不能为空")
	}
	outputPath := context.manager.taskOutputPath(context.taskID, "replace")
	undoPath := context.manager.taskOutputPath(context.taskID, "undo")
	output, err := createTaskOutput(outputPath)
	if err != nil {
		return nil, err
	}
	undo, err := createTaskOutput(undoPath)
	if err != nil {
		output.abort()
		cleanupTaskOutput(outputPath)
		return nil, err
	}
	finished := false
	defer func() {
		if err != nil {
			if !finished {
				output.abort()
				undo.abort()
			}
			cleanupTaskOutput(outputPath)
			cleanupTaskOutput(undoPath)
		}
	}()
	normalized := normalizeInsertedText(replacement, context.lineEnding)
	scanner := newStreamingMatchScanner([]byte(query), options)
	var offset, outputOffset, matchCount uint64
	emitMatch := func(found TaskMatch) error {
		if err := copySnapshotRange(context, outputOffset, found.StartByte, output); err != nil {
			return err
		}
		if err := output.write(normalized); err != nil {
			return err
		}
		outputOffset = found.EndByte
		matchCount++
		return nil
	}
	for offset < context.snapshot.len() {
		if context.cancel.Load() {
			return nil, errTaskCancelled
		}
		chunk, err := context.snapshot.readRange(offset, taskChunkBytes)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}
		if err := undo.write(chunk); err != nil {
			return nil, err
		}
		for _, b := range chunk {
			if found, ok := scanner.push(b); ok {
				if err := emitMatch(found); err != nil {
					return nil, err
				}
			}
		}
		offset += uint64(len(chunk))
		context.emit(TaskRunning, &taskOutcome{processedBytes: offset, matchCount: matchCount}, nil)
	}
	if found, ok := scanner.finish(); ok {
		if err := emitMatch(found); err != nil {
			return nil, err
		}
	}
	if err := copySnapshotRange(context, outputOffset, context.snapshot.len(), output); err != nil {
		return nil, err
	}
	finished = true
	lineBreaks, err := output.finish()
	if err != nil {
		undo.abort()
		return nil, err
	}
	undoLineBreaks, err := undo.finish()
	if err != nil {
		return nil, err
	}
	if context.cancel.Load() {
		return nil, errTaskCancelled
	}
	commit, err := context.manager.commitTaskOutput(context.sessionID, context.baseRevision, outputPath, lineBreaks, undoPath, undoLineBreaks, context.cancel)
	if err != nil {
		return nil, err
	}
	return &taskOutcome{
		processedBytes:    context.snapshot.len(),
		matchCount:        matchCount,
		resultRevision:    &commit.revision,
		resultByteLength:  &commit.byteLength,
		persistedRevision: &commit.persistedRevision,
		dirty:             &commit.dirty,
	}, nil
}

// runSelectAllCopy 优先流式写入系统剪贴板；不可用时写到 tasks/<id>.txt 并返回路径。
func runSelectAllCopy(context *taskContext) (*taskOutcome, error) {
	if !context.manager.hooks.forceCopyFallback.Load() {
		streamed, err := streamToPlatformClipboard(context)
		if err != nil {
			return nil, err
		}
		if streamed {
			message := "已流式写入系统剪贴板"
			return &taskOutcome{processedBytes: context.snapshot.len(), message: &message}, nil
		}
	}
	outputPath := context.manager.taskOutputPath(context.taskID, "txt")
	temporaryPath := context.manager.taskOutputPath(context.taskID, "copy.tmp")
	offset, err := func() (uint64, error) {
		output, err := createTaskOutput(temporaryPath)
		if err != nil {
			return 0, err
		}
		var offset uint64
		for offset < context.snapshot.len() {
			if context.cancel.Load() {
				output.abort()
				return 0, errTaskCancelled
			}
			chunk, err := context.snapshot.readRange(offset, taskChunkBytes)
			if err != nil {
				output.abort()
				return 0, err
			}
			if len(chunk) == 0 {
				break
			}
			if _, err := output.buffer.Write(chunk); err != nil {
				output.abort()
				return 0, asError(err)
			}
			offset += uint64(len(chunk))
			context.emit(TaskRunning, &taskOutcome{processedBytes: offset}, nil)
		}
		if _, err := output.finish(); err != nil {
			return 0, err
		}
		if context.cancel.Load() {
			return 0, errTaskCancelled
		}
		return offset, nil
	}()
	if err != nil {
		cleanupTaskOutput(temporaryPath)
		return nil, err
	}
	// .copy.tmp 不参与兜底清理；完整同步后再在锁内发布，避免并发任务暴露半文件或失效路径。
	if err := context.manager.publishCopyFallback(temporaryPath, outputPath); err != nil {
		cleanupTaskOutput(temporaryPath)
		return nil, err
	}
	message := "系统剪贴板不可用，已回退为临时文件：" + outputPath
	return &taskOutcome{processedBytes: offset, outputPath: &outputPath, message: &message}, nil
}

// streamToPlatformClipboard 把正文写入 pbcopy / clip.exe 的标准输入；平台无此命令或写入失败时返回 false。
func streamToPlatformClipboard(context *taskContext) (bool, error) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("pbcopy")
	case "windows":
		command = exec.Command("clip.exe")
	default:
		return false, nil
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return false, nil
	}
	if err := command.Start(); err != nil {
		return false, nil
	}
	kill := func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	var offset uint64
	for offset < context.snapshot.len() {
		if context.cancel.Load() {
			stdin.Close()
			kill()
			return false, errTaskCancelled
		}
		chunk, err := context.snapshot.readRange(offset, taskChunkBytes)
		if err != nil {
			stdin.Close()
			kill()
			return false, err
		}
		if len(chunk) == 0 {
			break
		}
		if _, err := stdin.Write(chunk); err != nil {
			stdin.Close()
			kill()
			return false, nil
		}
		offset += uint64(len(chunk))
		context.emit(TaskRunning, &taskOutcome{processedBytes: offset}, nil)
	}
	stdin.Close()
	return command.Wait() == nil, nil
}

func runJSONValidate(context *taskContext) (*taskOutcome, error) {
	if err := requireJSON(context); err != nil {
		return nil, err
	}
	if err := validateJSON(context, 0, context.snapshot.len()); err != nil {
		return nil, err
	}
	return &taskOutcome{processedBytes: context.snapshot.len()}, nil
}

// cancellableSnapshotReader 读取快照并把进度映射到 [start, start+span] 区间上报。
type cancellableSnapshotReader struct {
	inner     *snapshotReader
	context   *taskContext
	processed uint64
	start     uint64
	span      uint64
}

func (r *cancellableSnapshotReader) Read(output []byte) (int, error) {
	if r.context.cancel.Load() {
		return 0, errTaskCancelled
	}
	n, err := r.inner.Read(output)
	r.processed += uint64(n)
	mapped := r.start + r.span
	if total := r.context.snapshot.len(); total > 0 {
		mapped = r.start + r.processed*r.span/total
	}
	r.context.emit(TaskRunning, &taskOutcome{processedBytes: mapped}, nil)
	return n, err
}

// validateJSON 流式校验整份 JSON（允许重复键，与原实现的宽松语义一致），尾部只能是空白。
func validateJSON(context *taskContext, progressStart, progressSpan uint64) error {
	if context.cancel.Load() {
		return errTaskCancelled
	}
	reader := &cancellableSnapshotReader{inner: context.snapshot.reader(), context: context, start: progressStart, span: progressSpan}
	// 缓冲把底层进度事件约束为有界的分块频率。
	decoder := jsontext.NewDecoder(bufio.NewReaderSize(reader, taskChunkBytes),
		jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	if err := decoder.SkipValue(); err != nil {
		if context.cancel.Load() {
			return errTaskCancelled
		}
		return newErrorf("json-invalid", "JSON 校验失败：%v", err)
	}
	if _, err := decoder.ReadToken(); !errors.Is(err, io.EOF) {
		if context.cancel.Load() {
			return errTaskCancelled
		}
		if err == nil {
			err = errors.New("trailing characters")
		}
		return newErrorf("json-invalid", "JSON 尾部数据无效：%v", err)
	}
	if context.cancel.Load() {
		return errTaskCancelled
	}
	context.emit(TaskRunning, &taskOutcome{processedBytes: progressStart + progressSpan}, nil)
	return nil
}

// runJSONFormat 先校验（占进度前半），再流式重排缩进（后半）：字符串内容原样保留，
// 字符串外删除空白并按结构插入换行与两空格缩进，换行风格跟随文档。
func runJSONFormat(context *taskContext) (outcome *taskOutcome, err error) {
	if err := requireJSON(context); err != nil {
		return nil, err
	}
	total := context.snapshot.len()
	validationSpan := total / 2
	formatSpan := total - validationSpan
	if err := validateJSON(context, 0, validationSpan); err != nil {
		return nil, err
	}
	outputPath := context.manager.taskOutputPath(context.taskID, "json")
	undoPath := context.manager.taskOutputPath(context.taskID, "undo")
	output, err := createTaskOutput(outputPath)
	if err != nil {
		return nil, err
	}
	undo, err := createTaskOutput(undoPath)
	if err != nil {
		output.abort()
		cleanupTaskOutput(outputPath)
		return nil, err
	}
	finished := false
	defer func() {
		if err != nil {
			if !finished {
				output.abort()
				undo.abort()
			}
			cleanupTaskOutput(outputPath)
			cleanupTaskOutput(undoPath)
		}
	}()
	newline := []byte("\n")
	if context.lineEnding == LineEndingCRLF {
		newline = []byte("\r\n")
	}
	writeIndent := func(depth int) error {
		for range depth {
			if err := output.write([]byte("  ")); err != nil {
				return err
			}
		}
		return nil
	}
	var offset uint64
	inString, escaped, lineStart := false, false, false
	depth := 0
	for offset < total {
		if context.cancel.Load() {
			return nil, errTaskCancelled
		}
		chunk, err := context.snapshot.readRange(offset, taskChunkBytes)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}
		if err := undo.write(chunk); err != nil {
			return nil, err
		}
		// inString/escaped 跨分块保留；只有字符串外的空白与结构标点会被格式化。
		for _, b := range chunk {
			if inString {
				if err := output.write([]byte{b}); err != nil {
					return nil, err
				}
				switch {
				case escaped:
					escaped = false
				case b == '\\':
					escaped = true
				case b == '"':
					inString = false
				}
				continue
			}
			if b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' {
				continue
			}
			if lineStart && b != '}' && b != ']' {
				if err := writeIndent(depth); err != nil {
					return nil, err
				}
				lineStart = false
			}
			var werr error
			switch b {
			case '"':
				werr = output.write([]byte{b})
				inString = true
			case '{', '[':
				if werr = output.write([]byte{b}); werr == nil {
					werr = output.write(newline)
				}
				depth++
				lineStart = true
			case '}', ']':
				depth = max(depth-1, 0)
				if !lineStart {
					werr = output.write(newline)
				}
				if werr == nil {
					werr = writeIndent(depth)
				}
				if werr == nil {
					werr = output.write([]byte{b})
				}
				lineStart = false
			case ',':
				if werr = output.write([]byte(",")); werr == nil {
					werr = output.write(newline)
				}
				lineStart = true
			case ':':
				werr = output.write([]byte(": "))
			default:
				werr = output.write([]byte{b})
			}
			if werr != nil {
				return nil, werr
			}
		}
		offset += uint64(len(chunk))
		// 校验与格式化共享同一总量区间，第二阶段从中点继续而不会回退到零。
		progress := validationSpan + formatSpan
		if total > 0 {
			progress = validationSpan + offset*formatSpan/total
		}
		context.emit(TaskRunning, &taskOutcome{processedBytes: progress}, nil)
	}
	finished = true
	lineBreaks, err := output.finish()
	if err != nil {
		undo.abort()
		return nil, err
	}
	undoLineBreaks, err := undo.finish()
	if err != nil {
		return nil, err
	}
	if context.cancel.Load() {
		return nil, errTaskCancelled
	}
	commit, err := context.manager.commitTaskOutput(context.sessionID, context.baseRevision, outputPath, lineBreaks, undoPath, undoLineBreaks, context.cancel)
	if err != nil {
		return nil, err
	}
	return &taskOutcome{
		processedBytes:    total,
		resultRevision:    &commit.revision,
		resultByteLength:  &commit.byteLength,
		persistedRevision: &commit.persistedRevision,
		dirty:             &commit.dirty,
	}, nil
}

func requireJSON(context *taskContext) error {
	if context.documentKind != KindJSON {
		return newError("task-invalid-kind", "JSON 任务只能用于 .json 文档")
	}
	return nil
}

func (c *taskContext) emit(state TaskState, outcome *taskOutcome, message *string) {
	if c.sink == nil {
		return
	}
	revision := c.baseRevision
	if outcome.resultRevision != nil {
		revision = *outcome.resultRevision
	}
	c.sink(Event{Task: &TaskProgressEvent{
		SessionID:         c.sessionID,
		TaskID:            c.taskID,
		BaseRevision:      c.baseRevision,
		Revision:          revision,
		RequestID:         c.taskID,
		Kind:              c.kind,
		State:             state,
		ProcessedBytes:    outcome.processedBytes,
		TotalBytes:        c.snapshot.len(),
		MatchCount:        outcome.matchCount,
		CurrentMatch:      outcome.currentMatch,
		NearbyMatches:     outcome.nearbyMatches,
		ResultRevision:    outcome.resultRevision,
		ResultByteLength:  outcome.resultByteLength,
		PersistedRevision: outcome.persistedRevision,
		Dirty:             outcome.dirty,
		OutputPath:        outcome.outputPath,
		Message:           message,
	}})
}

type matchOptions struct {
	caseSensitive bool
	wholeWord     bool
}

// kmpMatcher 是流式 KMP 匹配器：逐字节推进，O(1) 摊还。
type kmpMatcher struct {
	pattern   []byte
	failure   []int
	matched   int
	processed uint64
}

func newKMPMatcher(pattern []byte) *kmpMatcher {
	failure := make([]int, len(pattern))
	for i, k := 1, 0; i < len(pattern); i++ {
		for k > 0 && pattern[i] != pattern[k] {
			k = failure[k-1]
		}
		if pattern[i] == pattern[k] {
			k++
		}
		failure[i] = k
	}
	return &kmpMatcher{pattern: pattern, failure: failure}
}

// push 推进一个字节；完成一次匹配时返回匹配起点。
func (m *kmpMatcher) push(b byte) (uint64, bool) {
	for m.matched > 0 && m.pattern[m.matched] != b {
		m.matched = m.failure[m.matched-1]
	}
	if m.pattern[m.matched] == b {
		m.matched++
	}
	m.processed++
	if m.matched != len(m.pattern) {
		return 0, false
	}
	start := m.processed - uint64(len(m.pattern))
	m.matched = m.failure[m.matched-1]
	return start, true
}

func (m *kmpMatcher) reset() { m.matched = 0 }

// streamingMatchScanner 在 KMP 之上实现大小写不敏感（ASCII）与整词匹配：
// 整词模式下候选匹配要等到下一个字节确认右边界后才发布；匹配之间不重叠。
type streamingMatchScanner struct {
	matcher    *kmpMatcher
	patternLen int
	options    matchOptions
	history    []byte // 最近 patternLen+1 个原始字节，用于判断左边界
	pending    *TaskMatch
}

func newStreamingMatchScanner(pattern []byte, options matchOptions) *streamingMatchScanner {
	normalized := make([]byte, len(pattern))
	for i, b := range pattern {
		normalized[i] = normalizeSearchByte(b, options.caseSensitive)
	}
	return &streamingMatchScanner{
		matcher:    newKMPMatcher(normalized),
		patternLen: len(pattern),
		options:    options,
		history:    make([]byte, 0, len(pattern)+1),
	}
}

func (s *streamingMatchScanner) push(b byte) (TaskMatch, bool) {
	var resolved *TaskMatch
	if s.pending != nil {
		candidate := s.pending
		s.pending = nil
		if !isWordByte(b) {
			s.matcher.reset()
			resolved = candidate
		}
	}
	s.history = append(s.history, b)
	if len(s.history) > s.patternLen+1 {
		s.history = s.history[1:]
	}
	var immediate *TaskMatch
	if start, ok := s.matcher.push(normalizeSearchByte(b, s.options.caseSensitive)); ok {
		hasPreceding := start != 0 && len(s.history) > s.patternLen
		if !(s.options.wholeWord && hasPreceding && isWordByte(s.history[0])) {
			found := TaskMatch{StartByte: start, EndByte: start + uint64(s.patternLen)}
			if s.options.wholeWord {
				s.pending = &found
			} else {
				s.matcher.reset()
				immediate = &found
			}
		}
	}
	if resolved != nil {
		return *resolved, true
	}
	if immediate != nil {
		return *immediate, true
	}
	return TaskMatch{}, false
}

func (s *streamingMatchScanner) finish() (TaskMatch, bool) {
	if s.pending == nil {
		return TaskMatch{}, false
	}
	pending := *s.pending
	s.pending = nil
	s.matcher.reset()
	return pending, true
}

func normalizeSearchByte(b byte, caseSensitive bool) byte {
	if !caseSensitive && b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// isWordByte 把 ASCII 字母数字、下划线与所有非 ASCII 字节视为单词字符。
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b >= 0x80
}
