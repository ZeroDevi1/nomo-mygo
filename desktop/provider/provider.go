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
	Close()
}

type Descriptor struct {
	ID, Name, Extension string
	Language            *editor.Language
	BindHints           func(*editor.State) func()
	Open                func(string) (Session, error)
	ExportPDF           func(context.Context, string, io.Reader, string) error
	// Fragment 可选，返回快照中待试排的导言、正文、UTF-8 起点。
	Fragment func(editor.Snapshot, int) (string, string, int, bool)
}
