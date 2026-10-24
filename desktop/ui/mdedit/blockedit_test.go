package mdedit

import (
	"strings"
	"testing"
)

func TestTableCommands(t *testing.T) {
	const doc = "intro\n\n| A | B \\| x | `c\\|d` |\n| :-- | :-: | --- |\n| 1 | 2 | 3 |\n| 4 | 5 | 6 |\n\nend\n"
	cases := []struct {
		op, at, want string
	}{
		{"addRowAfter", "2", "| A | B \\| x | `c\\|d` |\n| :--- | :---: | --- |\n| 1 | 2 | 3 |\n|  |  |  |\n| 4 | 5 | 6 |"},
		{"addRowBefore", "A", "| A | B \\| x | `c\\|d` |\n| :--- | :---: | --- |\n|  |  |  |\n| 1 | 2 | 3 |\n| 4 | 5 | 6 |"},
		{"deleteRow", "4", "| A | B \\| x | `c\\|d` |\n| :--- | :---: | --- |\n| 1 | 2 | 3 |"},
		{"addColumnBefore", "5", "| A |  | B \\| x | `c\\|d` |\n| :--- | --- | :---: | --- |\n| 1 |  | 2 | 3 |\n| 4 |  | 5 | 6 |"},
		{"deleteColumn", "3", "| A | B \\| x |\n| :--- | :---: |\n| 1 | 2 |\n| 4 | 5 |"},
		{"alignRight", "1", "| A | B \\| x | `c\\|d` |\n| ---: | :---: | --- |\n| 1 | 2 | 3 |\n| 4 | 5 | 6 |"},
	}
	for _, tc := range cases {
		ed := NewEditor(NewDocument(doc))
		pos := strings.Index(doc[7:], tc.at) + 7
		if !ed.TableCommand(pos, tc.op) {
			t.Fatalf("%s：命令未执行", tc.op)
		}
		got := ed.Doc.Source()
		if want := "intro\n\n" + tc.want + "\n\nend\n"; got != want {
			t.Errorf("%s：\n got %q\nwant %q", tc.op, got, want)
		}
	}
	// 表头行不能删除。
	ed := NewEditor(NewDocument(doc))
	if ed.TableCommand(strings.Index(doc, "A"), "deleteRow") {
		t.Fatal("表头行被删除")
	}
	if !ed.TableCommand(strings.Index(doc, "A"), "deleteTable") || ed.Doc.Source() != "intro\n\nend\n" {
		t.Fatalf("删除表格后 %q", ed.Doc.Source())
	}
	ed.Run("undo")
	if ed.Doc.Source() != doc {
		t.Fatalf("撤销后 %q", ed.Doc.Source())
	}
}

func TestCodeBlockCommands(t *testing.T) {
	const doc = "a\n\n```js title=x\nconst a = 1\nlet b\n```\n\n$$\nE=mc^2\n$$\n"
	ed := NewEditor(NewDocument(doc))
	at := strings.Index(doc, "const")
	if text, ok := ed.BlockContent(at); !ok || text != "const a = 1\nlet b" {
		t.Fatalf("代码内容 %q", text)
	}
	if text, ok := ed.BlockContent(strings.Index(doc, "E=")); !ok || text != "E=mc^2" {
		t.Fatalf("公式内容 %q", text)
	}
	if !ed.SetCodeLanguage(at, "ts") || !strings.Contains(ed.Doc.Source(), "```ts title=x\n") {
		t.Fatalf("改语言后 %q", ed.Doc.Source())
	}
	ed.SetCodeLanguage(at, "")
	if !strings.Contains(ed.Doc.Source(), "``` title=x\n") {
		t.Fatalf("去掉语言后 %q", ed.Doc.Source())
	}
	if !ed.DeleteBlock(strings.Index(ed.Doc.Source(), "E=")) || ed.Doc.Source() != "a\n\n``` title=x\nconst a = 1\nlet b\n```\n" {
		t.Fatalf("删除公式块后 %q", ed.Doc.Source())
	}
}

// TestTableCommandCaretContext：插入列后光标落在新的空单元格中，CaretContext 不越界（曾导致表情建议崩溃）。
func TestTableCommandCaretContext(t *testing.T) {
	doc := "intro\n\n| 名称 | 数量 |\n| :-- | --: |\n| 苹果 | 3 |\n"
	ed, tt := newTester(t, doc, 800, 600)
	tt.Frame()
	ed.TableCommand(strings.Index(doc, "苹果")+3, "addColumnAfter")
	tt.Frame()
	before, _, ok := ed.CaretContext(64)
	if ok && before != "" {
		t.Fatalf("空单元格光标前文字 %q", before)
	}
}
