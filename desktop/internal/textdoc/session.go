package textdoc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"nomo/desktop/internal/applog"
)

const (
	firstWindowBytes         = 256 * 1024
	openProbeBytes           = firstWindowBytes + 4
	maxWindowBytes           = 1024 * 1024
	maxEditTransactionBytes  = 8 * 1024 * 1024
	indexEditIdle            = 500 * time.Millisecond
	validationChunkBytes     = 256 * 1024
	journalFlushDelay        = time.Second
	taskSnapshotIndexTimeout = 2 * time.Second
	// longLineThresholdBytes 与前端 lexer 的 100_000 字节阈值是同一事实源，避免边界区间模式不一致。
	longLineThresholdBytes = 100_000
)

var nextSessionSequence atomic.Uint64

// sessionMetadata 是会话的编码、换行与编辑门禁；由 metaMu 保护。
type sessionMetadata struct {
	encoding           TextEncoding
	lineEnding         LineEnding
	filesystemReadonly bool
	baselineReady      bool
	validationComplete bool
}

// readonly 汇总所有编辑门禁：权限只读、baseline 未就绪、编码校验未完成或编码不支持。
func (m sessionMetadata) readonly() bool {
	return m.filesystemReadonly || !m.baselineReady || !m.validationComplete || m.encoding == EncodingUnsupported
}

// sessionState 是会话的可变正文状态，由 stateMu 保护。
type sessionState struct {
	revision          uint64
	persistedRevision uint64
	tree              pieceTree
	// original 持有一个引用；会话释放资源后为 nil。
	original                  *originalFile
	lineIndex                 *lineIndex
	initialIndexPending       bool
	initialIndexRequiresExact bool
	pendingIndexDeltas        []lineIndexDelta
	indexExactRebuildRequired bool
	history                   historyState
}

// session 是一个打开的分段文档。锁序固定为 saveMu → lifecycleMu → journalMu → stateMu，
// lineScanMu → stateMu；registry 锁只在最内层短暂持有。
type session struct {
	id         string
	pathMu     sync.Mutex
	path       string
	kind       DocumentKind
	bomLen     uint64
	metaMu     sync.Mutex
	meta       sessionMetadata
	baseErrMu  sync.Mutex
	baseErr    *string
	added      *addedStore
	stateMu    sync.RWMutex
	state      sessionState
	baselineMu sync.Mutex
	baseline   baselineIdentity
	// recoveryBaseline 始终对应 persistedRevision；恢复日志只引用它，不依赖可能被 replace-all 改写的当前 original。
	recoveryMu               sync.Mutex
	recoveryBaseline         *originalFile
	initialBaselineAssetPath string
	journalMu                sync.Mutex
	journal                  *editJournal
	journalFlushScheduled    atomic.Bool
	saveMu                   sync.Mutex
	saveInProgress           atomic.Bool
	cache                    *chunkCache
	// lineScanMu 让 pending 索引下的远端行号补扫在单会话内串行，并回写安全检查点，避免快速滚动叠加 O(offset)。
	lineScanMu     sync.Mutex
	indexCachePath string
	indexCacheKey  string
	sink           EventSink
	// initialIndexSnapshot 是首次全量扫描与编码校验使用的不可变快照，扫描完成后释放。
	initialSnapMu        sync.Mutex
	initialIndexSnapshot *snapshot
	validationGeneration atomic.Uint64
	indexGeneration      atomic.Uint64
	indexWorkerRunning   atomic.Bool
	indexCacheGeneration atomic.Uint64
	indexReportedBytes   atomic.Uint64
	indexReportedLines   atomic.Uint64
	closing              atomic.Bool
	lifecycleMu          sync.Mutex
	// 以下字段仅供测试观测与注入延迟。
	indexWorkerStarts       atomic.Uint64
	indexBuildStarts        atomic.Uint64
	initialIndexBuildStarts atomic.Uint64
	indexChunkDelay         atomic.Int64
	validationChunkDelay    time.Duration
	baselineCopyChunkDelay  time.Duration
}

func (s *session) currentPath() string {
	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	return s.path
}

func (s *session) metadata() sessionMetadata {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	return s.meta
}

var errSessionClosing = newError("session-closing", "分段文档会话正在关闭")

func (s *session) ensureActive() error {
	if s.closing.Load() {
		return errSessionClosing
	}
	return nil
}

func (s *session) ensureWritable() (sessionMetadata, error) {
	if err := s.ensureActive(); err != nil {
		return sessionMetadata{}, err
	}
	meta := s.metadata()
	if meta.readonly() {
		return sessionMetadata{}, newError("document-readonly", "当前编码或磁盘权限只允许只读打开")
	}
	return meta, nil
}

// snapshotLocked 在已持有 stateMu（读或写）时构造快照，额外持有一个 original 引用。
func (s *session) snapshotLocked() (*snapshot, error) {
	if s.state.original == nil {
		return nil, errSessionClosing
	}
	return &snapshot{revision: s.state.revision, tree: s.state.tree, original: s.state.original.retain(), added: s.added}, nil
}

func (s *session) snapshot() (*snapshot, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.snapshotLocked()
}

func (s *session) snapshotWithIndex() (*snapshot, *lineIndex, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	snap, err := s.snapshotLocked()
	return snap, s.state.lineIndex, err
}

// releaseResources 释放会话持有的全部 original 引用；之后 snapshot() 返回 session-closing。
// 仍在运行的后台任务各自持有快照引用，结束后由它们完成最终清理。
func (s *session) releaseResources() {
	s.stateMu.Lock()
	original := s.state.original
	s.state.original = nil
	s.stateMu.Unlock()
	original.release()
	s.recoveryMu.Lock()
	recovery := s.recoveryBaseline
	s.recoveryBaseline = nil
	s.recoveryMu.Unlock()
	recovery.release()
	s.initialSnapMu.Lock()
	initial := s.initialIndexSnapshot
	s.initialIndexSnapshot = nil
	s.initialSnapMu.Unlock()
	initial.release()
}

// Manager 是分段文档的唯一公共入口：会话注册表、后台任务表与状态根目录。
// 创建一次，进程内共享，方法并发安全。
type Manager struct {
	root        string
	registryMu  sync.Mutex
	byID        map[string]*session
	byPath      map[string]string
	tasksMu     sync.Mutex
	tasks       map[string]*taskControl
	copyFallbMu sync.Mutex
	hooks       testHooks
}

// testHooks 是测试注入点；生产代码中全部为零值。
type testHooks struct {
	forceCopyFallback        atomic.Bool
	forceBaselineCopyFallbk  atomic.Bool
	baselineCopyChunkDelayMs atomic.Int64
	validationDelayMs        atomic.Int64
	reloadCleanupFail        atomic.Bool
	// saveWriteFailAfter 为负表示禁用；否则在写入该字节数后注入一次写失败。
	saveWriteFailAfter atomic.Int64
	// savePause 非空时，保存在完成前置校验后暂停，直到测试释放。
	savePause atomic.Pointer[testPause]
}

// testPause 让测试在固定位置暂停一个后台调用：entered 在到达时关闭，release 后继续。
type testPause struct {
	entered  chan struct{}
	released chan struct{}
}

func newTestPause() *testPause {
	return &testPause{entered: make(chan struct{}), released: make(chan struct{})}
}

func (p *testPause) pause() {
	close(p.entered)
	<-p.released
}

// NewManager 以 root 为状态根目录创建管理器，并清理崩溃遗留的复制兜底与会话临时资产。
func NewManager(root string) *Manager {
	if err := cleanupCopyFallbacks(filepath.Join(root, "tasks"), ""); err != nil {
		applog.Warn("SegmentedDocument", "清理过期复制兜底失败，继续启动分段引擎："+err.Error())
	}
	if err := cleanupSessionTransients(filepath.Join(root, "sessions")); err != nil {
		applog.Warn("SegmentedDocument", "清理未发布的会话 baseline/Added 资产失败："+err.Error())
	}
	manager := &Manager{root: root, byID: map[string]*session{}, byPath: map[string]string{}, tasks: map[string]*taskControl{}}
	manager.hooks.saveWriteFailAfter.Store(-1)
	return manager
}

func (m *Manager) session(id string) (*session, error) {
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return nil, newError("session-not-found", "分段文档会话不存在")
	}
	return s, nil
}

func (m *Manager) recoveryJournalPath(path string) string {
	return filepath.Join(m.root, "recovery", recoveryKey(path)+".journal")
}

// OpenDocument 打开 .txt/.json 文档并返回首窗；源文件缺失但有恢复日志时，先物化恢复文档再报告。
func (m *Manager) OpenDocument(path string, sink EventSink) (*OpenResult, error) {
	requested, err := absoluteNormalizedPath(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(requested)
	if err != nil {
		if !isNotExist(err) {
			return nil, newErrorf("open-failed", "解析分段文档路径失败：%v", err)
		}
		if _, err := documentKindFromPath(requested); err != nil {
			return nil, err
		}
		conflict, err := recoveryConflictForMissing(m.recoveryJournalPath(requested))
		if err != nil {
			return nil, err
		}
		if conflict != nil {
			recovered, err := materializeRecoveryDocument(requested, m.root, conflict)
			if err != nil {
				return nil, err
			}
			cleanupRecoveryConflict(conflict)
			return nil, sourceMissingRecovered(recovered)
		}
		return nil, newErrorf("open-failed", "源文件不存在且没有可用恢复资产：%s", requested)
	}
	sessionID := nextSessionID()
	m.registryMu.Lock()
	if existing, ok := m.byPath[canonical]; ok {
		m.registryMu.Unlock()
		return nil, newErrorf("session-already-open", "文件已在分段会话中打开：%s", existing)
	}
	m.byPath[canonical] = sessionID
	m.registryMu.Unlock()
	candidate, err := m.buildCandidate(canonical, sessionID, sink, true)
	if err != nil {
		// 构造失败时释放预占的路径，未提交前 registry 中只有这一项。
		m.registryMu.Lock()
		if m.byPath[canonical] == sessionID {
			delete(m.byPath, canonical)
		}
		m.registryMu.Unlock()
		return nil, err
	}
	m.registryMu.Lock()
	m.byID[sessionID] = candidate.session
	m.registryMu.Unlock()
	return candidate.activate(), nil
}

// sessionCandidate 是已完成首窗与恢复的会话；activate 前不触碰 registry，失败时 discard。
type sessionCandidate struct {
	session         *session
	result          OpenResult
	indexReady      bool
	persistCache    bool
	validationReady bool
	baselineReady   bool
}

func (c *sessionCandidate) activate() *OpenResult {
	if c.baselineReady {
		if !c.validationReady {
			startEncodingValidation(c.session)
		}
		if !c.indexReady {
			startLineIndex(c.session, false, c.persistCache)
		}
	} else {
		startBaselineMaterialization(c.session, c.indexReady, c.validationReady, c.persistCache)
	}
	result := c.result
	return &result
}

func (c *sessionCandidate) discard() {
	if err := c.session.added.remove(); err != nil {
		applog.Warn("SegmentedDocument", "清理未启用的候选会话失败："+err.Error())
	}
	c.session.releaseResources()
}

func (m *Manager) buildCandidate(path, sessionID string, sink EventSink, recover bool) (*sessionCandidate, error) {
	kind, err := documentKindFromPath(path)
	if err != nil {
		return nil, err
	}
	// 探测、baseline 与首窗句柄必须来自同一个文件对象；不能读完探测后才按路径采身份。
	probeFile, err := os.Open(path)
	if err != nil {
		return nil, newErrorf("open-failed", "打开分段文档失败：%v", err)
	}
	info, err := probeFile.Stat()
	if err != nil {
		probeFile.Close()
		return nil, newErrorf("open-failed", "读取文件状态失败：%v", err)
	}
	if !info.Mode().IsRegular() {
		probeFile.Close()
		return nil, newError("not-a-file", "目标路径不是文件")
	}
	baseline, err := readFileIdentity(path, probeFile)
	if err != nil {
		probeFile.Close()
		return nil, err
	}
	probeBytes, err := io.ReadAll(io.LimitReader(probeFile, openProbeBytes))
	if err != nil {
		probeFile.Close()
		return nil, asError(err)
	}
	if err := ensureOpenIdentityUnchanged(path, probeFile, baseline); err != nil {
		probeFile.Close()
		return nil, err
	}
	probeComplete := baseline.PhysicalLen <= uint64(len(probeBytes))
	probed := probe(probeBytes, probeComplete)
	pendingOriginal, err := originalFromFile(probeFile, probed.bomLen)
	if err != nil {
		probeFile.Close()
		return nil, err
	}
	// 采集句柄元数据后再复核一次，封住校验与首窗句柄构造之间的窄窗口。
	if err := ensureOpenIdentityUnchanged(path, pendingOriginal.file, baseline); err != nil {
		pendingOriginal.release()
		return nil, err
	}
	baselineAssetPath := filepath.Join(m.root, "sessions", sessionID+".baseline")
	var immutable *originalFile
	if !m.hooks.forceBaselineCopyFallbk.Load() {
		if immutable, err = tryOpenCOWImmutable(path, baselineAssetPath, probed.bomLen); err != nil {
			pendingOriginal.release()
			return nil, err
		}
	}
	if immutable == nil && probeComplete {
		if immutable, err = createImmutableFromBytes(baselineAssetPath, probeBytes, probed.bomLen); err != nil {
			pendingOriginal.release()
			return nil, err
		}
	}
	baselineReady := immutable != nil
	// 建立 baseline 后再按探测句柄复核身份，确认首窗字节与 baseline 来自同一个文件版本。
	if err := ensureOpenIdentityUnchanged(path, pendingOriginal.file, baseline); err != nil {
		pendingOriginal.release()
		immutable.release()
		return nil, err
	}
	original := pendingOriginal
	if baselineReady {
		pendingOriginal.release()
		original = immutable
	}
	// 非 COW 文件系统只用源句柄发布首窗；baseline 完成前 readonly 门禁阻止产生补丁。
	// fail 统一释放已获取的 original 引用。
	fail := func(err error) (*sessionCandidate, error) {
		original.release()
		return nil, err
	}
	added, err := createAddedStore(filepath.Join(m.root, "sessions", sessionID+".added"))
	if err != nil {
		return fail(err)
	}
	failAdded := func(err error) (*sessionCandidate, error) {
		_ = added.remove()
		return fail(err)
	}
	journalPath := m.recoveryJournalPath(path)
	var journal *editJournal
	var persistedRevision uint64
	var recoveredRecords []journalRecord
	var conflict *recoveryConflict
	if recover {
		journal, persistedRevision, recoveredRecords, conflict, err = openJournal(journalPath, baseline)
		if err != nil {
			return failAdded(err)
		}
	} else {
		journal = emptyJournal(journalPath, baseline)
	}
	var recoveryConflictPath *string
	if conflict != nil {
		recovered, err := materializeRecoveryDocument(path, m.root, conflict)
		if err != nil {
			return failAdded(err)
		}
		cleanupRecoveryConflict(conflict)
		recoveryConflictPath = &recovered
	}
	tree := treeFromOriginal(original.bodyLen)
	revision := persistedRevision
	for _, record := range recoveredRecords {
		if record.BaseRevision != revision {
			return failAdded(newError("journal-revision-gap", "恢复日志 revision 不连续"))
		}
		if tree, err = replayRecord(tree, added, record); err != nil {
			return failAdded(err)
		}
		revision = record.Revision
	}
	indexCachePath := filepath.Join(m.root, "indexes", recoveryKey(path)+".json")
	indexCacheKey := baselineCacheKey(baseline)
	var cachedIndex *lineIndex
	if len(recoveredRecords) == 0 {
		if cachedIndex, err = loadLineIndexCache(indexCachePath, indexCacheKey); err != nil {
			applog.Warn("SegmentedDocument", "忽略无效行索引缓存并重新扫描："+err.Error())
			cachedIndex = nil
		}
	}
	indexReady := cachedIndex != nil
	meta := sessionMetadata{
		encoding:           probed.encoding,
		lineEnding:         probed.lineEnding,
		filesystemReadonly: info.Mode().Perm()&0o222 == 0,
		baselineReady:      baselineReady,
		validationComplete: indexReady || probeComplete || probed.encoding == EncodingUnsupported,
	}
	if cachedIndex != nil {
		switch {
		case !cachedIndex.UTF8Valid:
			meta.encoding = EncodingUnsupported
		case probed.bomLen == 3:
			meta.encoding = EncodingUTF8BOM
		default:
			meta.encoding = EncodingUTF8
		}
		meta.lineEnding = cachedIndex.LineEnding
	}
	initialIndex := cachedIndex
	if initialIndex == nil {
		initialIndex = pendingLineIndex(tree.len(), revision)
	}
	s := &session{
		id:                       sessionID,
		path:                     path,
		kind:                     kind,
		bomLen:                   probed.bomLen,
		meta:                     meta,
		added:                    added,
		baseline:                 baseline,
		recoveryBaseline:         original.retain(),
		initialBaselineAssetPath: baselineAssetPath,
		journal:                  journal,
		cache:                    newChunkCache(defaultCacheCapacityBytes),
		indexCachePath:           indexCachePath,
		indexCacheKey:            indexCacheKey,
		sink:                     sink,
		validationChunkDelay:     time.Duration(m.hooks.validationDelayMs.Load()) * time.Millisecond,
		baselineCopyChunkDelay:   time.Duration(m.hooks.baselineCopyChunkDelayMs.Load()) * time.Millisecond,
		state: sessionState{
			revision:            revision,
			persistedRevision:   persistedRevision,
			tree:                tree,
			original:            original,
			lineIndex:           initialIndex,
			initialIndexPending: !indexReady,
		},
	}
	if !indexReady {
		s.initialIndexSnapshot = &snapshot{revision: revision, tree: tree, original: original.retain(), added: added}
	}
	candidate := &sessionCandidate{session: s}
	firstSnapshot, firstIndex, err := s.snapshotWithIndex()
	if err != nil {
		candidate.discard()
		return nil, err
	}
	firstWindow, err := readWindowFromSnapshot(s, firstSnapshot, firstIndex, 0, firstWindowBytes, nil)
	firstSnapshot.release()
	if err != nil {
		candidate.discard()
		return nil, err
	}
	current := s.metadata()
	candidate.result = OpenResult{
		SessionID:            sessionID,
		Revision:             revision,
		PersistedRevision:    persistedRevision,
		DocumentKind:         kind,
		Encoding:             current.encoding,
		LineEnding:           current.lineEnding,
		ByteLength:           tree.len(),
		FilesystemReadonly:   current.filesystemReadonly,
		Readonly:             current.readonly(),
		FirstWindow:          *firstWindow,
		RecoveryConflictPath: recoveryConflictPath,
	}
	candidate.indexReady = indexReady
	candidate.persistCache = len(recoveredRecords) == 0
	candidate.validationReady = meta.validationComplete
	candidate.baselineReady = baselineReady
	return candidate, nil
}

// ReloadSession 按当前路径重新打开文档并原子替换旧会话；候选验证失败时旧会话完全不动。
func (m *Manager) ReloadSession(sessionID string, sink EventSink) (*OpenResult, error) {
	old, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	path := old.currentPath()
	candidate, err := m.buildCandidate(path, nextSessionID(), sink, false)
	if err != nil {
		return nil, err
	}
	if !old.closing.CompareAndSwap(false, true) {
		candidate.discard()
		return nil, errSessionClosing
	}
	m.cancelTasksForSession(sessionID)
	old.indexGeneration.Add(1)
	old.validationGeneration.Add(1)
	swapErr := func() error {
		old.saveMu.Lock()
		defer old.saveMu.Unlock()
		old.lifecycleMu.Lock()
		defer old.lifecycleMu.Unlock()
		old.stateMu.RLock()
		historyAssets := old.state.history.allAssets()
		old.stateMu.RUnlock()
		old.journalMu.Lock()
		defer old.journalMu.Unlock()
		if old.currentPath() != path {
			return newError("reload-path-changed", "构建 reload 候选期间文档路径已变化，请重试")
		}
		m.registryMu.Lock()
		if m.byID[sessionID] != old || m.byPath[path] != sessionID {
			m.registryMu.Unlock()
			return newError("session-not-found", "旧分段文档会话已被替换")
		}
		delete(m.byID, sessionID)
		m.byID[candidate.session.id] = candidate.session
		m.byPath[path] = candidate.session.id
		m.registryMu.Unlock()

		// registry 交换后 reload 已提交；旧资产清理失败只能告警，不能返回失败并假装旧会话仍可用。
		if m.hooks.reloadCleanupFail.Swap(false) {
			applog.Warn("SegmentedDocument", "测试注入：reload 已提交，跳过旧会话资产清理")
			return nil
		}
		if err := old.journal.remove(); err != nil {
			applog.Warn("SegmentedDocument", "reload 后清理旧恢复日志失败："+err.Error())
		}
		for asset := range historyAssets {
			if err := removeIfExists(asset); err != nil {
				applog.Warn("SegmentedDocument", "reload 后清理旧历史资产失败："+err.Error())
			}
		}
		if err := old.added.remove(); err != nil {
			applog.Warn("SegmentedDocument", "reload 后清理旧 Added 文件失败："+err.Error())
		}
		return nil
	}()
	if swapErr != nil {
		old.closing.Store(false)
		candidate.discard()
		return nil, swapErr
	}
	old.releaseResources()
	return candidate.activate(), nil
}

// ReadWindow 读取 revision 下从 startByte 起约 targetBytes 字节、对齐到字符与 CRLF 边界的窗口。
func (m *Manager) ReadWindow(sessionID string, revision, startByte uint64, targetBytes int, requestID *uint64) (*Window, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	snap, index, err := s.snapshotWithIndex()
	if err != nil {
		return nil, err
	}
	defer snap.release()
	if revision != snap.revision {
		return nil, revisionConflict(revision, snap.revision)
	}
	return readWindowFromSnapshot(s, snap, index, startByte, targetBytes, requestID)
}

// ApplyEdits 原子提交一批编辑，生成新 revision 并记入撤销历史与恢复日志。
func (m *Manager) ApplyEdits(batch EditBatch) (*ApplyResult, error) {
	s, err := m.session(batch.SessionID)
	if err != nil {
		return nil, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	meta, err := s.ensureWritable()
	if err != nil {
		return nil, err
	}
	s.stateMu.RLock()
	if s.state.revision != batch.BaseRevision {
		actual := s.state.revision
		s.stateMu.RUnlock()
		return nil, revisionConflict(batch.BaseRevision, actual)
	}
	if len(batch.Edits) == 0 {
		result := &ApplyResult{Revision: s.state.revision, PersistedRevision: s.state.persistedRevision, Dirty: s.state.revision != s.state.persistedRevision}
		s.stateMu.RUnlock()
		return result, nil
	}
	snap, err := s.snapshotLocked()
	s.stateMu.RUnlock()
	if err != nil {
		return nil, err
	}
	defer snap.release()
	edits := sortedEdits(batch.Edits)
	if err := validateEditTransactionSize(edits); err != nil {
		return nil, err
	}
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
	inverseEdits := make([]Edit, 0, len(edits))
	indexEdits := make([]lineIndexEdit, 0, len(edits))
	invalidatedFrom, invalidatedTo := ^uint64(0), uint64(0)
	var historyWeight uint64
	var coordinateDelta int64
	for _, edit := range edits {
		inserted := normalizeInsertedText(edit.InsertedText, meta.lineEnding)
		deleted, err := snap.readRange(edit.FromByte, int(edit.ToByte-edit.FromByte))
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
		invalidatedFrom = min(invalidatedFrom, edit.FromByte)
		invalidatedTo = max(invalidatedTo, max(edit.ToByte, edit.FromByte+uint64(len(inserted))))
		historyWeight += edit.ToByte - edit.FromByte + uint64(len(inserted))
		if !utf8Valid(inserted) {
			return nil, newError("invalid-utf8", "插入文本不是有效 UTF-8")
		}
		if !utf8Valid(deleted) {
			return nil, newError("invalid-utf8", "被替换范围不是有效 UTF-8")
		}
		inverseFrom := uint64(int64(edit.FromByte) + coordinateDelta)
		inverseEdits = append(inverseEdits, Edit{FromByte: inverseFrom, ToByte: inverseFrom + uint64(len(inserted)), InsertedText: string(deleted)})
		coordinateDelta += int64(len(inserted)) - int64(edit.ToByte-edit.FromByte)
		journalEdits = append(journalEdits, Edit{FromByte: edit.FromByte, ToByte: edit.ToByte, InsertedText: string(inserted)})
		indexEdits = append(indexEdits, lineIndexEdit{
			fromByte: edit.FromByte, toByte: edit.ToByte, insertedBytes: uint64(len(inserted)),
			insertedLines: countLineBreaks(inserted), deletedLines: countLineBreaks(deleted),
		})
		prepared = append(prepared, preparedEdit{from: edit.FromByte, to: edit.ToByte, offset: offset, inserted: inserted})
	}
	nextTree := snap.tree
	for i := len(prepared) - 1; i >= 0; i-- {
		edit := prepared[i]
		var p *piece
		if edit.offset != nil {
			value := addedPiece(*edit.offset, uint64(len(edit.inserted)), countLineBreaks(edit.inserted))
			p = &value
		}
		if nextTree, err = nextTree.replaceRange(edit.from, edit.to, p); err != nil {
			return nil, err
		}
	}
	s.cache.clear()
	// journal 与 state 在同一提交区间内持锁，避免“命令返回错误但 revision 已生效”。
	s.journalMu.Lock()
	s.stateMu.Lock()
	if s.state.revision != batch.BaseRevision {
		actual := s.state.revision
		s.stateMu.Unlock()
		s.journalMu.Unlock()
		return nil, revisionConflict(batch.BaseRevision, actual)
	}
	nextRevision := batch.BaseRevision + 1
	s.state.history.pushTransaction(historyOperation{edits: inverseEdits}, historyOperation{edits: journalEdits}, historyWeight)
	s.state.revision = nextRevision
	s.state.tree = nextTree
	updateLineIndexAfterEdits(&s.state, lineIndexDelta{baseRevision: batch.BaseRevision, revision: nextRevision, edits: indexEdits})
	persisted := s.state.persistedRevision
	s.journal.record(journalRecord{BaseRevision: batch.BaseRevision, Revision: nextRevision, Edits: journalEdits})
	cleanupUnreferencedHistoryAssets(&s.state, s.journal)
	s.stateMu.Unlock()
	s.journalMu.Unlock()
	scheduleJournalFlush(s)
	startLineIndex(s, true, false)
	return &ApplyResult{
		Revision:            nextRevision,
		PersistedRevision:   persisted,
		Dirty:               nextRevision != persisted,
		InvalidatedFromByte: invalidatedFrom,
		InvalidatedToByte:   invalidatedTo,
	}, nil
}

// UndoRevision 撤销最近一次事务。编码校验发现非法内容后仍允许撤销直至基线。
func (m *Manager) UndoRevision(sessionID string, baseRevision uint64) (*HistoryResult, error) {
	return m.moveHistory(sessionID, baseRevision, false)
}

// RedoRevision 重做最近撤销的事务；受只读门禁约束。
func (m *Manager) RedoRevision(sessionID string, baseRevision uint64) (*HistoryResult, error) {
	return m.moveHistory(sessionID, baseRevision, true)
}

func (m *Manager) moveHistory(sessionID string, baseRevision uint64, redo bool) (*HistoryResult, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if err := s.ensureActive(); err != nil {
		return nil, err
	}
	if redo {
		if _, err := s.ensureWritable(); err != nil {
			return nil, err
		}
	}
	s.stateMu.RLock()
	if s.state.revision != baseRevision {
		actual := s.state.revision
		s.stateMu.RUnlock()
		return nil, revisionConflict(baseRevision, actual)
	}
	stack := s.state.history.undo
	if redo {
		stack = s.state.history.redo
	}
	if len(stack) == 0 {
		result := s.historyResultLocked(false)
		s.stateMu.RUnlock()
		return result, nil
	}
	entry := stack[len(stack)-1]
	operation := entry.undo
	if redo {
		operation = entry.redo
	}
	snap, err := s.snapshotLocked()
	s.stateMu.RUnlock()
	if err != nil {
		return nil, err
	}
	defer snap.release()
	prepared, err := prepareHistoryOperation(s, snap, operation, s.metadata().lineEnding)
	if err != nil {
		return nil, err
	}
	s.cache.clear()
	s.journalMu.Lock()
	s.stateMu.Lock()
	if s.state.revision != baseRevision {
		actual := s.state.revision
		s.stateMu.Unlock()
		s.journalMu.Unlock()
		prepared.original.release()
		return nil, revisionConflict(baseRevision, actual)
	}
	history := &s.state.history
	if redo {
		if len(history.redo) == 0 {
			s.stateMu.Unlock()
			s.journalMu.Unlock()
			prepared.original.release()
			return nil, newError("history-empty", "撤销/重做历史已变化")
		}
		moved := history.redo[len(history.redo)-1]
		history.redo = history.redo[:len(history.redo)-1]
		history.undo = append(history.undo, moved)
	} else {
		if len(history.undo) == 0 {
			s.stateMu.Unlock()
			s.journalMu.Unlock()
			prepared.original.release()
			return nil, newError("history-empty", "撤销/重做历史已变化")
		}
		moved := history.undo[len(history.undo)-1]
		history.undo = history.undo[:len(history.undo)-1]
		history.redo = append(history.redo, moved)
	}
	previousOriginal := s.state.original
	s.state.tree = prepared.tree
	s.state.original = prepared.original
	s.state.revision++
	if prepared.indexEdits != nil {
		updateLineIndexAfterEdits(&s.state, lineIndexDelta{baseRevision: baseRevision, revision: baseRevision + 1, edits: prepared.indexEdits})
	} else {
		invalidateLineIndexForReplacement(&s.state)
	}
	s.journal.record(journalRecord{BaseRevision: baseRevision, Revision: s.state.revision, Edits: prepared.journalEdits, ReplacementFile: prepared.replacement})
	cleanupUnreferencedHistoryAssets(&s.state, s.journal)
	result := s.historyResultLocked(true)
	s.stateMu.Unlock()
	s.journalMu.Unlock()
	previousOriginal.release()
	scheduleJournalFlush(s)
	startLineIndex(s, true, false)
	return result, nil
}

func (s *session) historyResultLocked(changed bool) *HistoryResult {
	return &HistoryResult{
		Changed:           changed,
		Revision:          s.state.revision,
		PersistedRevision: s.state.persistedRevision,
		ByteLength:        s.state.tree.len(),
		Dirty:             s.state.revision != s.state.persistedRevision,
		CanUndo:           s.state.history.canUndo(),
		CanRedo:           s.state.history.canRedo(),
	}
}

// FlushJournal 立即把 revision 之前的补丁落盘到恢复日志。
func (m *Manager) FlushJournal(sessionID string, revision uint64) (*FlushResult, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if _, err := flushSessionJournal(s, &revision); err != nil {
		return nil, err
	}
	return &FlushResult{Revision: revision}, nil
}

// SaveRevision 把 revision 的冻结快照流式写入目标（为空时写回原路径）并原子替换，
// 之后以写出的文件作为新 baseline；保存期间产生的更高 revision 通过保留的日志重放。
func (m *Manager) SaveRevision(sessionID string, revision uint64, targetPath string, overwriteExternal bool) (*SaveResult, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.closing.Load() {
		return nil, errSessionClosing
	}
	s.saveInProgress.Store(true)
	defer s.saveInProgress.Store(false)
	snap, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	defer snap.release()
	if snap.revision != revision {
		return nil, revisionConflict(revision, snap.revision)
	}
	meta := s.metadata()
	if !meta.baselineReady {
		return nil, newError("baseline-materialization-pending", "不可变 baseline 仍在后台准备，完成前不能保存")
	}
	s.stateMu.RLock()
	persistedRevision := s.state.persistedRevision
	s.stateMu.RUnlock()
	if !meta.validationComplete && revision != persistedRevision {
		return nil, newError("encoding-validation-pending", "后台编码校验尚未完成；补丁已保留，校验完成后可保存")
	}
	if meta.encoding == EncodingUnsupported && revision != persistedRevision {
		return nil, newError("unsupported-dirty-save", "后台发现非 UTF-8 内容；已产生的补丁会保留，但必须先转换编码再保存")
	}
	if pause := m.hooks.savePause.Swap(nil); pause != nil {
		pause.pause()
	}
	currentPath := s.currentPath()
	if targetPath == "" {
		targetPath = currentPath
	}
	target, err := normalizeSaveTarget(targetPath)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		kind, err := documentKindFromPath(target)
		if err != nil {
			return nil, err
		}
		if kind != s.kind {
			return nil, newError("save-kind-mismatch", "另存为目标必须保留当前 TXT/JSON 文档类型")
		}
	}
	savingOriginalPath := target == currentPath
	if savingOriginalPath && !overwriteExternal {
		// recovery flush 会读取不可变 original；先稳定映射外部修改错误，避免底层读错误越过 external-file-changed 契约。
		if err := ensureBaselineUnchanged(s, target); err != nil {
			return nil, err
		}
	}
	// 冻结快照已捕获并完成基线校验；随后刷新可包含更高 revision，保存仍只写冻结的树。
	if _, err := flushSessionJournal(s, nil); err != nil {
		return nil, err
	}
	reservation, err := m.acquireSavePath(sessionID, currentPath, target)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			reservation.rollback()
		}
	}()
	if savingOriginalPath && !overwriteExternal {
		if err := ensureBaselineUnchanged(s, target); err != nil {
			return nil, err
		}
	}
	parent := filepath.Dir(target)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return nil, newErrorf("save-directory-missing", "保存目录不存在：%s", parent)
	}
	tempPath := uniqueTempPath(parent, target)
	if err := m.writeSaveTemp(s, snap, target, tempPath, savingOriginalPath && !overwriteExternal); err != nil {
		if cleanupErr := removeIfExists(tempPath); cleanupErr != nil {
			applog.Warn("SegmentedDocument", "清理失败的保存临时文件失败："+cleanupErr.Error())
		}
		return nil, err
	}
	newBaseline, err := readIdentity(target)
	if err != nil {
		return nil, err
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return nil, newErrorf("save-metadata-failed", "读取已保存文件权限失败：%v", err)
	}
	targetReadonly := targetInfo.Mode().Perm()&0o222 == 0
	replacement, err := openImmutableOriginal(target, filepath.Join(m.root, "sessions",
		fmt.Sprintf("%s-%d-%s.baseline", s.id, revision, nextSessionID())), s.bomLen)
	if err != nil {
		return nil, err
	}
	if identity, err := readIdentity(target); err != nil || !identity.equal(newBaseline) {
		replacement.release()
		if err != nil {
			return nil, err
		}
		return nil, newError("external-file-changed-after-save", "保存后建立不可变 baseline 期间目标文件又被外部修改")
	}
	nextJournalPath := m.recoveryJournalPath(target)
	// 与 ApplyEdits 相同按 journal → state 顺序提交：保存旧 revision 时只剪掉已落盘记录，保留并发产生的更高 revision。
	s.lifecycleMu.Lock()
	s.journalMu.Lock()
	s.stateMu.Lock()
	unlock := func() {
		s.stateMu.Unlock()
		s.journalMu.Unlock()
		s.lifecycleMu.Unlock()
	}
	rebased := treeFromOriginal(replacement.bodyLen)
	for _, record := range s.journal.records {
		if record.Revision <= revision {
			continue
		}
		if rebased, err = replayRecord(rebased, s.added, record); err != nil {
			unlock()
			replacement.release()
			return nil, err
		}
	}
	if err := s.journal.prune(revision, newBaseline, nextJournalPath, s.state.history.protectedAssets(), s.bomLen); err != nil {
		unlock()
		replacement.release()
		return nil, err
	}
	cleanupUnreferencedHistoryAssets(&s.state, s.journal)
	reservation.commit()
	committed = true
	s.pathMu.Lock()
	s.path = target
	s.pathMu.Unlock()
	s.baselineMu.Lock()
	s.baseline = newBaseline
	s.baselineMu.Unlock()
	s.recoveryMu.Lock()
	previousRecovery := s.recoveryBaseline
	s.recoveryBaseline = replacement
	s.recoveryMu.Unlock()
	s.metaMu.Lock()
	s.meta.filesystemReadonly = targetReadonly
	readonly := s.meta.readonly()
	s.metaMu.Unlock()
	// 保存后让 original 指向刚写出的目标快照；更高 revision 由保留的日志重放，
	// 这样另存为后旧源文件再被修改也不会污染当前会话。
	previousOriginal := s.state.original
	s.state.original = replacement.retain()
	s.state.tree = rebased
	s.state.persistedRevision = revision
	currentRevision := s.state.revision
	dirty := currentRevision != revision
	var indexToCache *lineIndex
	if currentRevision == revision && s.state.lineIndex.Completed {
		indexToCache = s.state.lineIndex
	}
	unlock()
	previousRecovery.release()
	previousOriginal.release()
	if indexToCache != nil {
		cachePath := filepath.Join(m.root, "indexes", recoveryKey(target)+".json")
		if err := indexToCache.saveCache(cachePath, baselineCacheKey(newBaseline)); err != nil {
			applog.Warn("SegmentedDocument", "保存目标行索引缓存失败："+err.Error())
		}
	}
	return &SaveResult{
		SessionID:          sessionID,
		SavedRevision:      revision,
		CurrentRevision:    currentRevision,
		PersistedRevision:  revision,
		Dirty:              dirty,
		FilesystemReadonly: targetReadonly,
		Readonly:           readonly,
		ModifiedAt:         int64(newBaseline.ModifiedNanos / 1_000_000_000),
	}, nil
}

// writeSaveTemp 把快照（含 BOM）写入临时文件、保留目标权限并同步，最后原子替换目标。
func (m *Manager) writeSaveTemp(s *session, snap *snapshot, target, tempPath string, checkBaseline bool) error {
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return newErrorf("save-temp-create-failed", "创建临时保存文件失败：%v", err)
	}
	closed := false
	defer func() {
		if !closed {
			temp.Close()
		}
	}()
	if s.bomLen == 3 {
		if _, err := temp.Write(utf8BOM); err != nil {
			return asError(err)
		}
	}
	failAfter := m.hooks.saveWriteFailAfter.Swap(-1)
	if err := copySnapshotForSave(snap.reader(), temp, failAfter); err != nil {
		return newErrorf("save-write-failed", "流式写入临时文件失败：%v", err)
	}
	if info, err := os.Stat(target); err == nil {
		if err := temp.Chmod(info.Mode().Perm()); err != nil {
			return newErrorf("save-permissions-failed", "保留目标文件权限失败：%v", err)
		}
	} else if !isNotExist(err) {
		return newErrorf("save-metadata-failed", "读取目标文件权限失败：%v", err)
	}
	if err := temp.Sync(); err != nil {
		return newErrorf("save-sync-failed", "同步临时保存文件失败：%v", err)
	}
	closed = true
	temp.Close()
	if checkBaseline {
		if err := ensureBaselineUnchanged(s, target); err != nil {
			return err
		}
	}
	if err := replaceFile(tempPath, target); err != nil {
		return newErrorf("save-replace-failed", "原子替换文件失败：%v", err)
	}
	return asError(syncDir(filepath.Dir(target)))
}

// savePathReservation 在保存期间预占目标路径；commit 一次性切换路径占位，rollback 释放预占。
type savePathReservation struct {
	manager        *Manager
	sessionID      string
	previousPath   string
	targetPath     string
	insertedTarget bool
}

func (m *Manager) acquireSavePath(sessionID, previousPath, targetPath string) (*savePathReservation, error) {
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	inserted := false
	if owner, ok := m.byPath[targetPath]; ok {
		if owner != sessionID {
			return nil, newErrorf("save-target-open", "保存目标已由另一分段会话打开：%s", owner)
		}
	} else {
		m.byPath[targetPath] = sessionID
		inserted = true
	}
	return &savePathReservation{manager: m, sessionID: sessionID, previousPath: previousPath, targetPath: targetPath, insertedTarget: inserted}, nil
}

func (r *savePathReservation) commit() {
	r.manager.registryMu.Lock()
	defer r.manager.registryMu.Unlock()
	if r.previousPath != r.targetPath && r.manager.byPath[r.previousPath] == r.sessionID {
		delete(r.manager.byPath, r.previousPath)
	}
	r.manager.byPath[r.targetPath] = r.sessionID
}

func (r *savePathReservation) rollback() {
	if !r.insertedTarget {
		return
	}
	r.manager.registryMu.Lock()
	defer r.manager.registryMu.Unlock()
	if r.manager.byPath[r.targetPath] == r.sessionID {
		delete(r.manager.byPath, r.targetPath)
	}
}

// CloseSession 关闭会话：干净或放弃修改时删除日志与资产，否则先落盘日志以便下次恢复。
func (m *Manager) CloseSession(sessionID string, discardChanges bool) error {
	s, err := m.session(sessionID)
	if err != nil {
		return err
	}
	if !s.closing.CompareAndSwap(false, true) {
		return errSessionClosing
	}
	m.cancelTasksForSession(sessionID)
	s.indexGeneration.Add(1)
	s.validationGeneration.Add(1)
	closeErr := func() error {
		// 锁序与保存一致：先等待保存，再等待已进入提交点的编辑/写任务。
		s.saveMu.Lock()
		defer s.saveMu.Unlock()
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		s.stateMu.RLock()
		dirty := s.state.revision != s.state.persistedRevision
		revision := s.state.revision
		historyAssets := s.state.history.allAssets()
		s.stateMu.RUnlock()
		if discardChanges || !dirty {
			s.journalMu.Lock()
			err := s.journal.remove()
			s.journalMu.Unlock()
			if err != nil {
				return err
			}
			for asset := range historyAssets {
				if err := removeIfExists(asset); err != nil {
					return asError(err)
				}
			}
			if err := s.added.remove(); err != nil {
				return err
			}
		} else {
			if _, err := flushSessionJournal(s, &revision); err != nil {
				return err
			}
			s.journalMu.Lock()
			s.stateMu.Lock()
			if s.state.revision != revision {
				actual := s.state.revision
				s.stateMu.Unlock()
				s.journalMu.Unlock()
				return revisionConflict(revision, actual)
			}
			orphaned := s.state.history.cleanupCandidatesAfterHistoryDrop(s.journal.referencedAssets())
			for _, asset := range orphaned {
				if err := removeIfExists(asset); err != nil {
					s.stateMu.Unlock()
					s.journalMu.Unlock()
					return asError(err)
				}
			}
			s.state.history.forgetAssets(orphaned)
			s.stateMu.Unlock()
			s.journalMu.Unlock()
			// 日志已内嵌普通补丁，任务替换另有独立文件引用；旧会话 Added 不参与恢复。
			if err := s.added.remove(); err != nil {
				return err
			}
		}
		m.registryMu.Lock()
		delete(m.byID, sessionID)
		for path, owner := range m.byPath {
			if owner == sessionID {
				delete(m.byPath, path)
			}
		}
		m.registryMu.Unlock()
		return nil
	}()
	if closeErr != nil {
		s.closing.Store(false)
		return closeErr
	}
	s.releaseResources()
	return nil
}

// CheckExternalChange 比较磁盘上的当前身份与会话基线，报告外部修改或删除。
func (m *Manager) CheckExternalChange(sessionID string) (*ExternalChangeResult, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	path := s.currentPath()
	s.baselineMu.Lock()
	baseline := s.baseline
	s.baselineMu.Unlock()
	change := ExternalNone
	var modifiedAt int64
	var current *baselineIdentity
	if _, err := os.Stat(path); err != nil {
		if !isNotExist(err) {
			return nil, newErrorf("file-identity-failed", "检查外部文件变化失败：%v", err)
		}
		change = ExternalDeleted
	} else {
		identity, err := readIdentity(path)
		if err != nil {
			return nil, err
		}
		modifiedAt = int64(identity.ModifiedNanos / 1_000_000_000)
		if !identity.equal(baseline) {
			change = ExternalModified
		}
		current = &identity
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return &ExternalChangeResult{
		SessionID:      sessionID,
		Revision:       s.state.revision,
		Change:         change,
		ChangeToken:    externalChangeToken(change, current, baseline),
		ModifiedAt:     modifiedAt,
		Dirty:          s.state.revision != s.state.persistedRevision,
		SaveInProgress: s.saveInProgress.Load(),
	}, nil
}

// SessionStatus 返回会话当前 revision、索引进度与编辑门禁。
func (m *Manager) SessionStatus(sessionID string) (*SessionStatus, error) {
	s, err := m.session(sessionID)
	if err != nil {
		return nil, err
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	meta := s.metadata()
	s.baseErrMu.Lock()
	baseErr := s.baseErr
	s.baseErrMu.Unlock()
	index := s.state.lineIndex
	return &SessionStatus{
		SessionID:          sessionID,
		Revision:           s.state.revision,
		PersistedRevision:  s.state.persistedRevision,
		ByteLength:         s.state.tree.len(),
		IndexedBytes:       index.IndexedBytes,
		TotalBytes:         index.TotalBytes,
		EstimatedLines:     index.EstimatedLines,
		Completed:          index.Completed,
		Encoding:           meta.encoding,
		LineEnding:         meta.lineEnding,
		FilesystemReadonly: meta.filesystemReadonly,
		Readonly:           meta.readonly(),
		BaselineError:      baseErr,
		CanUndo:            s.state.history.canUndo(),
		CanRedo:            s.state.history.canRedo(),
	}, nil
}

func (m *Manager) cancelTasksForSession(sessionID string) {
	m.tasksMu.Lock()
	defer m.tasksMu.Unlock()
	for _, control := range m.tasks {
		if control.sessionID == sessionID {
			control.cancel.Store(true)
		}
	}
}

func nextSessionID() string {
	sequence := nextSessionSequence.Add(1)
	return "seg-" + strconv.FormatUint(uint64(time.Now().UnixNano()), 16) + "-" + strconv.FormatUint(sequence, 16)
}

func sortedEdits(edits []Edit) []Edit {
	sorted := append([]Edit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].FromByte != sorted[j].FromByte {
			return sorted[i].FromByte < sorted[j].FromByte
		}
		return sorted[i].ToByte < sorted[j].ToByte
	})
	return sorted
}
