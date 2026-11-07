// Package provider 定义随应用编译的语言扩展契约。描述符在启动后只读。
package provider

import (
	"context"
	"image"
	"io"

	"github.com/egoist/mygo/ui/editor"
)

type Session interface {
	Compile(context.Context, io.Reader) (int, error)
	Page(context.Context, int) (image.Image, error)
	PageAtSize(context.Context, int, int) (image.Image, error)
	Close()
}

type Descriptor struct {
	ID, Name   string
	Extensions []string // lower-case suffixes without dots
	Language   *editor.Language
	BindHints  func(*editor.State) func()
	Open       func(string) (Session, error)
	ExportPDF  func(context.Context, string, io.Reader, string) error
	// Fragment 可选，返回快照中待试排的导言、正文、UTF-8 起点。
	Fragment func(editor.Snapshot, int) (string, string, int, bool)
	// IncludedFragment extracts a paragraph from a child file without a document preamble.
	IncludedFragment func(editor.Snapshot, int) (string, int, bool)
}
