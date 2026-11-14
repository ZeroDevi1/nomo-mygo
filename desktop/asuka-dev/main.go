package main

import (
	"io"
	"time"

	"github.com/egoist/asuka/colors"
	"github.com/egoist/asuka/fonts"
	"github.com/egoist/asuka/sdk/document"
	"github.com/egoist/asuka/sdk/markdown"
	"github.com/egoist/asuka/signal"
	"github.com/egoist/asuka/ui"

	"nomo/desktop/internal/textcodec"
)

type source struct {
	Text     string
	Encoding textcodec.Encoding
}

func main() {
	newDocuments().Run()
}

func newDocuments() *document.Documents[source] {
	return document.New(document.Options[source]{
		Types: []document.Type{{
			Name: "Markdown", Extensions: []string{"md", "markdown"}, MIMETypes: []string{"text/markdown"},
		}},
		New: func() source {
			return source{Encoding: textcodec.EncodingUTF8}
		},
		Read:      readSource,
		Write:     writeSource,
		Equal:     func(a, b source) bool { return a == b },
		Autosave:  document.AutosaveOff,
		AskOnQuit: true,
		Width:     920,
		Height:    620,
		MinWidth:  460,
		MinHeight: 300,
		View:      editorView,
	})
}

func readSource(r io.Reader) (source, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return source{}, err
	}
	content, encoding, err := textcodec.Decode(data)
	return source{Text: content, Encoding: encoding}, err
}

func writeSource(w io.Writer, content source) error {
	encoding := content.Encoding
	if encoding == textcodec.EncodingGBK {
		// Asuka 的原地保存回退会先截断文件。GBK 无法编码的字符改用 UTF-8 保存。
		encoding = textcodec.EncodingUTF8
	}
	data, err := textcodec.Encode(content.Text, encoding)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func editorView(doc *document.Document[source]) ui.Node {
	content := doc.Content()
	synced := content.Peek().Text
	text := signal.New(synced)

	// 原生文本框绑定字符串。外部重载时更新控件，编辑时保留文档的磁盘编码。
	signal.Effect(func() {
		file, edited := content.Get(), text.Get()
		if file.Text != synced {
			synced = file.Text
			text.Set(synced)
		} else if edited != synced {
			synced = edited
			file.Text = edited
			doc.Set(file)
		}
	})

	showPreview := signal.New(true)
	env := ui.UseEnvironment()
	state := doc.State()
	return ui.SafeArea(ui.Style{Grow: 1},
		ui.Row(ui.Style{Padding: ui.Symmetric(8, 12), Gap: 8, Align: ui.AlignCenter},
			ui.Text(signal.Func(func() string {
				encoding := content.Get().Encoding
				label := string(encoding)
				if encoding == textcodec.EncodingGBK {
					label = "GBK → UTF-8"
				}
				if state.Get().Edited {
					return "未保存 · " + label
				}
				return label
			}), ui.Style{Grow: 1, Color: colors.SecondaryLabel}),
			ui.Button(ui.ButtonProps{
				Title: signal.Func(func() string {
					if showPreview.Get() {
						return "隐藏预览"
					}
					return "显示预览"
				}),
				OnPress: func() { showPreview.Set(!showPreview.Peek()) },
			}),
		),
		ui.View(ui.ViewProps{Style: ui.StyleFunc(func() ui.Style {
			style := ui.Style{Grow: 1}
			if env.Get().Width >= 640 {
				style.Direction = ui.RowDir
			}
			return style
		})},
			ui.TextField(ui.TextFieldProps{
				Value: text, Placeholder: ui.Val("Markdown 源码"), Multiline: true, Plain: true,
				Style: ui.Style{Grow: 1, Basis: ui.Pt(0), MinWidth: ui.Pt(0), MinHeight: ui.Pt(0),
					Font: fonts.Custom("monospace", fonts.Body), Padding: ui.All(16)},
			}),
			ui.Show(showPreview, func() ui.Node { return previewView(text) }),
		),
	)
}

func previewView(text *signal.Signal[string]) ui.Node {
	preview := signal.New(text.Peek())
	// 合并连续输入，关闭预览或文档时取消尚未应用的更新。
	signal.Effect(func() {
		next := text.Get()
		if next == preview.Peek() {
			return
		}
		expired := false
		timer := time.AfterFunc(150*time.Millisecond, func() {
			ui.Post(func() {
				if !expired {
					preview.Set(next)
				}
			})
		})
		signal.OnCleanup(func() {
			expired = true
			timer.Stop()
		})
	})
	return ui.Scroll(ui.ScrollProps{
		Style: ui.Style{Grow: 1, Basis: ui.Pt(0), MinWidth: ui.Pt(0), MinHeight: ui.Pt(0),
			Background: colors.SecondaryBackground},
		ContentStyle: ui.Style{Padding: ui.All(20)},
	}, markdown.View(markdown.Props{Text: preview}))
}
