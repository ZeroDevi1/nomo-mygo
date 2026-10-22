// Package textdoc 是 TXT/JSON 分段文档引擎：大文件按窗口读取、Piece Tree 增量编辑、
// 崩溃恢复日志、后台行索引与搜索/替换/格式化任务。
//
// 所有文件、revision 与后台任务语义只通过 Manager 暴露，桥接层与测试不穿透内部结构。
// JSON 字段名与前端 tauriPort.ts 的协议保持一致。
package textdoc

import (
	"errors"
	"fmt"
	"io/fs"
)

// DocumentKind 由扩展名决定：.txt → text，.json → json。
type DocumentKind string

const (
	KindText DocumentKind = "text"
	KindJSON DocumentKind = "json"
)

// TextEncoding 是分段文档的编码；非 UTF-8 内容一律 unsupported 并只读显示。
type TextEncoding string

const (
	EncodingUTF8        TextEncoding = "utf-8"
	EncodingUTF8BOM     TextEncoding = "utf-8-bom"
	EncodingUnsupported TextEncoding = "unsupported"
)

// LineEnding 描述文档换行风格；CR-only 与混合换行都归入 mixed。
type LineEnding string

const (
	LineEndingLF    LineEnding = "lf"
	LineEndingCRLF  LineEnding = "crlf"
	LineEndingMixed LineEnding = "mixed"
)

// OpenResult 是打开或重载会话的结果。
type OpenResult struct {
	SessionID         string       `json:"sessionId"`
	Revision          uint64       `json:"revision"`
	PersistedRevision uint64       `json:"persistedRevision"`
	DocumentKind      DocumentKind `json:"documentKind"`
	Encoding          TextEncoding `json:"encoding"`
	LineEnding        LineEnding   `json:"lineEnding"`
	ByteLength        uint64       `json:"byteLength"`
	// FilesystemReadonly 只反映源文件权限，不含 baseline/编码校验期间的临时编辑门禁。
	FilesystemReadonly bool   `json:"filesystemReadonly"`
	Readonly           bool   `json:"readonly"`
	FirstWindow        Window `json:"firstWindow"`
	// RecoveryConflictPath 是原文件基线变化时物化出的恢复文档路径；补丁不会自动套到新基线。
	RecoveryConflictPath *string `json:"recoveryConflictPath,omitzero"`
}

// Window 是一段按 UTF-8/CRLF 边界对齐的正文窗口。
type Window struct {
	RequestID           *uint64 `json:"requestId,omitzero"`
	Revision            uint64  `json:"revision"`
	StartByte           uint64  `json:"startByte"`
	EndByte             uint64  `json:"endByte"`
	StartLine           uint64  `json:"startLine"`
	Text                string  `json:"text"`
	LeadingPartialLine  bool    `json:"leadingPartialLine"`
	TrailingPartialLine bool    `json:"trailingPartialLine"`
	LongLine            bool    `json:"longLine"`
	IndexProgress       float64 `json:"indexProgress"`
	// UTF16ByteOffsets 仅在有损解码时给出：长度恒为 UTF-16 码元数 + 1，映射回原始字节偏移。
	UTF16ByteOffsets []uint32          `json:"utf16ByteOffsets,omitzero"`
	JSONLexicalState *JSONLexicalState `json:"jsonLexicalState,omitzero"`
}

// JSONLexicalMode 是 JSON 词法扫描是否位于字符串内部。
type JSONLexicalMode string

const (
	JSONModeDefault JSONLexicalMode = "default"
	JSONModeString  JSONLexicalMode = "string"
)

// JSONLexicalState 让前端高亮器从窗口起点之前的正确词法状态继续。
type JSONLexicalState struct {
	Mode    JSONLexicalMode `json:"mode"`
	Escaped bool            `json:"escaped"`
}

func defaultJSONState() JSONLexicalState { return JSONLexicalState{Mode: JSONModeDefault} }

// Edit 是基于某个 revision 字节坐标的一次替换。
type Edit struct {
	FromByte     uint64 `json:"fromByte"`
	ToByte       uint64 `json:"toByte"`
	InsertedText string `json:"insertedText"`
}

// EditBatch 是一次原子提交的编辑集合，坐标都相对 BaseRevision。
type EditBatch struct {
	SessionID    string `json:"sessionId"`
	BaseRevision uint64 `json:"baseRevision"`
	Edits        []Edit `json:"edits"`
}

type ApplyResult struct {
	Revision            uint64 `json:"revision"`
	PersistedRevision   uint64 `json:"persistedRevision"`
	Dirty               bool   `json:"dirty"`
	InvalidatedFromByte uint64 `json:"invalidatedFromByte"`
	InvalidatedToByte   uint64 `json:"invalidatedToByte"`
}

type HistoryResult struct {
	Changed           bool   `json:"changed"`
	Revision          uint64 `json:"revision"`
	PersistedRevision uint64 `json:"persistedRevision"`
	ByteLength        uint64 `json:"byteLength"`
	Dirty             bool   `json:"dirty"`
	CanUndo           bool   `json:"canUndo"`
	CanRedo           bool   `json:"canRedo"`
}

type FlushResult struct {
	Revision uint64 `json:"revision"`
}

type SaveResult struct {
	SessionID         string `json:"sessionId"`
	SavedRevision     uint64 `json:"savedRevision"`
	CurrentRevision   uint64 `json:"currentRevision"`
	PersistedRevision uint64 `json:"persistedRevision"`
	Dirty             bool   `json:"dirty"`
	// FilesystemReadonly 是保存目标的权限只读状态，与会话临时编辑门禁分开上报。
	FilesystemReadonly bool  `json:"filesystemReadonly"`
	Readonly           bool  `json:"readonly"`
	ModifiedAt         int64 `json:"modifiedAt"`
}

// SearchDirection 决定搜索分页相对锚点的方向。
type SearchDirection string

const (
	// SearchForward 返回锚点之后的下一页，当前项为页首。
	SearchForward SearchDirection = "forward"
	// SearchBackward 返回锚点之前的最近一页，当前项为最接近锚点的前一条。
	SearchBackward SearchDirection = "backward"
)

// TaskKind 是后台任务类型。
type TaskKind string

const (
	TaskSearch        TaskKind = "search"
	TaskReplaceAll    TaskKind = "replace-all"
	TaskSelectAllCopy TaskKind = "select-all-copy"
	TaskJSONValidate  TaskKind = "json-validate"
	TaskJSONFormat    TaskKind = "json-format"
)

// Task 是前端提交的后台任务；Type 决定其余字段的含义。
type Task struct {
	Type          TaskKind `json:"type"`
	Query         string   `json:"query"`
	Replacement   string   `json:"replacement"`
	CaseSensitive bool     `json:"caseSensitive"`
	WholeWord     bool     `json:"wholeWord"`
	WrapAround    bool     `json:"wrapAround"`
	// AnchorByte 只参与筛选结果窗口，全文匹配总数仍按完整快照统计。
	AnchorByte *uint64          `json:"anchorByte"`
	Direction  *SearchDirection `json:"direction"`
}

func (t Task) validKind() bool {
	switch t.Type {
	case TaskSearch, TaskReplaceAll, TaskSelectAllCopy, TaskJSONValidate, TaskJSONFormat:
		return true
	}
	return false
}

func (t Task) writes() bool { return t.Type == TaskReplaceAll || t.Type == TaskJSONFormat }

type StartTaskRequest struct {
	SessionID    string `json:"sessionId"`
	BaseRevision uint64 `json:"baseRevision"`
	Task         Task   `json:"task"`
}

type TaskState string

const (
	TaskRunning   TaskState = "running"
	TaskCompleted TaskState = "completed"
	TaskCancelled TaskState = "cancelled"
	TaskConflict  TaskState = "conflict"
	TaskFailed    TaskState = "failed"
)

type StartTaskResult struct {
	TaskID string `json:"taskId"`
}

type CancelTaskResult struct {
	TaskID    string `json:"taskId"`
	Cancelled bool   `json:"cancelled"`
}

type ExternalChangeKind string

const (
	ExternalNone     ExternalChangeKind = "none"
	ExternalModified ExternalChangeKind = "modified"
	ExternalDeleted  ExternalChangeKind = "deleted"
)

type ExternalChangeResult struct {
	SessionID string             `json:"sessionId"`
	Revision  uint64             `json:"revision"`
	Change    ExternalChangeKind `json:"change"`
	// ChangeToken 对同一外部身份稳定、真实身份变化即改变；前端据此抑制“忽略”后的重复提示。
	ChangeToken    string `json:"changeToken"`
	ModifiedAt     int64  `json:"modifiedAt"`
	Dirty          bool   `json:"dirty"`
	SaveInProgress bool   `json:"saveInProgress"`
}

type SessionStatus struct {
	SessionID         string       `json:"sessionId"`
	Revision          uint64       `json:"revision"`
	PersistedRevision uint64       `json:"persistedRevision"`
	ByteLength        uint64       `json:"byteLength"`
	IndexedBytes      uint64       `json:"indexedBytes"`
	TotalBytes        uint64       `json:"totalBytes"`
	EstimatedLines    uint64       `json:"estimatedLines"`
	Completed         bool         `json:"completed"`
	Encoding          TextEncoding `json:"encoding"`
	LineEnding        LineEnding   `json:"lineEnding"`
	// FilesystemReadonly 不包含后台准备阶段的临时只读状态。
	FilesystemReadonly bool    `json:"filesystemReadonly"`
	Readonly           bool    `json:"readonly"`
	BaselineError      *string `json:"baselineError,omitzero"`
	CanUndo            bool    `json:"canUndo"`
	CanRedo            bool    `json:"canRedo"`
}

// IndexProgressEvent 通过 nomo://segmented-index-progress 推送给打开会话的窗口。
type IndexProgressEvent struct {
	SessionID      string        `json:"sessionId"`
	TaskID         string        `json:"taskId"`
	RequestID      string        `json:"requestId"`
	Revision       uint64        `json:"revision"`
	IndexedBytes   uint64        `json:"indexedBytes"`
	TotalBytes     uint64        `json:"totalBytes"`
	EstimatedLines uint64        `json:"estimatedLines"`
	Completed      bool          `json:"completed"`
	Encoding       *TextEncoding `json:"encoding,omitzero"`
	LineEnding     *LineEnding   `json:"lineEnding,omitzero"`
	Readonly       *bool         `json:"readonly,omitzero"`
	BaselineError  *string       `json:"baselineError,omitzero"`
}

// TaskProgressEvent 通过 nomo://segmented-task-progress 推送给发起任务的窗口。
type TaskProgressEvent struct {
	SessionID         string      `json:"sessionId"`
	TaskID            string      `json:"taskId"`
	BaseRevision      uint64      `json:"baseRevision"`
	Revision          uint64      `json:"revision"`
	RequestID         string      `json:"requestId"`
	Kind              TaskKind    `json:"kind"`
	State             TaskState   `json:"state"`
	ProcessedBytes    uint64      `json:"processedBytes"`
	TotalBytes        uint64      `json:"totalBytes"`
	MatchCount        uint64      `json:"matchCount"`
	CurrentMatch      *TaskMatch  `json:"currentMatch,omitzero"`
	NearbyMatches     []TaskMatch `json:"nearbyMatches,omitempty"`
	ResultRevision    *uint64     `json:"resultRevision,omitzero"`
	ResultByteLength  *uint64     `json:"resultByteLength,omitzero"`
	PersistedRevision *uint64     `json:"persistedRevision,omitzero"`
	Dirty             *bool       `json:"dirty,omitzero"`
	OutputPath        *string     `json:"outputPath,omitzero"`
	Message           *string     `json:"message,omitzero"`
}

type TaskMatch struct {
	StartByte uint64 `json:"startByte"`
	EndByte   uint64 `json:"endByte"`
}

// Event 是推给前端的分段事件；Index 与 Task 恰有一个非空。
type Event struct {
	Index *IndexProgressEvent
	Task  *TaskProgressEvent
}

// EventSink 接收会话事件；可能在任意后台 goroutine 上调用，实现必须并发安全且不阻塞太久。
type EventSink func(Event)

// Error 是返回给前端的结构化错误，前端按 Code 分支处理。
type Error struct {
	Code           string  `json:"code"`
	Message        string  `json:"message"`
	ActualRevision *uint64 `json:"actualRevision,omitzero"`
	RecoveryPath   *string `json:"recoveryPath,omitzero"`
}

func (e *Error) Error() string { return e.Message }

func newError(code, message string) *Error { return &Error{Code: code, Message: message} }

func newErrorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func revisionConflict(expected, actual uint64) *Error {
	return &Error{
		Code:           "revision-conflict",
		Message:        fmt.Sprintf("文档 revision 已变化：expected=%d, actual=%d", expected, actual),
		ActualRevision: &actual,
	}
}

func sourceMissingRecovered(path string) *Error {
	return &Error{
		Code:         "source-missing-recovered",
		Message:      "源文件不存在，未保存内容已恢复到 " + path,
		RecoveryPath: &path,
	}
}

// asError 把任意错误规整为结构化错误（非 *Error 的底层错误归为 io-error）；nil 原样返回 nil 接口，
// 避免返回“持有 nil 指针的非 nil error”。
func asError(err error) error {
	if err == nil {
		return nil
	}
	return structured(err)
}

// structured 返回 err 对应的 *Error；err 不得为 nil。
func structured(err error) *Error {
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	return newError("io-error", err.Error())
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

func ptr[T any](value T) *T { return &value }
