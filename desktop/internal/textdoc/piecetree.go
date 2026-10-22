package textdoc

import (
	"fmt"
	"sync/atomic"
)

// pieceSource 区分片段来自不可变原文（original）还是追加写入的 Added 文件。
type pieceSource uint8

const (
	sourceOriginal pieceSource = iota
	sourceAdded
)

type piece struct {
	source pieceSource
	offset uint64
	length uint64
	// lineBreaks 为 nil 表示尚未统计：原文件首窗返回时还没有完成全量扫描。
	lineBreaks *uint64
}

func originalPiece(offset, length uint64) piece {
	return piece{source: sourceOriginal, offset: offset, length: length}
}

func addedPiece(offset, length, lineBreaks uint64) piece {
	return piece{source: sourceAdded, offset: offset, length: length, lineBreaks: &lineBreaks}
}

func (p piece) slice(start, length uint64) piece {
	next := piece{source: p.source, offset: p.offset + start, length: length}
	// 未读取被切分区间时不能猜测换行数；行索引负责精确映射。
	if start == 0 && length == p.length {
		next.lineBreaks = p.lineBreaks
	}
	return next
}

// treapNode 一经创建不再修改，因此可在多个 pieceTree 版本间共享。
type treapNode struct {
	piece      piece
	left       *treapNode
	right      *treapNode
	priority   uint64
	logicalLen uint64
	lineBreaks *uint64
}

// pieceTree 是持久化隐式 Treap：复制只复制根指针，编辑与按字节定位均为期望 O(log n)。
type pieceTree struct {
	root *treapNode
}

func treeFromOriginal(length uint64) pieceTree {
	if length == 0 {
		return pieceTree{}
	}
	return pieceTree{root: newLeaf(originalPiece(0, length))}
}

func (t pieceTree) len() uint64 { return nodeLen(t.root) }

// replaceRange 用 inserted（可空）替换 [from, to)，返回新版本，原版本不变。
func (t pieceTree) replaceRange(from, to uint64, inserted *piece) (pieceTree, error) {
	if from > to || to > t.len() {
		return pieceTree{}, newErrorf("invalid-edit-range", "编辑范围越界：%d..%d, len=%d", from, to, t.len())
	}
	left, remainder, err := split(t.root, from)
	if err != nil {
		return pieceTree{}, err
	}
	_, right, err := split(remainder, to-from)
	if err != nil {
		return pieceTree{}, err
	}
	var middle *treapNode
	if inserted != nil && inserted.length > 0 {
		middle = newLeaf(*inserted)
	}
	return pieceTree{root: merge(merge(left, middle), right)}, nil
}

func (t pieceTree) replaceAllWithAdded(offset, length, lineBreaks uint64) pieceTree {
	if length == 0 {
		return pieceTree{}
	}
	return pieceTree{root: newLeaf(addedPiece(offset, length, lineBreaks))}
}

// segment 是一个片段与目标范围的交集：片段内相对起点与长度。
type segment struct {
	piece    piece
	relative uint64
	length   uint64
}

// segments 只展开与 [start, end) 相交的叶片，避免每次窗口读取线性扫描全部片段。
func (t pieceTree) segments(start, end uint64) ([]segment, error) {
	if start > end || end > t.len() {
		return nil, newErrorf("invalid-read-range", "Piece 读取范围越界：%d..%d, len=%d", start, end, t.len())
	}
	var output []segment
	collectSegments(t.root, 0, start, end, &output)
	return output, nil
}

func (t pieceTree) aggregateLineBreaks() *uint64 {
	if t.root == nil {
		return nil
	}
	return t.root.lineBreaks
}

var errSplitInvalid = newError("piece-tree-split-invalid", "Piece Tree 分割位置越界")

func split(root *treapNode, offset uint64) (*treapNode, *treapNode, error) {
	if root == nil {
		if offset == 0 {
			return nil, nil, nil
		}
		return nil, nil, errSplitInvalid
	}
	if offset > root.logicalLen {
		return nil, nil, errSplitInvalid
	}
	leftLen := nodeLen(root.left)
	pieceEnd := leftLen + root.piece.length
	switch {
	case offset < leftLen:
		left, remainder, err := split(root.left, offset)
		if err != nil {
			return nil, nil, err
		}
		return left, makeNode(root.piece, remainder, root.right, root.priority), nil
	case offset > pieceEnd:
		remainder, right, err := split(root.right, offset-pieceEnd)
		if err != nil {
			return nil, nil, err
		}
		return makeNode(root.piece, root.left, remainder, root.priority), right, nil
	case offset == leftLen:
		return root.left, makeNode(root.piece, nil, root.right, root.priority), nil
	case offset == pieceEnd:
		return makeNode(root.piece, root.left, nil, root.priority), root.right, nil
	}
	within := offset - leftLen
	leftPiece := newLeaf(root.piece.slice(0, within))
	rightPiece := newLeaf(root.piece.slice(within, root.piece.length-within))
	return merge(root.left, leftPiece), merge(rightPiece, root.right), nil
}

func merge(left, right *treapNode) *treapNode {
	switch {
	case left == nil:
		return right
	case right == nil:
		return left
	case left.priority <= right.priority:
		return makeNode(left.piece, left.left, merge(left.right, right), left.priority)
	default:
		return makeNode(right.piece, merge(left, right.left), right.right, right.priority)
	}
}

func newLeaf(p piece) *treapNode { return makeNode(p, nil, nil, nextPriority()) }

func makeNode(p piece, left, right *treapNode, priority uint64) *treapNode {
	return &treapNode{
		piece:      p,
		left:       left,
		right:      right,
		priority:   priority,
		logicalLen: nodeLen(left) + p.length + nodeLen(right),
		lineBreaks: combineLineBreaks(left, p.lineBreaks, right),
	}
}

// combineLineBreaks 只有在左右子树与当前片段换行数都已知时才汇总，否则为未知。
func combineLineBreaks(left *treapNode, current *uint64, right *treapNode) *uint64 {
	var total uint64
	if left != nil {
		if left.lineBreaks == nil {
			return nil
		}
		total += *left.lineBreaks
	}
	if current == nil {
		return nil
	}
	total += *current
	if right != nil {
		if right.lineBreaks == nil {
			return nil
		}
		total += *right.lineBreaks
	}
	return &total
}

func collectSegments(node *treapNode, subtreeStart, wantedStart, wantedEnd uint64, output *[]segment) {
	if node == nil {
		return
	}
	subtreeEnd := subtreeStart + node.logicalLen
	if subtreeEnd <= wantedStart || subtreeStart >= wantedEnd {
		return
	}
	leftLen := nodeLen(node.left)
	collectSegments(node.left, subtreeStart, wantedStart, wantedEnd, output)
	pieceStart := subtreeStart + leftLen
	pieceEnd := pieceStart + node.piece.length
	if pieceEnd > wantedStart && pieceStart < wantedEnd {
		overlapStart := max(pieceStart, wantedStart)
		overlapEnd := min(pieceEnd, wantedEnd)
		*output = append(*output, segment{piece: node.piece, relative: overlapStart - pieceStart, length: overlapEnd - overlapStart})
	}
	collectSegments(node.right, pieceEnd, wantedStart, wantedEnd, output)
}

func nodeLen(node *treapNode) uint64 {
	if node == nil {
		return 0
	}
	return node.logicalLen
}

var nextNodeSequence atomic.Uint64

// nextPriority 用 SplitMix64 生成平衡优先级：只服务于树形平衡，序列确定且无需外部 RNG。
func nextPriority() uint64 {
	value := nextNodeSequence.Add(1)
	value += 0x9e3779b97f4a7c15
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func (p piece) String() string {
	kind := "original"
	if p.source == sourceAdded {
		kind = "added"
	}
	return fmt.Sprintf("%s[%d+%d]", kind, p.offset, p.length)
}
