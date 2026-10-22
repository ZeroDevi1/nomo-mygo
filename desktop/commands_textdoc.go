package main

import "nomo/desktop/internal/textdoc"

// 分段文档（TXT/JSON 大文件）命令：参数原样转交 textdoc.Manager，进度事件广播给所有页面，
// 前端按 sessionId / taskId 过滤（与 Tauri window.emit 的投递范围一致）。

const (
	segmentedIndexProgressEvent = "nomo://segmented-index-progress"
	segmentedTaskProgressEvent  = "nomo://segmented-task-progress"
)

func segmentedEventSink(event textdoc.Event) {
	switch {
	case event.Index != nil:
		broadcast(segmentedIndexProgressEvent, event.Index)
	case event.Task != nil:
		broadcast(segmentedTaskProgressEvent, event.Task)
	}
}

func init() {
	register("open_segmented_document", func(_ *commandCall, args struct {
		Path string `json:"path"`
	}) (*textdoc.OpenResult, error) {
		return app.documents.OpenDocument(args.Path, segmentedEventSink)
	})
	register("reload_segmented_session", func(_ *commandCall, args struct {
		SessionID string `json:"sessionId"`
	}) (*textdoc.OpenResult, error) {
		return app.documents.ReloadSession(args.SessionID, segmentedEventSink)
	})
	register("read_segmented_window", func(_ *commandCall, args struct {
		SessionID   string `json:"sessionId"`
		Revision    uint64 `json:"revision"`
		StartByte   uint64 `json:"startByte"`
		TargetBytes int    `json:"targetBytes"`
		RequestID   uint64 `json:"requestId"`
	}) (*textdoc.Window, error) {
		return app.documents.ReadWindow(args.SessionID, args.Revision, args.StartByte, args.TargetBytes, &args.RequestID)
	})
	register("apply_segmented_edits", func(_ *commandCall, args struct {
		Batch textdoc.EditBatch `json:"batch"`
	}) (*textdoc.ApplyResult, error) {
		return app.documents.ApplyEdits(args.Batch)
	})
	register("undo_segmented_revision", func(_ *commandCall, args struct {
		SessionID    string `json:"sessionId"`
		BaseRevision uint64 `json:"baseRevision"`
	}) (*textdoc.HistoryResult, error) {
		return app.documents.UndoRevision(args.SessionID, args.BaseRevision)
	})
	register("redo_segmented_revision", func(_ *commandCall, args struct {
		SessionID    string `json:"sessionId"`
		BaseRevision uint64 `json:"baseRevision"`
	}) (*textdoc.HistoryResult, error) {
		return app.documents.RedoRevision(args.SessionID, args.BaseRevision)
	})
	register("flush_segmented_journal", func(_ *commandCall, args struct {
		SessionID string `json:"sessionId"`
		Revision  uint64 `json:"revision"`
	}) (*textdoc.FlushResult, error) {
		return app.documents.FlushJournal(args.SessionID, args.Revision)
	})
	register("save_segmented_revision", func(_ *commandCall, args struct {
		SessionID         string  `json:"sessionId"`
		Revision          uint64  `json:"revision"`
		TargetPath        *string `json:"targetPath"`
		OverwriteExternal *bool   `json:"overwriteExternal"`
	}) (*textdoc.SaveResult, error) {
		target := ""
		if args.TargetPath != nil {
			target = *args.TargetPath
		}
		return app.documents.SaveRevision(args.SessionID, args.Revision, target, args.OverwriteExternal != nil && *args.OverwriteExternal)
	})
	register("start_segmented_task", func(_ *commandCall, args struct {
		Request textdoc.StartTaskRequest `json:"request"`
	}) (*textdoc.StartTaskResult, error) {
		return app.documents.StartTask(args.Request, segmentedEventSink)
	})
	register("cancel_segmented_task", func(_ *commandCall, args struct {
		TaskID string `json:"taskId"`
	}) (*textdoc.CancelTaskResult, error) {
		return app.documents.CancelTask(args.TaskID), nil
	})
	register("close_segmented_session", func(_ *commandCall, args struct {
		SessionID      string `json:"sessionId"`
		DiscardChanges *bool  `json:"discardChanges"`
	}) (any, error) {
		return nil, app.documents.CloseSession(args.SessionID, args.DiscardChanges != nil && *args.DiscardChanges)
	})
	register("check_segmented_external_change", func(_ *commandCall, args struct {
		SessionID string `json:"sessionId"`
	}) (*textdoc.ExternalChangeResult, error) {
		return app.documents.CheckExternalChange(args.SessionID)
	})
	register("get_segmented_session_status", func(_ *commandCall, args struct {
		SessionID string `json:"sessionId"`
	}) (*textdoc.SessionStatus, error) {
		return app.documents.SessionStatus(args.SessionID)
	})
}
