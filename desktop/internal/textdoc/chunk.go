package textdoc

import (
	"container/list"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"nomo/desktop/internal/applog"
)

const (
	copyBufferBytes = 64 * 1024
	// defaultCacheCapacityBytes 是每个会话窗口缓存的字节上限（不是条数），超出按 LRU 淘汰。
	defaultCacheCapacityBytes = 8 * 1024 * 1024
)

type cacheKey struct{ revision, start, end uint64 }

// chunkCache 缓存最近读取的窗口字节；容量按字节计，编辑提交时整体清空。
type chunkCache struct {
	capacity int
	mu       sync.Mutex
	entries  map[cacheKey]*list.Element
	order    *list.List // 前端为最久未用
	used     int
}

type cacheEntry struct {
	key   cacheKey
	value []byte
}

func newChunkCache(capacity int) *chunkCache {
	return &chunkCache{capacity: capacity, entries: map[cacheKey]*list.Element{}, order: list.New()}
}

// get 返回的切片与缓存共享，调用方不得修改。
func (c *chunkCache) get(revision, start, end uint64) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[cacheKey{revision, start, end}]
	if !ok {
		return nil
	}
	c.order.MoveToBack(element)
	return element.Value.(*cacheEntry).value
}

// insert 缓存并返回 value；单块超过容量时不缓存。
func (c *chunkCache) insert(revision, start, end uint64, value []byte) []byte {
	if len(value) > c.capacity {
		return value
	}
	key := cacheKey{revision, start, end}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.used -= len(element.Value.(*cacheEntry).value)
		c.order.Remove(element)
		delete(c.entries, key)
	}
	for c.used+len(value) > c.capacity {
		oldest := c.order.Front()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*cacheEntry)
		c.order.Remove(oldest)
		delete(c.entries, entry.key)
		c.used -= len(entry.value)
	}
	c.used += len(value)
	c.entries[key] = c.order.PushBack(&cacheEntry{key: key, value: value})
	return value
}

func (c *chunkCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[cacheKey]*list.Element{}
	c.order.Init()
	c.used = 0
}

func (c *chunkCache) usage() (used, capacity int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used, c.capacity
}

// originalFile 是会话的只读正文来源：会话私有的不可变 baseline 资产，或首窗阶段的源文件句柄。
//
// 生命周期以引用计数管理：创建者持有 1 个引用，每个额外持有者（会话状态、恢复 baseline、
// 快照、后台任务）先 retain 再在结束时 release；计数归零时关闭句柄并删除私有资产。
type originalFile struct {
	refs        atomic.Int64
	file        *os.File
	bodyOffset  uint64
	bodyLen     uint64
	physicalLen int64
	modified    time.Time
	// ownedAsset 非空时为会话私有 baseline 资产，release 归零后删除；恢复目录通过硬链接独立持有它。
	ownedAsset string
}

func openOriginal(path string, bodyOffset uint64) (*originalFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, newErrorf("open-failed", "打开分段文档失败：%v", err)
	}
	original, err := originalFromFile(file, bodyOffset)
	if err != nil {
		file.Close()
		return nil, err
	}
	return original, nil
}

// originalFromFile 接管已完成身份绑定的句柄，避免探测校验后再次按路径打开产生 TOCTOU。
func originalFromFile(file *os.File, bodyOffset uint64) (*originalFile, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, asError(err)
	}
	physical := uint64(info.Size())
	original := &originalFile{
		file:        file,
		bodyOffset:  bodyOffset,
		bodyLen:     physical - min(physical, bodyOffset),
		physicalLen: info.Size(),
		modified:    info.ModTime(),
	}
	original.refs.Store(1)
	return original, nil
}

// openImmutableOriginal 以文件级 COW clone 建立独立于源路径写入的 baseline；不支持 clone 时流式复制。
func openImmutableOriginal(source, assetPath string, bodyOffset uint64) (*originalFile, error) {
	original, err := tryOpenCOWImmutable(source, assetPath, bodyOffset)
	if err != nil || original != nil {
		return original, err
	}
	if err := copyDurableImmutableAsset(source, assetPath, 0); err != nil {
		return nil, err
	}
	return openOwnedOriginal(assetPath, bodyOffset)
}

// tryOpenCOWImmutable 只尝试文件系统 COW；不支持时返回 (nil, nil)，由会话在首窗发布后后台流式物化。
func tryOpenCOWImmutable(source, assetPath string, bodyOffset uint64) (*originalFile, error) {
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o755); err != nil {
		return nil, asError(err)
	}
	if err := removeIfExists(assetPath); err != nil {
		return nil, asError(err)
	}
	if cloneFileCOW(source, assetPath) != nil {
		if err := removeIfExists(assetPath); err != nil {
			return nil, asError(err)
		}
		return nil, nil
	}
	if err := syncFile(assetPath); err != nil {
		return nil, asError(err)
	}
	if err := syncParentDir(assetPath); err != nil {
		return nil, asError(err)
	}
	return openOwnedOriginal(assetPath, bodyOffset)
}

// copyToImmutableOriginal 流式复制源文件为私有 baseline；delay 仅供测试放慢复制以覆盖竞态。
func copyToImmutableOriginal(source, assetPath string, bodyOffset uint64, chunkDelay time.Duration) (*originalFile, error) {
	if err := copyDurableImmutableAsset(source, assetPath, chunkDelay); err != nil {
		return nil, err
	}
	return openOwnedOriginal(assetPath, bodyOffset)
}

// createImmutableFromBytes 用已完整读入的小文件字节直接发布 baseline，避免跨平台后台竞态。
func createImmutableFromBytes(assetPath string, data []byte, bodyOffset uint64) (*originalFile, error) {
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o755); err != nil {
		return nil, asError(err)
	}
	if err := removeIfExists(assetPath); err != nil {
		return nil, asError(err)
	}
	output, err := os.OpenFile(assetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, asError(err)
	}
	if _, err := output.Write(data); err != nil {
		output.Close()
		return nil, asError(err)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return nil, asError(err)
	}
	output.Close()
	if err := syncParentDir(assetPath); err != nil {
		return nil, asError(err)
	}
	return openOwnedOriginal(assetPath, bodyOffset)
}

func openOwnedOriginal(assetPath string, bodyOffset uint64) (*originalFile, error) {
	original, err := openOriginal(assetPath, bodyOffset)
	if err != nil {
		_ = removeIfExists(assetPath)
		return nil, err
	}
	original.ownedAsset = assetPath
	return original, nil
}

func (o *originalFile) retain() *originalFile {
	o.refs.Add(1)
	return o
}

// release 放弃一个引用；最后一个引用释放时关闭句柄并删除私有资产。
func (o *originalFile) release() {
	if o == nil {
		return
	}
	switch remaining := o.refs.Add(-1); {
	case remaining > 0:
		return
	case remaining < 0:
		applog.Error("SegmentedDocument", "originalFile 引用计数为负，存在重复释放")
		return
	}
	o.file.Close()
	if o.ownedAsset != "" {
		if err := removeIfExists(o.ownedAsset); err != nil {
			applog.Warn("SegmentedDocument", fmt.Sprintf("清理会话 baseline 资产失败（%s）：%v", o.ownedAsset, err))
		}
	}
}

func (o *originalFile) immutableAssetPath() (string, error) {
	if o.ownedAsset == "" {
		return "", newError("immutable-baseline-missing", "当前 Original 不是可持久化的会话 baseline")
	}
	return o.ownedAsset, nil
}

func (o *originalFile) readRange(offset uint64, output []byte) (int, error) {
	if err := o.ensureUnchanged(); err != nil {
		return 0, err
	}
	n, err := o.file.ReadAt(output, int64(o.bodyOffset+offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	if err := o.ensureUnchanged(); err != nil {
		return 0, err
	}
	return n, nil
}

func (o *originalFile) ensureUnchanged() error {
	info, err := o.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != o.physicalLen || !info.ModTime().Equal(o.modified) {
		return errors.New("original file changed while reading immutable session snapshot")
	}
	return nil
}

func copyDurableImmutableAsset(source, target string, chunkDelay time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return asError(err)
	}
	if err := removeIfExists(target); err != nil {
		return asError(err)
	}
	input, err := os.Open(source)
	if err != nil {
		return asError(err)
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return asError(err)
	}
	buffer := make([]byte, copyBufferBytes)
	for {
		n, readErr := input.Read(buffer)
		if n > 0 {
			if _, err := output.Write(buffer[:n]); err != nil {
				output.Close()
				return asError(err)
			}
			if chunkDelay > 0 {
				time.Sleep(chunkDelay)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			output.Close()
			return asError(readErr)
		}
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return asError(err)
	}
	output.Close()
	return asError(syncParentDir(target))
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !isNotExist(err) {
		return err
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// addedStore 是会话私有的追加写文件，保存所有插入文本；由会话独占，关闭或重载时显式 remove。
type addedStore struct {
	path string
	mu   sync.Mutex
	file *os.File // remove 后为 nil
	len  uint64
}

func createAddedStore(path string) (*addedStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, asError(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, asError(err)
	}
	return &addedStore{path: path, file: file}, nil
}

var errAddedClosed = newError("added-store-closed", "Added 文件已经关闭")

func (a *addedStore) append(data []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return 0, errAddedClosed
	}
	offset := a.len
	if _, err := a.file.WriteAt(data, int64(offset)); err != nil {
		return 0, asError(err)
	}
	a.len += uint64(len(data))
	return offset, nil
}

func (a *addedStore) appendReader(reader io.Reader) (uint64, uint64, error) {
	return a.appendReaderCancellable(reader, func() bool { return false })
}

func (a *addedStore) appendReaderCancellable(reader io.Reader, shouldCancel func() bool) (uint64, uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return 0, 0, errAddedClosed
	}
	offset := a.len
	buffer := make([]byte, copyBufferBytes)
	var written uint64
	for {
		if shouldCancel() {
			return 0, 0, newError("task-cancelled", "后台任务已取消")
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, err := a.file.WriteAt(buffer[:n], int64(offset+written)); err != nil {
				return 0, 0, asError(err)
			}
			written += uint64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, 0, asError(readErr)
		}
	}
	a.len += written
	return offset, written, nil
}

func (a *addedStore) sync() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return errAddedClosed
	}
	return asError(a.file.Sync())
}

func (a *addedStore) length() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.len
}

// remove 关闭并删除 Added 文件；删除失败时重新打开句柄，让调用方返回错误后会话仍可继续或重试。
func (a *addedStore) remove() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file != nil {
		a.file.Close()
		a.file = nil
	}
	if err := os.Remove(a.path); err != nil && !isNotExist(err) {
		if file, reopenErr := os.OpenFile(a.path, os.O_RDWR, 0o644); reopenErr == nil {
			a.file = file
		}
		return asError(err)
	}
	return nil
}

func (a *addedStore) readRange(offset uint64, output []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return 0, errAddedClosed
	}
	n, err := a.file.ReadAt(output, int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return n, asError(err)
	}
	return n, nil
}

// snapshot 是某个 revision 的只读视图。持有一个 original 引用，用完必须 release。
type snapshot struct {
	revision uint64
	tree     pieceTree
	original *originalFile
	added    *addedStore
}

func (s *snapshot) release() {
	if s != nil && s.original != nil {
		s.original.release()
		s.original = nil
	}
}

// clone 复制快照并为副本额外持有一个 original 引用。
func (s *snapshot) clone() *snapshot {
	return &snapshot{revision: s.revision, tree: s.tree, original: s.original.retain(), added: s.added}
}

func (s *snapshot) len() uint64 { return s.tree.len() }

// readRange 读取 [start, start+length) 与文档长度的交集。
func (s *snapshot) readRange(start uint64, length int) ([]byte, error) {
	if start > s.len() {
		return nil, newErrorf("invalid-read-range", "读取位置越界：%d > %d", start, s.len())
	}
	wanted := min(uint64(length), s.len()-start)
	output := make([]byte, wanted)
	segments, err := s.tree.segments(start, start+wanted)
	if err != nil {
		return nil, err
	}
	var outputOffset uint64
	for _, seg := range segments {
		target := output[outputOffset : outputOffset+seg.length]
		var n int
		if seg.piece.source == sourceOriginal {
			n, err = s.original.readRange(seg.piece.offset+seg.relative, target)
		} else {
			n, err = s.added.readRange(seg.piece.offset+seg.relative, target)
		}
		if err != nil {
			return nil, asError(err)
		}
		if uint64(n) != seg.length {
			return nil, newError("unexpected-eof", "分段正文在 Piece 区间内提前结束")
		}
		outputOffset += seg.length
	}
	return output, nil
}

// byteAt 返回 offset 处的字节；越界时 ok 为 false。
func (s *snapshot) byteAt(offset uint64) (byte, bool, error) {
	if offset >= s.len() {
		return 0, false, nil
	}
	data, err := s.readRange(offset, 1)
	if err != nil || len(data) == 0 {
		return 0, false, err
	}
	return data[0], true, nil
}

func (s *snapshot) reader() *snapshotReader { return &snapshotReader{snapshot: s} }

// snapshotReader 顺序读取快照正文；不持有额外引用，生命周期受所属快照约束。
type snapshotReader struct {
	snapshot *snapshot
	position uint64
}

func (r *snapshotReader) Read(output []byte) (int, error) {
	if r.position >= r.snapshot.len() {
		return 0, io.EOF
	}
	data, err := r.snapshot.readRange(r.position, len(output))
	if err != nil {
		return 0, err
	}
	copy(output, data)
	r.position += uint64(len(data))
	return len(data), nil
}
