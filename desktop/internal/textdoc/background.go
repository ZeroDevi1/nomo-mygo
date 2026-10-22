package textdoc

import (
	"fmt"
	"time"

	"nomo/desktop/internal/applog"
)

func updateLineIndexAfterEdits(state *sessionState, delta lineIndexDelta) {
	state.indexExactRebuildRequired = true
	if state.initialIndexPending {
		if !state.initialIndexRequiresExact {
			expectedBase := delta.baseRevision
			if n := len(state.pendingIndexDeltas); n > 0 {
				expectedBase = state.pendingIndexDeltas[n-1].revision
			}
			if expectedBase == delta.baseRevision {
				state.pendingIndexDeltas = append(state.pendingIndexDeltas, delta)
			} else {
				state.initialIndexRequiresExact = true
				state.pendingIndexDeltas = nil
			}
		}
		state.lineIndex = pendingLineIndex(state.tree.len(), state.revision)
		return
	}
	next, err := state.lineIndex.applyDelta(delta)
	if err != nil {
		// delta 不变量失败时显式降级为待重建，绝不继续暴露伪精确检查点。
		applog.Warn("SegmentedDocument", "合并行索引增量失败，等待精确重建："+err.Error())
		state.lineIndex = pendingLineIndex(state.tree.len(), state.revision)
		return
	}
	state.lineIndex = next
}

func invalidateLineIndexForReplacement(state *sessionState) {
	state.initialIndexRequiresExact = state.initialIndexRequiresExact || state.initialIndexPending
	state.pendingIndexDeltas = nil
	state.indexExactRebuildRequired = true
	state.lineIndex = pendingLineIndex(state.tree.len(), state.revision)
}

// startBaselineMaterialization 在后台把源文件流式复制为私有不可变 baseline；
// 完成前会话只读，完成后再启动编码校验与行索引。复制前后身份不一致时放弃发布。
func startBaselineMaterialization(s *session, indexReady, validationReady, persistCache bool) {
	go func() {
		err := func() error {
			if s.closing.Load() {
				return nil
			}
			source := s.currentPath()
			s.baselineMu.Lock()
			expected := s.baseline
			s.baselineMu.Unlock()
			immutable, err := copyToImmutableOriginal(source, s.initialBaselineAssetPath, s.bomLen, s.baselineCopyChunkDelay)
			if err != nil {
				return err
			}
			if identity, err := readIdentity(source); err != nil || !identity.equal(expected) {
				immutable.release()
				if err != nil {
					return err
				}
				return newError("file-changed-during-baseline-copy", "后台建立不可变 baseline 时源文件发生变化")
			}
			s.lifecycleMu.Lock()
			if s.closing.Load() {
				s.lifecycleMu.Unlock()
				immutable.release()
				return nil
			}
			s.stateMu.Lock()
			previousOriginal := s.state.original
			s.state.original = immutable.retain()
			var previousInitial *snapshot
			if s.state.initialIndexPending {
				s.initialSnapMu.Lock()
				previousInitial = s.initialIndexSnapshot
				s.initialIndexSnapshot = &snapshot{revision: s.state.revision, tree: s.state.tree, original: immutable.retain(), added: s.added}
				s.initialSnapMu.Unlock()
			}
			index := s.state.lineIndex
			s.stateMu.Unlock()
			s.recoveryMu.Lock()
			previousRecovery := s.recoveryBaseline
			s.recoveryBaseline = immutable
			s.recoveryMu.Unlock()
			s.metaMu.Lock()
			s.meta.baselineReady = true
			meta := s.meta
			s.metaMu.Unlock()
			s.lifecycleMu.Unlock()
			previousOriginal.release()
			previousInitial.release()
			previousRecovery.release()

			if !validationReady {
				startEncodingValidation(s)
			}
			if !indexReady {
				startLineIndex(s, false, persistCache)
			} else if validationReady {
				// 缓存索引无需再扫描，也要通知前端 baseline 门禁已解除。
				emitCompletedIndex(s, s.sink, index, meta)
			}
			return nil
		}()
		if err == nil {
			return
		}
		message := err.Error()
		s.baseErrMu.Lock()
		s.baseErr = &message
		s.baseErrMu.Unlock()
		if s.sink != nil {
			if snap, index, snapErr := s.snapshotWithIndex(); snapErr == nil {
				id := "baseline-" + s.id
				s.sink(Event{Index: &IndexProgressEvent{
					SessionID:      s.id,
					TaskID:         id,
					RequestID:      id,
					Revision:       snap.revision,
					IndexedBytes:   index.IndexedBytes,
					TotalBytes:     snap.len(),
					EstimatedLines: index.EstimatedLines,
					Readonly:       ptr(true),
					BaselineError:  &message,
				}})
				snap.release()
			}
		}
		applog.Error("SegmentedDocument", "后台建立不可变 baseline 失败："+message)
	}()
}

// startEncodingValidation 在后台对初始快照做全文 UTF-8 校验，完成后解除编码门禁并通知前端。
func startEncodingValidation(s *session) {
	generation := s.validationGeneration.Add(1)
	s.initialSnapMu.Lock()
	var snap *snapshot
	if s.initialIndexSnapshot != nil {
		snap = s.initialIndexSnapshot.clone()
	}
	s.initialSnapMu.Unlock()
	if snap == nil {
		applog.Error("SegmentedDocument", "编码校验缺少不可变初始快照")
		return
	}
	go func() {
		defer snap.release()
		shouldCancel := func() bool {
			return s.closing.Load() || s.validationGeneration.Load() != generation
		}
		valid, done, err := validateSnapshotUTF8(snap, shouldCancel, s.validationChunkDelay)
		if err != nil {
			applog.Error("SegmentedDocument", "后台 UTF-8 校验失败："+err.Error())
			return
		}
		if !done || shouldCancel() {
			return
		}
		s.stateMu.RLock()
		if s.state.revision != snap.revision {
			s.stateMu.RUnlock()
			return
		}
		revision := s.state.revision
		index := s.state.lineIndex
		s.metaMu.Lock()
		if s.meta.validationComplete || shouldCancel() {
			s.metaMu.Unlock()
			s.stateMu.RUnlock()
			return
		}
		s.meta.validationComplete = true
		switch {
		case !valid:
			s.meta.encoding = EncodingUnsupported
		case s.bomLen == uint64(len(utf8BOM)):
			s.meta.encoding = EncodingUTF8BOM
		default:
			s.meta.encoding = EncodingUTF8
		}
		meta := s.meta
		s.metaMu.Unlock()
		s.stateMu.RUnlock()
		if s.sink != nil {
			id := fmt.Sprintf("validation-%s-%d", s.id, revision)
			s.sink(Event{Index: &IndexProgressEvent{
				SessionID:      s.id,
				TaskID:         id,
				RequestID:      id,
				Revision:       revision,
				IndexedBytes:   s.indexReportedBytes.Load(),
				TotalBytes:     index.TotalBytes,
				EstimatedLines: s.indexReportedLines.Load(),
				Completed:      index.Completed,
				Encoding:       ptr(meta.encoding),
				Readonly:       ptr(meta.readonly()),
			}})
		}
	}()
}

// validateSnapshotUTF8 分块校验整个快照；取消时 done 为 false。
func validateSnapshotUTF8(snap *snapshot, shouldCancel func() bool, delay time.Duration) (valid, done bool, err error) {
	var offset uint64
	var tail []byte
	for offset < snap.len() {
		if shouldCancel() {
			return false, false, nil
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		chunk, err := snap.readRange(offset, validationChunkBytes)
		if err != nil {
			return false, false, err
		}
		if len(chunk) == 0 {
			break
		}
		var stillValid bool
		tail, stillValid = updateUTF8Validation(tail, chunk, true)
		if !stillValid {
			return false, true, nil
		}
		offset += uint64(len(chunk))
	}
	return len(tail) == 0, true, nil
}

// startLineIndex 推进索引代数并确保至多一个索引 worker 运行。debounce 为 true 时，
// worker 在编辑静默 indexEditIdle 后才对最新快照做全文扫描，输入热路径只推进代数。
func startLineIndex(s *session, debounce, persistCache bool) {
	generation := s.indexGeneration.Add(1)
	if persistCache {
		s.indexCacheGeneration.Store(generation)
	}
	if s.sink != nil {
		if snap, index, err := s.snapshotWithIndex(); err == nil {
			if !index.Completed {
				id := fmt.Sprintf("index-%s-%d", s.id, snap.revision)
				s.sink(Event{Index: &IndexProgressEvent{
					SessionID: s.id, TaskID: id, RequestID: id, Revision: snap.revision, TotalBytes: snap.len(),
				}})
			}
			snap.release()
		}
	}
	if !s.indexWorkerRunning.CompareAndSwap(false, true) {
		return
	}
	s.indexWorkerStarts.Add(1)
	go func() {
		debounceNext := debounce
		for {
			loopGeneration := s.indexGeneration.Load()
			s.stateMu.RLock()
			initialPending := s.state.initialIndexPending
			s.stateMu.RUnlock()
			if initialPending {
				needsExact, err := buildAndCommitInitialIndex(s, s.sink, persistCache)
				if err != nil {
					s.indexWorkerRunning.Store(false)
					if !s.closing.Load() {
						applog.Error("SegmentedDocument", "构建原始分段行索引失败："+err.Error())
					}
					return
				}
				if needsExact {
					debounceNext = true
					continue
				}
			} else {
				if debounceNext {
					time.Sleep(indexEditIdle)
					if s.indexGeneration.Load() != loopGeneration {
						continue
					}
				}
				debounceNext = true
				persist := s.indexCacheGeneration.Load() == loopGeneration
				committed, err := buildAndCommitExactIndex(s, s.sink, loopGeneration, persist)
				if err != nil {
					if s.indexGeneration.Load() != loopGeneration {
						continue
					}
					s.indexWorkerRunning.Store(false)
					if !s.closing.Load() {
						applog.Error("SegmentedDocument", "构建分段行索引失败："+err.Error())
					}
					return
				}
				if !committed {
					continue
				}
			}
			s.indexWorkerRunning.Store(false)
			if s.indexGeneration.Load() == loopGeneration {
				return
			}
			// Store(false) 与新编辑调度之间存在窗口；CAS 保证至多一个 worker 接续。
			if !s.indexWorkerRunning.CompareAndSwap(false, true) {
				return
			}
		}
	}()
}

func (s *session) chunkDelayCancel(cancel func() bool) func() bool {
	return func() bool {
		if delay := s.indexChunkDelay.Load(); delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		return cancel()
	}
}

// buildAndCommitInitialIndex 对打开时的不可变快照做首次全量扫描；期间的编辑以 revision delta
// 合并，不取消这次扫描。返回是否还需要一次精确重建。
func buildAndCommitInitialIndex(s *session, sink EventSink, persistCache bool) (bool, error) {
	if s.closing.Load() {
		return false, nil
	}
	s.initialSnapMu.Lock()
	var snap *snapshot
	if s.initialIndexSnapshot != nil {
		snap = s.initialIndexSnapshot.clone()
	}
	s.initialSnapMu.Unlock()
	if snap == nil {
		return false, newError("initial-index-missing", "初始索引快照不存在")
	}
	defer snap.release()
	s.indexBuildStarts.Add(1)
	s.initialIndexBuildStarts.Add(1)
	// 原始扫描只受会话关闭影响；普通编辑通过 revision delta 合并。
	shouldCancel := s.chunkDelayCancel(func() bool { return s.closing.Load() })
	publishProgress := func(indexedBytes, estimatedLines uint64, checkpoint lineIndexBuildCheckpoint) {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		if !s.state.initialIndexPending || s.state.revision != snap.revision {
			return
		}
		// 前台补扫检查点与后台 JSON 检查点共存，只增量发布本次稀疏节点。
		s.state.lineIndex = s.state.lineIndex.withBuildProgress(indexedBytes, estimatedLines, checkpoint)
	}
	index, err := buildLineIndex(s.id, snap, trackedIndexSink(s, sink), publishProgress, shouldCancel)
	if err != nil || index == nil {
		return false, err
	}
	s.stateMu.Lock()
	if !s.state.initialIndexPending {
		needsExact := s.state.indexExactRebuildRequired
		s.stateMu.Unlock()
		return needsExact, nil
	}
	latestRevision := s.state.revision
	hadEdits := latestRevision != snap.revision
	mergeFailed := s.state.initialIndexRequiresExact
	if !mergeFailed {
		for _, delta := range s.state.pendingIndexDeltas {
			merged, err := index.applyDelta(delta)
			if err != nil {
				mergeFailed = true
				applog.Warn("SegmentedDocument", "原始索引合并编辑 delta 失败："+err.Error())
				break
			}
			index = merged
		}
		mergeFailed = mergeFailed || index.Revision != latestRevision
	}
	s.state.initialIndexPending = false
	s.state.initialIndexRequiresExact = false
	s.state.pendingIndexDeltas = nil
	s.state.indexExactRebuildRequired = hadEdits || mergeFailed
	var committed *lineIndex
	if mergeFailed {
		s.state.lineIndex = pendingLineIndex(s.state.tree.len(), latestRevision)
	} else {
		s.state.lineIndex = index
		committed = index
	}
	canPersist := persistCache && !hadEdits && !mergeFailed && s.state.revision == s.state.persistedRevision
	needsExact := s.state.indexExactRebuildRequired
	s.stateMu.Unlock()
	s.initialSnapMu.Lock()
	initial := s.initialIndexSnapshot
	s.initialIndexSnapshot = nil
	s.initialSnapMu.Unlock()
	initial.release()
	if committed != nil {
		meta := updateIndexMetadata(s, committed)
		if canPersist {
			persistLineIndex(s, committed)
		}
		emitCompletedIndex(s, sink, committed, meta)
	}
	return needsExact, nil
}

// buildAndCommitExactIndex 对最新快照做精确重建；代数变化（有新编辑）时放弃并返回 false。
func buildAndCommitExactIndex(s *session, sink EventSink, generation uint64, persistCache bool) (bool, error) {
	if s.closing.Load() {
		return true, nil
	}
	snap, err := s.snapshot()
	if err != nil {
		return false, err
	}
	defer snap.release()
	shouldCancel := s.chunkDelayCancel(func() bool { return s.indexGeneration.Load() != generation })
	s.indexBuildStarts.Add(1)
	// delta 合并后的索引仍可服务读取；精确重建不再发送从 0 开始的进度，以免 UI 回退。
	index, err := buildLineIndex(s.id, snap, nil, nil, shouldCancel)
	if err != nil || index == nil {
		return false, err
	}
	s.stateMu.Lock()
	if s.state.revision != snap.revision || shouldCancel() {
		s.stateMu.Unlock()
		return false, nil
	}
	s.state.lineIndex = index
	s.state.initialIndexPending = false
	s.state.pendingIndexDeltas = nil
	s.state.indexExactRebuildRequired = false
	canPersist := persistCache && s.state.revision == s.state.persistedRevision
	s.stateMu.Unlock()
	meta := updateIndexMetadata(s, index)
	if canPersist {
		persistLineIndex(s, index)
	}
	emitCompletedIndex(s, sink, index, meta)
	return true, nil
}

func updateIndexMetadata(s *session, index *lineIndex) sessionMetadata {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if index.LineEndingExact {
		s.meta.lineEnding = index.LineEnding
	}
	return s.meta
}

func persistLineIndex(s *session, index *lineIndex) {
	if err := index.saveCache(s.indexCachePath, s.indexCacheKey); err != nil {
		applog.Warn("SegmentedDocument", "写入行索引缓存失败："+err.Error())
	}
}

func emitCompletedIndex(s *session, sink EventSink, index *lineIndex, meta sessionMetadata) {
	s.indexReportedBytes.Store(index.IndexedBytes)
	s.indexReportedLines.Store(index.EstimatedLines)
	if sink == nil {
		return
	}
	id := fmt.Sprintf("index-%s-%d", s.id, index.Revision)
	event := &IndexProgressEvent{
		SessionID:      s.id,
		TaskID:         id,
		RequestID:      id,
		Revision:       index.Revision,
		IndexedBytes:   index.IndexedBytes,
		TotalBytes:     index.TotalBytes,
		EstimatedLines: index.EstimatedLines,
		Completed:      true,
		Encoding:       ptr(meta.encoding),
		Readonly:       ptr(meta.readonly()),
	}
	if index.LineEndingExact {
		event.LineEnding = ptr(meta.lineEnding)
	}
	sink(Event{Index: event})
}

// trackedIndexSink 在转发索引进度的同时记录最近上报值，供编码校验事件复用。
func trackedIndexSink(s *session, sink EventSink) EventSink {
	if sink == nil {
		return nil
	}
	return func(event Event) {
		if event.Index != nil {
			s.indexReportedBytes.Store(event.Index.IndexedBytes)
			s.indexReportedLines.Store(event.Index.EstimatedLines)
		}
		sink(event)
	}
}

// scheduleJournalFlush 在 1 秒后把补丁刷入恢复日志；已有排队时合并为一次。
func scheduleJournalFlush(s *session) {
	if !s.journalFlushScheduled.CompareAndSwap(false, true) {
		return
	}
	go func() {
		time.Sleep(journalFlushDelay)
		if s.closing.Load() {
			s.journalFlushScheduled.Store(false)
			return
		}
		s.saveMu.Lock()
		defer s.saveMu.Unlock()
		if _, err := flushSessionJournal(s, nil); err != nil {
			applog.Error("SegmentedDocument", "刷新恢复日志失败："+err.Error())
		}
	}()
}

// flushSessionJournal 必须在 saveMu 下调用：把预先建立的不可变 baseline 以硬链接交给日志，
// 再原子追加补丁记录。首次落盘只同步 Added 与小型日志，不在 1 秒恢复窗口内复制全文。
func flushSessionJournal(s *session, expectedRevision *uint64) (uint64, error) {
	s.stateMu.RLock()
	revision, persistedRevision := s.state.revision, s.state.persistedRevision
	s.stateMu.RUnlock()
	if expectedRevision != nil && *expectedRevision != revision {
		return 0, revisionConflict(*expectedRevision, revision)
	}
	s.journalMu.Lock()
	if !s.journal.hasRecords() {
		// 在日志锁内复位，保证随后记录的编辑一定能重新调度。
		s.journalFlushScheduled.Store(false)
		s.journalMu.Unlock()
		return revision, nil
	}
	snapshotTarget := ""
	if s.journal.needsRecoverySnapshot() {
		snapshotTarget = s.journal.recoverySnapshotPath()
	}
	s.journalMu.Unlock()
	resetSchedule := func() {
		s.journalMu.Lock()
		s.journalFlushScheduled.Store(false)
		s.journalMu.Unlock()
	}
	if err := s.added.sync(); err != nil {
		resetSchedule()
		return 0, err
	}
	var prepared *recoverySnapshot
	if snapshotTarget != "" {
		s.recoveryMu.Lock()
		baseline := s.recoveryBaseline
		var source string
		var err error
		if baseline == nil {
			err = errSessionClosing
		} else {
			source, err = baseline.immutableAssetPath()
		}
		if err == nil {
			err = publishRecoveryBaseline(source, snapshotTarget)
		}
		s.recoveryMu.Unlock()
		if err != nil {
			resetSchedule()
			return 0, err
		}
		prepared = &recoverySnapshot{Path: snapshotTarget, Revision: persistedRevision, BomLen: s.bomLen}
	}
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	if prepared != nil {
		s.journal.installRecoverySnapshot(*prepared)
	}
	err := s.journal.flush()
	s.journalFlushScheduled.Store(false)
	if err != nil {
		return 0, err
	}
	return revision, nil
}
