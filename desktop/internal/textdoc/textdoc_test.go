package textdoc

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// eventLog 收集会话事件，供测试等待任务或索引状态。
type eventLog struct {
	mu     sync.Mutex
	events []Event
	// onEvent 在记录前调用，用于在任务进行中注入操作。
	onEvent func(Event)
}

func (l *eventLog) sink(event Event) {
	if l.onEvent != nil {
		l.onEvent(event)
	}
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *eventLog) waitTask(t *testing.T, taskID string, terminal bool) TaskProgressEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		for _, event := range l.events {
			if event.Task == nil || event.Task.TaskID != taskID {
				continue
			}
			if event.Task.State == TaskCompleted || terminal && event.Task.State != TaskRunning {
				l.mu.Unlock()
				return *event.Task
			}
		}
		l.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s timed out", taskID)
	return TaskProgressEvent{}
}

func (l *eventLog) processed(taskID string, runningOnly bool) []uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var values []uint64
	for _, event := range l.events {
		if event.Task != nil && event.Task.TaskID == taskID && (!runningOnly || event.Task.State == TaskRunning) {
			values = append(values, event.Task.ProcessedBytes)
		}
	}
	return values
}

func waitStatus(t *testing.T, m *Manager, sessionID string, ready func(*SessionStatus) bool) *SessionStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := m.SessionStatus(sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if ready(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s status timed out", sessionID)
	return nil
}

func waitIndex(t *testing.T, m *Manager, sessionID string) *SessionStatus {
	return waitStatus(t, m, sessionID, func(s *SessionStatus) bool { return s.Completed })
}

// waitIndexWorkerIdle 等待会话的后台索引协程退出（含写入行索引缓存）。
func waitIndexWorkerIdle(t *testing.T, m *Manager, sessionID string) {
	t.Helper()
	s, err := m.session(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.indexWorkerRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatal("index worker did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitExactIndex(t *testing.T, m *Manager, sessionID string) {
	t.Helper()
	s, err := m.session(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.stateMu.RLock()
		exact := s.state.lineIndex.Revision == s.state.revision && s.state.lineIndex.LineEndingExact && !s.state.indexExactRebuildRequired
		s.stateMu.RUnlock()
		if exact {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("exact index timed out")
}

func mustCode(t *testing.T, err error, code string) {
	t.Helper()
	var structured *Error
	if !errors.As(err, &structured) || structured.Code != code {
		t.Fatalf("expected error code %s, got %v", code, err)
	}
}

// must 在错误时 panic（测试框架会报告失败与堆栈），以便直接包裹多返回值调用。
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLargeFixture(t *testing.T, path string, pattern []byte, target int) {
	t.Helper()
	data := bytes.Repeat(pattern, target/len(pattern)+1)[:target]
	writeFile(t, path, data)
}

func edit(sessionID string, base, from, to uint64, text string) EditBatch {
	return EditBatch{SessionID: sessionID, BaseRevision: base, Edits: []Edit{{FromByte: from, ToByte: to, InsertedText: text}}}
}

func searchTask(query string) Task { return Task{Type: TaskSearch, Query: query, CaseSensitive: true} }

func TestOpensUTF8BOMWithoutSplittingMultibyteCharacter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("\xEF\xBB\xBFa\xE4\xB8\xADb\nnext"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	window := must(m.ReadWindow(opened.SessionID, opened.Revision, 1, 2, nil))
	if opened.Encoding != EncodingUTF8BOM || opened.ByteLength != 10 {
		t.Fatalf("opened %+v", opened)
	}
	if window.StartByte != 1 || window.Text != "中" || window.EndByte != 4 {
		t.Fatalf("window %+v", window)
	}
}

func TestAppliesRevisionedEditsAndStreamSavesBOMAndCRLF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("\xEF\xBB\xBFa\r\nb"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	applied := must(m.ApplyEdits(edit(opened.SessionID, opened.Revision, 3, 4, "中\n末")))
	_, err := m.ReadWindow(opened.SessionID, opened.Revision, 0, 64, nil)
	mustCode(t, err, "revision-conflict")
	current := must(m.ReadWindow(opened.SessionID, applied.Revision, 0, 64, nil))
	if current.Text != "a\r\n中\r\n末" || !applied.Dirty {
		t.Fatalf("current %q applied %+v", current.Text, applied)
	}
	saved := must(m.SaveRevision(opened.SessionID, applied.Revision, "", false))
	if saved.Dirty {
		t.Fatal("saved should be clean")
	}
	disk, _ := os.ReadFile(path)
	if !bytes.Equal(disk, []byte("\xEF\xBB\xBFa\r\n\xE4\xB8\xAD\r\n\xE6\x9C\xAB")) {
		t.Fatalf("disk %q", disk)
	}
}

func TestStreamsSearchAcrossTaskChunksWithPaging(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	body := bytes.Repeat([]byte("a"), 64*1024-3)
	body = append(body, "needle-tail"...)
	for range 40 {
		body = append(body, "|needle"...)
	}
	writeFile(t, path, body)
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))

	run := func(task Task) TaskProgressEvent {
		log := &eventLog{}
		started := must(m.StartTask(StartTaskRequest{SessionID: opened.SessionID, BaseRevision: opened.Revision, Task: task}, log.sink))
		return log.waitTask(t, started.TaskID, false)
	}
	initial := run(searchTask("needle"))
	if initial.MatchCount != 41 || initial.CurrentMatch.StartByte != 64*1024-3 || len(initial.NearbyMatches) != 16 {
		t.Fatalf("initial %+v", initial)
	}
	anchor := initial.NearbyMatches[15].StartByte
	forwardTask := searchTask("needle")
	forwardTask.AnchorByte, forwardTask.Direction = &anchor, ptr(SearchForward)
	forward := run(forwardTask)
	if forward.MatchCount != 41 || len(forward.NearbyMatches) != 16 || forward.CurrentMatch.StartByte != forward.NearbyMatches[0].StartByte {
		t.Fatalf("forward %+v", forward)
	}
	for _, found := range forward.NearbyMatches {
		if found.StartByte <= anchor {
			t.Fatalf("forward page contains %d <= %d", found.StartByte, anchor)
		}
	}
	current := forward.CurrentMatch.StartByte
	backwardTask := searchTask("needle")
	backwardTask.AnchorByte, backwardTask.Direction = &current, ptr(SearchBackward)
	backward := run(backwardTask)
	if len(backward.NearbyMatches) != 16 || backward.CurrentMatch.StartByte != backward.NearbyMatches[15].StartByte {
		t.Fatalf("backward %+v", backward)
	}
	for _, found := range backward.NearbyMatches {
		if found.StartByte >= current {
			t.Fatalf("backward page contains %d >= %d", found.StartByte, current)
		}
	}
}

func TestSearchRequestDeserializesCamelCasePageAnchor(t *testing.T) {
	var request StartTaskRequest
	raw := `{"sessionId":"session","baseRevision":7,"task":{"type":"search","query":"needle","anchorByte":42,"direction":"backward"}}`
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	if request.Task.Type != TaskSearch || *request.Task.AnchorByte != 42 || *request.Task.Direction != SearchBackward {
		t.Fatalf("request %+v", request)
	}
}

func TestReportsCleanDirtyModifiedAndDeletedExternalState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("alpha"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	clean := must(m.CheckExternalChange(opened.SessionID))
	if clean.Change != ExternalNone || clean.Dirty || clean.SaveInProgress {
		t.Fatalf("clean %+v", clean)
	}
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	must(m.ApplyEdits(edit(opened.SessionID, opened.Revision, 5, 5, " local")))
	writeFile(t, path, []byte("external replacement"))
	modified := must(m.CheckExternalChange(opened.SessionID))
	if modified.Change != ExternalModified || !modified.Dirty {
		t.Fatalf("modified %+v", modified)
	}
	os.Remove(path)
	deleted := must(m.CheckExternalChange(opened.SessionID))
	if deleted.Change != ExternalDeleted {
		t.Fatalf("deleted %+v", deleted)
	}
}

func TestDetectsSameSizeSameMtimeAtomicReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	replacement := filepath.Join(root, "replacement.txt")
	writeFile(t, path, []byte("AAAA"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	clean := must(m.CheckExternalChange(opened.SessionID))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	edited := must(m.ApplyEdits(edit(opened.SessionID, opened.Revision, 4, 4, " local")))
	info, _ := os.Stat(path)
	writeFile(t, replacement, []byte("BBBB"))
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	changed := must(m.CheckExternalChange(opened.SessionID))
	if changed.Change != ExternalModified || changed.ChangeToken == clean.ChangeToken {
		t.Fatalf("changed %+v", changed)
	}
	_, err := m.SaveRevision(opened.SessionID, edited.Revision, "", false)
	mustCode(t, err, "external-file-changed")
	disk, _ := os.ReadFile(path)
	if string(disk) != "BBBB" {
		t.Fatalf("disk %q", disk)
	}
}

func TestKeepsNewerRevisionDirtyWhenEditingDuringSave(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	state := filepath.Join(root, "state")
	writeFile(t, path, []byte("base"))
	m := NewManager(state)
	opened := must(m.OpenDocument(path, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	one := must(m.ApplyEdits(edit(opened.SessionID, 0, 4, 4, " one")))
	pause := newTestPause()
	m.hooks.savePause.Store(pause)
	type saveOutcome struct {
		result *SaveResult
		err    error
	}
	done := make(chan saveOutcome)
	go func() {
		result, err := m.SaveRevision(opened.SessionID, one.Revision, "", false)
		done <- saveOutcome{result, err}
	}()
	<-pause.entered
	saving := must(m.CheckExternalChange(opened.SessionID))
	if !saving.SaveInProgress || !saving.Dirty {
		t.Fatalf("saving %+v", saving)
	}
	two := must(m.ApplyEdits(edit(opened.SessionID, one.Revision, 8, 8, " two")))
	close(pause.released)
	outcome := <-done
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	saved := outcome.result
	if saved.SavedRevision != one.Revision || saved.CurrentRevision != two.Revision || saved.PersistedRevision != one.Revision || !saved.Dirty {
		t.Fatalf("saved %+v", saved)
	}
	if disk, _ := os.ReadFile(path); string(disk) != "base one" {
		t.Fatalf("disk %q", disk)
	}
	if window := must(m.ReadWindow(opened.SessionID, two.Revision, 0, 64, nil)); window.Text != "base one two" {
		t.Fatalf("window %q", window.Text)
	}
	if err := m.CloseSession(opened.SessionID, false); err != nil {
		t.Fatal(err)
	}
	recovered := must(NewManager(state).OpenDocument(path, nil))
	if recovered.FirstWindow.Text != "base one two" {
		t.Fatalf("recovered %q", recovered.FirstWindow.Text)
	}
}

func TestRelocatesRecoveryJournalAfterSaveAs(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.txt")
	savedAs := filepath.Join(root, "saved-as.txt")
	state := filepath.Join(root, "state")
	writeFile(t, original, []byte("base"))
	m := NewManager(state)
	opened := must(m.OpenDocument(original, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	first := must(m.ApplyEdits(edit(opened.SessionID, 0, 4, 4, " one")))
	must(m.SaveRevision(opened.SessionID, first.Revision, savedAs, false))
	writeFile(t, original, []byte("evil"))
	second := must(m.ApplyEdits(edit(opened.SessionID, first.Revision, 8, 8, " two")))
	must(m.FlushJournal(opened.SessionID, second.Revision))
	if err := m.CloseSession(opened.SessionID, false); err != nil {
		t.Fatal(err)
	}
	recovered := must(NewManager(state).OpenDocument(savedAs, nil))
	if recovered.FirstWindow.Text != "base one two" {
		t.Fatalf("recovered %q", recovered.FirstWindow.Text)
	}
}

func TestRecoversFlushedEditsAndDiscardRemovesRecovery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	state := filepath.Join(root, "state")
	writeFile(t, path, []byte("base"))
	m := NewManager(state)
	opened := must(m.OpenDocument(path, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	applied := must(m.ApplyEdits(edit(opened.SessionID, opened.Revision, 4, 4, " local")))
	must(m.FlushJournal(opened.SessionID, applied.Revision))
	if err := m.CloseSession(opened.SessionID, false); err != nil {
		t.Fatal(err)
	}
	recoveredManager := NewManager(state)
	recovered := must(recoveredManager.OpenDocument(path, nil))
	window := must(recoveredManager.ReadWindow(recovered.SessionID, recovered.Revision, 0, 64, nil))
	if window.Text != "base local" || recovered.Revision != 1 {
		t.Fatalf("recovered %q rev %d", window.Text, recovered.Revision)
	}
	if err := recoveredManager.CloseSession(recovered.SessionID, true); err != nil {
		t.Fatal(err)
	}
	clean := must(NewManager(state).OpenDocument(path, nil))
	if clean.FirstWindow.Text != "base" {
		t.Fatalf("clean %q", clean.FirstWindow.Text)
	}
}

func TestReplaceAllCommitsOneRevisionAndUndoRedo(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("foo xx foo"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	log := &eventLog{}
	task := Task{Type: TaskReplaceAll, Query: "foo", Replacement: "bar", CaseSensitive: true}
	started := must(m.StartTask(StartTaskRequest{SessionID: opened.SessionID, BaseRevision: opened.Revision, Task: task}, log.sink))
	replaced := log.waitTask(t, started.TaskID, false)
	if replaced.MatchCount != 2 || *replaced.ResultRevision != 1 {
		t.Fatalf("replaced %+v", replaced)
	}
	if window := must(m.ReadWindow(opened.SessionID, 1, 0, 64, nil)); window.Text != "bar xx bar" {
		t.Fatalf("window %q", window.Text)
	}
	undone := must(m.UndoRevision(opened.SessionID, 1))
	if window := must(m.ReadWindow(opened.SessionID, undone.Revision, 0, 64, nil)); window.Text != "foo xx foo" || !undone.CanRedo {
		t.Fatalf("undone %q %+v", window.Text, undone)
	}
	redone := must(m.RedoRevision(opened.SessionID, undone.Revision))
	if window := must(m.ReadWindow(opened.SessionID, redone.Revision, 0, 64, nil)); window.Text != "bar xx bar" || !redone.CanUndo {
		t.Fatalf("redone %q %+v", window.Text, redone)
	}
}

func TestJSONFormatStreamsAndPreservesCRLF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.json")
	writeFile(t, path, []byte("{\r\n\"items\":[1,2],\"s\":\"a, b\"\r\n}"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	if opened.LineEnding != LineEndingCRLF {
		t.Fatalf("line ending %s", opened.LineEnding)
	}
	log := &eventLog{}
	started := must(m.StartTask(StartTaskRequest{SessionID: opened.SessionID, BaseRevision: opened.Revision, Task: Task{Type: TaskJSONFormat}}, log.sink))
	formatted := log.waitTask(t, started.TaskID, false)
	window := must(m.ReadWindow(opened.SessionID, *formatted.ResultRevision, 0, 1024, nil))
	want := "{\r\n  \"items\": [\r\n    1,\r\n    2\r\n  ],\r\n  \"s\": \"a, b\"\r\n}"
	if window.Text != want {
		t.Fatalf("formatted %q", window.Text)
	}
	var value any
	if err := json.Unmarshal([]byte(window.Text), &value); err != nil {
		t.Fatal(err)
	}
}

func TestJSONValidateReportsInvalidAndStreamingProgress(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.json")
	var body bytes.Buffer
	body.WriteString("[")
	for i := 0; body.Len() < 512*1024; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString(`{"name":"value","ok":true}`)
	}
	body.WriteString("]")
	writeFile(t, path, body.Bytes())
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	waitIndex(t, m, opened.SessionID)
	log := &eventLog{}
	started := must(m.StartTask(StartTaskRequest{SessionID: opened.SessionID, BaseRevision: opened.Revision, Task: Task{Type: TaskJSONValidate}}, log.sink))
	validated := log.waitTask(t, started.TaskID, false)
	if validated.ProcessedBytes != opened.ByteLength {
		t.Fatalf("validated %+v", validated)
	}
	progress := log.processed(started.TaskID, false)
	for i := 1; i < len(progress); i++ {
		if progress[i] < progress[i-1] {
			t.Fatalf("progress regressed %v", progress)
		}
	}
	streaming := false
	for _, value := range log.processed(started.TaskID, true) {
		if value > 0 && value < opened.ByteLength {
			streaming = true
		}
	}
	if !streaming {
		t.Fatal("validation must report streaming progress")
	}

	invalidPath := filepath.Join(root, "invalid.json")
	writeFile(t, invalidPath, []byte(`{"a":1} trailing`))
	invalid := must(m.OpenDocument(invalidPath, nil))
	waitIndex(t, m, invalid.SessionID)
	invalidLog := &eventLog{}
	invalidTask := must(m.StartTask(StartTaskRequest{SessionID: invalid.SessionID, BaseRevision: invalid.Revision, Task: Task{Type: TaskJSONValidate}}, invalidLog.sink))
	failed := invalidLog.waitTask(t, invalidTask.TaskID, true)
	if failed.State != TaskFailed || !strings.Contains(*failed.Message, "JSON 尾部数据无效") {
		t.Fatalf("failed %+v", failed)
	}
}

func TestCopyFallbackKeepsLatestAndPreservesOtherTaskAssets(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	tasks := filepath.Join(state, "tasks")
	writeFile(t, filepath.Join(tasks, "stale.txt"), []byte("stale"))
	assets := []string{"replace.replace", "undo.undo", "format.json"}
	for _, asset := range assets {
		writeFile(t, filepath.Join(tasks, asset), []byte(asset))
	}
	m := NewManager(state)
	if _, err := os.Stat(filepath.Join(tasks, "stale.txt")); err == nil {
		t.Fatal("stale fallback must be removed at startup")
	}
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("first"))
	opened := must(m.OpenDocument(path, nil))
	waitIndex(t, m, opened.SessionID)
	m.hooks.forceCopyFallback.Store(true)
	copyTask := func(revision uint64) TaskProgressEvent {
		log := &eventLog{}
		started := must(m.StartTask(StartTaskRequest{SessionID: opened.SessionID, BaseRevision: revision, Task: Task{Type: TaskSelectAllCopy}}, log.sink))
		return log.waitTask(t, started.TaskID, false)
	}
	first := copyTask(opened.Revision)
	if content, _ := os.ReadFile(*first.OutputPath); string(content) != "first" || !strings.Contains(*first.Message, *first.OutputPath) {
		t.Fatalf("first %+v", first)
	}
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	edited := must(m.ApplyEdits(edit(opened.SessionID, opened.Revision, opened.ByteLength, opened.ByteLength, " second")))
	second := copyTask(edited.Revision)
	if *second.OutputPath == *first.OutputPath {
		t.Fatal("fallback paths must differ")
	}
	if _, err := os.Stat(*first.OutputPath); err == nil {
		t.Fatal("older fallback must be removed")
	}
	if content, _ := os.ReadFile(*second.OutputPath); string(content) != "first second" {
		t.Fatalf("second %q", content)
	}
	for _, asset := range assets {
		if _, err := os.Stat(filepath.Join(tasks, asset)); err != nil {
			t.Fatalf("must preserve %s", asset)
		}
	}
}

func TestRejectsUTF8AndCRLFSplitsAndEditsAcrossPieces(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("a中\r\nb"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	_, err := m.ApplyEdits(edit(opened.SessionID, 0, 2, 2, "x"))
	mustCode(t, err, "utf8-boundary-split")
	_, err = m.ApplyEdits(edit(opened.SessionID, 0, 5, 5, "x"))
	mustCode(t, err, "crlf-boundary-split")
	first := must(m.ApplyEdits(edit(opened.SessionID, 0, 1, 4, "文")))
	second := must(m.ApplyEdits(edit(opened.SessionID, first.Revision, 0, 6, "X")))
	if window := must(m.ReadWindow(opened.SessionID, second.Revision, 0, 64, nil)); window.Text != "Xb" {
		t.Fatalf("window %q", window.Text)
	}
}

func TestOpensInvalidUTF8ReadonlyAndPreservesDirtyOnReplaceFailure(t *testing.T) {
	root := t.TempDir()
	invalidPath := filepath.Join(root, "invalid.txt")
	writeFile(t, invalidPath, []byte{0xff, 0xfe})
	m := NewManager(filepath.Join(root, "state"))
	invalid := must(m.OpenDocument(invalidPath, nil))
	if !invalid.Readonly {
		t.Fatal("invalid UTF-8 must be readonly")
	}
	_, err := m.ApplyEdits(edit(invalid.SessionID, invalid.Revision, 0, 0, "x"))
	mustCode(t, err, "document-readonly")

	path := filepath.Join(root, "valid.txt")
	writeFile(t, path, []byte("base"))
	valid := must(m.OpenDocument(path, nil))
	waitStatus(t, m, valid.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	applied := must(m.ApplyEdits(edit(valid.SessionID, valid.Revision, 4, 4, " dirty")))
	target := filepath.Join(root, "non-empty-directory.txt")
	writeFile(t, filepath.Join(target, "child"), []byte("keep"))
	_, err = m.SaveRevision(valid.SessionID, applied.Revision, target, false)
	mustCode(t, err, "save-replace-failed")
	if status := must(m.CheckExternalChange(valid.SessionID)); !status.Dirty {
		t.Fatal("must stay dirty")
	}
}

func TestPreservesDirtyStateAndRecoveryAfterSaveWriteFailure(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("base"))
	m := NewManager(state)
	opened := must(m.OpenDocument(path, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	edited := must(m.ApplyEdits(edit(opened.SessionID, opened.Revision, 4, 4, " dirty")))
	m.hooks.saveWriteFailAfter.Store(2)
	_, err := m.SaveRevision(opened.SessionID, edited.Revision, "", false)
	mustCode(t, err, "save-write-failed")
	if disk, _ := os.ReadFile(path); string(disk) != "base" {
		t.Fatalf("disk %q", disk)
	}
	if window := must(m.ReadWindow(opened.SessionID, edited.Revision, 0, 64, nil)); window.Text != "base dirty" {
		t.Fatalf("window %q", window.Text)
	}
	// 保存在临时写入前已刷新 recovery；模拟进程重启后仍能重放未保存的 revision。
	recovered := must(NewManager(state).OpenDocument(path, nil))
	if recovered.Revision != edited.Revision || recovered.FirstWindow.Text != "base dirty" {
		t.Fatalf("recovered %+v", recovered)
	}
}

func TestCancelsReadTaskAndWriteTaskConflictsWithNewRevision(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	writeLargeFixture(t, path, []byte("foo-"), 2*1024*1024)
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	cancelLog := &eventLog{}
	cancelLog.onEvent = func(event Event) {
		if event.Task != nil && event.Task.ProcessedBytes > 0 {
			m.CancelTask(event.Task.TaskID)
		}
	}
	started := must(m.StartTask(StartTaskRequest{SessionID: opened.SessionID, Task: searchTask("missing")}, cancelLog.sink))
	if cancelled := cancelLog.waitTask(t, started.TaskID, true); cancelled.State != TaskCancelled {
		t.Fatalf("state %s", cancelled.State)
	}
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	var edited atomic.Bool
	conflictLog := &eventLog{}
	conflictLog.onEvent = func(event Event) {
		if event.Task != nil && event.Task.ProcessedBytes > 0 && !edited.Swap(true) {
			if _, err := m.ApplyEdits(edit(opened.SessionID, 0, 0, 0, "local-")); err != nil {
				t.Error(err)
			}
		}
	}
	replace := must(m.StartTask(StartTaskRequest{SessionID: opened.SessionID, Task: Task{Type: TaskReplaceAll, Query: "foo", Replacement: "bar", CaseSensitive: true}}, conflictLog.sink))
	if conflict := conflictLog.waitTask(t, replace.TaskID, true); conflict.State != TaskConflict {
		t.Fatalf("state %s %v", conflict.State, conflict.Message)
	}
}

func TestValidatesTailEncodingBeforeEnablingWrites(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "late-invalid.txt")
	data := append(bytes.Repeat([]byte("a"), 300*1024), 0xff)
	writeFile(t, path, data)
	m := NewManager(filepath.Join(root, "state"))
	m.hooks.validationDelayMs.Store(100)
	opened := must(m.OpenDocument(path, nil))
	if !opened.Readonly {
		t.Fatal("must stay readonly until validation finishes")
	}
	pending := must(m.ReadWindow(opened.SessionID, opened.Revision, 300*1024, 8, nil))
	if pending.Text != "�" || !reflect.DeepEqual(pending.UTF16ByteOffsets, []uint32{0, 1}) {
		t.Fatalf("pending %+v", pending)
	}
	status := waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return s.Encoding == EncodingUnsupported })
	if !status.Readonly {
		t.Fatal("unsupported encoding must be readonly")
	}
	_, err := m.ApplyEdits(edit(opened.SessionID, opened.Revision, 0, 0, "unsafe"))
	mustCode(t, err, "document-readonly")
}

func TestMarksTailWindowIntersectingLongLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "long-line.txt")
	writeFile(t, path, append(bytes.Repeat([]byte("a"), 150*1024), "\nshort\n"...))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	if window := must(m.ReadWindow(opened.SessionID, opened.Revision, 140*1024, 4*1024, nil)); !window.LongLine {
		t.Fatal("tail block of long line must report longLine")
	}
	// 后台索引完成后会把行索引缓存写入 state/indexes；等它结束，避免与临时目录清理竞争。
	waitIndexWorkerIdle(t, m, opened.SessionID)
}

func TestReturnsJSONLexicalStateForWindowInsideLongString(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "long.json")
	data := append([]byte(`{"value":"`), bytes.Repeat([]byte("a"), 1200*1024)...)
	data = append(data, `","tail":true}`...)
	writeFile(t, path, data)
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	waitExactIndex(t, m, opened.SessionID)
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	start := uint64(1024*1024 + 73)
	window := must(m.ReadWindow(opened.SessionID, opened.Revision, start, 64, nil))
	if window.JSONLexicalState == nil || window.JSONLexicalState.Mode != JSONModeString || window.JSONLexicalState.Escaped {
		t.Fatalf("lexical %+v", window.JSONLexicalState)
	}
	edited := must(m.ApplyEdits(edit(opened.SessionID, opened.Revision, 0, 0, "\n")))
	immediate := must(m.ReadWindow(opened.SessionID, edited.Revision, start+1, 64, nil))
	if immediate.JSONLexicalState != nil {
		t.Fatal("remote JSON state after invalidation must be unknown")
	}
}

func TestReloadAtomicallyReplacesTheOldSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("before"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	writeFile(t, path, []byte("after reload"))
	reloaded := must(m.ReloadSession(opened.SessionID, nil))
	if reloaded.SessionID == opened.SessionID || reloaded.FirstWindow.Text != "after reload" {
		t.Fatalf("reloaded %+v", reloaded)
	}
	_, err := m.SessionStatus(opened.SessionID)
	mustCode(t, err, "session-not-found")
	_, err = m.OpenDocument(path, nil)
	mustCode(t, err, "session-already-open")
	if err := m.CloseSession(reloaded.SessionID, false); err != nil {
		t.Fatal(err)
	}
	reopened := must(m.OpenDocument(path, nil))
	if reopened.FirstWindow.Text != "after reload" {
		t.Fatalf("reopened %q", reopened.FirstWindow.Text)
	}
}

func TestMaterializesRecoveryWhenSourceWasDeleted(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	state := filepath.Join(root, "state")
	writeFile(t, path, []byte("base"))
	m := NewManager(state)
	opened := must(m.OpenDocument(path, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	applied := must(m.ApplyEdits(edit(opened.SessionID, 0, 4, 4, " unsaved")))
	must(m.FlushJournal(opened.SessionID, applied.Revision))
	if err := m.CloseSession(opened.SessionID, false); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	_, err := NewManager(state).OpenDocument(path, nil)
	var structured *Error
	if !errors.As(err, &structured) || structured.Code != "source-missing-recovered" || structured.RecoveryPath == nil {
		t.Fatalf("err %v", err)
	}
	if content, _ := os.ReadFile(*structured.RecoveryPath); string(content) != "base unsaved" {
		t.Fatalf("recovered %q", content)
	}
}

func TestSurfacesBaselineMismatchAsReadableRecoveryConflict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	state := filepath.Join(root, "state")
	writeFile(t, path, []byte("base"))
	m := NewManager(state)
	opened := must(m.OpenDocument(path, nil))
	waitStatus(t, m, opened.SessionID, func(s *SessionStatus) bool { return !s.Readonly })
	applied := must(m.ApplyEdits(edit(opened.SessionID, 0, 4, 4, " local")))
	must(m.FlushJournal(opened.SessionID, applied.Revision))
	if err := m.CloseSession(opened.SessionID, false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("externally changed"))
	reopened := must(NewManager(state).OpenDocument(path, nil))
	if reopened.RecoveryConflictPath == nil || reopened.FirstWindow.Text != "externally changed" {
		t.Fatalf("reopened %+v", reopened)
	}
	if content, _ := os.ReadFile(*reopened.RecoveryConflictPath); string(content) != "base local" {
		t.Fatalf("conflict copy %q", content)
	}
}

func TestMapsLossyUTF16BoundariesToOriginalWindowBytes(t *testing.T) {
	text, offsets := decodeLossyWithUTF16Offsets([]byte("a\xff\xf0\x9f\x98\x80b"))
	if text != "a�\U0001F600b" || !reflect.DeepEqual(offsets, []uint32{0, 1, 2, 2, 6, 7}) {
		t.Fatalf("text %q offsets %v", text, offsets)
	}
}

func TestCountsCROnlyAndLoneCRLinesConsistently(t *testing.T) {
	if countLineBreaks([]byte("a\rb\rc")) != 2 || countLineBreaks([]byte("a\r\nb\rc\n")) != 3 {
		t.Fatal("line break counts")
	}
	cases := map[string]LineEnding{"a\rb\r": LineEndingMixed, "a\r\nb\r\n": LineEndingCRLF, "a\r\nb\r": LineEndingMixed, "a\nb": LineEndingLF}
	for input, want := range cases {
		if got := detectLineEnding([]byte(input)); got != want {
			t.Fatalf("%q: %s want %s", input, got, want)
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "cr.txt")
	writeFile(t, path, []byte("one\rtwo\rthree"))
	m := NewManager(filepath.Join(root, "state"))
	opened := must(m.OpenDocument(path, nil))
	status := waitIndex(t, m, opened.SessionID)
	if status.EstimatedLines != 2 || status.LineEnding != LineEndingMixed {
		t.Fatalf("status %+v", status)
	}
	window := must(m.ReadWindow(opened.SessionID, opened.Revision, 8, 64, nil))
	if window.StartLine != 2 || window.Text != "three" {
		t.Fatalf("window %+v", window)
	}
}

func TestKMPMatcherPreservesOverlappingMatchesAcrossStream(t *testing.T) {
	matcher := newKMPMatcher([]byte("aba"))
	var matches []uint64
	for _, b := range []byte("ababa") {
		if start, ok := matcher.push(b); ok {
			matches = append(matches, start)
		}
	}
	if !reflect.DeepEqual(matches, []uint64{0, 2}) {
		t.Fatalf("matches %v", matches)
	}
}

func TestStreamingScannerHonoursWholeWordAndCaseInsensitivity(t *testing.T) {
	scan := func(text, query string, options matchOptions) []uint64 {
		scanner := newStreamingMatchScanner([]byte(query), options)
		var starts []uint64
		for _, b := range []byte(text) {
			if found, ok := scanner.push(b); ok {
				starts = append(starts, found.StartByte)
			}
		}
		if found, ok := scanner.finish(); ok {
			starts = append(starts, found.StartByte)
		}
		return starts
	}
	if got := scan("Foo foobar foo", "foo", matchOptions{wholeWord: true}); !reflect.DeepEqual(got, []uint64{0, 11}) {
		t.Fatalf("whole word %v", got)
	}
	if got := scan("aaaa", "aa", matchOptions{caseSensitive: true}); !reflect.DeepEqual(got, []uint64{0, 2}) {
		t.Fatalf("non-overlapping %v", got)
	}
}

func TestPieceTreeSplitsMergesAndTracksLineBreaks(t *testing.T) {
	tree := treeFromOriginal(10)
	if tree.aggregateLineBreaks() != nil {
		t.Fatal("original line breaks are unknown")
	}
	inserted := addedPiece(0, 3, 1)
	next := must(tree.replaceRange(2, 5, &inserted))
	if next.len() != 10 {
		t.Fatalf("len %d", next.len())
	}
	segments := must(next.segments(0, next.len()))
	var shape []string
	for _, seg := range segments {
		shape = append(shape, seg.piece.String())
	}
	if !reflect.DeepEqual(shape, []string{"original[0+2]", "added[0+3]", "original[5+5]"}) {
		t.Fatalf("shape %v", shape)
	}
	if tree.len() != 10 {
		t.Fatal("persistent tree must be unchanged")
	}
	whole := treeFromOriginal(0).replaceAllWithAdded(0, 3, 2)
	if lines := whole.aggregateLineBreaks(); lines == nil || *lines != 2 {
		t.Fatal("added pieces carry exact line breaks")
	}
	if _, err := next.replaceRange(4, 11, nil); err == nil {
		t.Fatal("out-of-range edit must fail")
	}
}

func TestJournalRoundTripsChecksumsAndRejectsTampering(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.journal")
	baseline := baselineIdentity{Path: "/tmp/doc.txt", PhysicalLen: 4, ModifiedNanos: 1, FileID: ptr("unix:1:2")}
	journal := emptyJournal(path, baseline)
	journal.record(journalRecord{BaseRevision: 0, Revision: 1, Edits: []Edit{{FromByte: 4, ToByte: 4, InsertedText: "<\"\n中>"}}})
	if err := journal.flush(); err != nil {
		t.Fatal(err)
	}
	journal.record(journalRecord{BaseRevision: 1, Revision: 2, Edits: []Edit{}, ReplacementFile: &journalReplacementFile{Path: "/tmp/r", LineBreaks: 3}})
	if err := journal.flush(); err != nil {
		t.Fatal(err)
	}
	_, persisted, recovered, conflict, err := openJournal(path, baseline)
	if err != nil || conflict != nil || persisted != 0 || len(recovered) != 2 || recovered[0].Edits[0].InsertedText != "<\"\n中>" {
		t.Fatalf("recovered %+v conflict %v err %v", recovered, conflict, err)
	}
	// 截断的最后一行会被忽略，已校验前缀仍可恢复。
	content, _ := os.ReadFile(path)
	writeFile(t, path, append(content, `{"checksum":"`...))
	if _, _, recovered, _, err := openJournal(path, baseline); err != nil || len(recovered) != 2 {
		t.Fatalf("truncated tail: %v %d", err, len(recovered))
	}
	tampered := bytes.Replace(content, []byte(`"revision":2`), []byte(`"revision":3`), 1)
	writeFile(t, path, tampered)
	_, _, _, _, err = openJournal(path, baseline)
	mustCode(t, err, "journal-checksum-failed")
	writeFile(t, path, content)
	other := baseline
	other.ModifiedNanos = 2
	if _, _, _, conflict, err := openJournal(path, other); err != nil || conflict == nil || len(conflict.records) != 2 {
		t.Fatalf("baseline mismatch must surface conflict: %v %v", conflict, err)
	}
}

func TestJournalReadsRustWrittenLine(t *testing.T) {
	// 由 Tauri 版本写出的日志头部：serde 内部标签枚举按 type → 声明顺序输出字段。
	payload := `{"type":"header","version":1,"baseline":{"path":"/a.txt","physicalLen":4,"modifiedNanos":7},"persisted_revision":0}`
	line := `{"checksum":"` + checksum([]byte(payload)) + `","payload":` + payload + "}\n"
	path := filepath.Join(t.TempDir(), "rust.journal")
	writeFile(t, path, []byte(line))
	contents, err := readJournal(path)
	if err != nil || contents.baseline.Path != "/a.txt" || contents.baseline.ModifiedNanos != 7 {
		t.Fatalf("contents %+v err %v", contents, err)
	}
}

func TestStartupRemovesUnpublishedSessionBaselineAssets(t *testing.T) {
	state := t.TempDir()
	stale := filepath.Join(state, "sessions", "seg-1.baseline")
	writeFile(t, stale, []byte("stale"))
	NewManager(state)
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("stale baseline must be removed")
	}
}

func TestCloseReleasesBaselineAssets(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	path := filepath.Join(root, "sample.txt")
	writeFile(t, path, []byte("base"))
	m := NewManager(state)
	opened := must(m.OpenDocument(path, nil))
	waitIndex(t, m, opened.SessionID)
	if err := m.CloseSession(opened.SessionID, false); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(state, "sessions"))
	if len(entries) != 0 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("session assets left behind: %v", names)
	}
}
