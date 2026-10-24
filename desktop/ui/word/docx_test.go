package word

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nomo/desktop/ui/mdedit"
)

// unzipDocx 解压 .docx，校验每个 XML 部件格式正确，返回各部件内容。
func unzipDocx(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		parts[f.Name] = string(b)
		if strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".rels") {
			d := xml.NewDecoder(bytes.NewReader(b))
			for {
				if _, err := d.Token(); err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("%s 不是格式正确的 XML：%v", f.Name, err)
				}
			}
		}
	}
	return parts
}

func pngBytes(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBuildDocx(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pic.png"), pngBytes(t, 40, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "---\ntitle: x\n---\n# 标题 A\n\n<!-- toc -->\n- [标题 A](#标题-a)\n<!-- /toc -->\n\n" +
		"正文 **粗** *斜* ~~删~~ `code` H~2~O E=mc^2^ <u>下划线</u> <span style=\"color:#ff0000\">红</span> [链接](https://example.com) 脚注[^1]。\n\n" +
		"行内公式 $a^2$ 与图片 ![图](pic.png)\n\n" +
		"$$\nx=1\n$$\n\n```mermaid\nflowchart TD\n  A-->B\n```\n\n```go\nfunc main() {}\n```\n\n" +
		"> [!TIP] 小技巧\n> 提示内容\n\n> 普通引用\n\n" +
		"1. 一\n2. 二\n   - 子项\n\n3) 再起\n\n- [ ] 待办\n- [x] 完成\n\n" +
		"| 左 | 中 | 右 |\n| :-- | :-: | --: |\n| 1 | 2 | 3 |\n| 4 |\n\n---\n\n" +
		"<div align=\"center\"><svg width=\"20\" height=\"10\"><rect width=\"20\" height=\"10\" fill=\"red\"/></svg></div>\n\n" +
		"<table><tr><td>原样</td></tr></table>\n\n[^1]: 脚注内容 **加粗**。\n"
	var reqs []mdedit.RenderRequest
	render := func(rs []mdedit.RenderRequest) []mdedit.RenderResult {
		reqs = append(reqs, rs...)
		out := make([]mdedit.RenderResult, len(rs))
		for k := range rs {
			img := image.NewRGBA(image.Rect(0, 0, 60, 30))
			img.Set(1, 1, color.Black)
			out[k] = mdedit.RenderResult{Img: img, W: 30, H: 15, Depth: 4}
		}
		return out
	}
	data, warnings, err := Build(src, Options{Title: "测试", BaseDir: dir, Render: render})
	if err != nil {
		t.Fatal(err)
	}
	parts := unzipDocx(t, data)
	doc := parts["word/document.xml"]
	checks := map[string]string{
		"标题样式":     `<w:pStyle w:val="Heading1"/>`,
		"书签":       `w:name="nomo_h1"`,
		"目录内部链接":   `<w:hyperlink w:anchor="nomo_h1"`,
		"外部链接":     `<w:hyperlink r:id="`,
		"粗体":       `<w:b/>`,
		"删除线":      `<w:strike/>`,
		"下标":       `<w:vertAlign w:val="subscript"/>`,
		"上标":       `<w:vertAlign w:val="superscript"/>`,
		"下划线":      `<w:u w:val="single"/>`,
		"颜色":       `<w:color w:val="FF0000"/>`,
		"行内代码":     `<w:rStyle w:val="CodeChar"/>`,
		"代码块":      `<w:pStyle w:val="Code"/>`,
		"编号":       `<w:numId w:val="2"/>`,
		"第二个有序列表":  `<w:numId w:val="3"/>`,
		"无序子项":     `<w:ilvl w:val="1"/><w:numId w:val="1"/>`,
		"任务框":      "☐ ",
		"已完成任务":    "☑ ",
		"表格":       `<w:tblHeader/>`,
		"右对齐":      `<w:jc w:val="right"/>`,
		"提示块标题":    "小技巧",
		"提示块底色":    `w:fill="DAFBE1"`,
		"图片":       `<a:blip r:embed="`,
		"行内公式下沉":   `<w:position w:val="-6"/>`,
		"脚注引用":     `<w:footnoteReference w:id="1"/>`,
		"分割线":      `<w:bottom w:val="single" w:sz="6"`,
		"未支持 HTML": "&lt;table&gt;",
	}
	for name, want := range checks {
		if !strings.Contains(doc, want) {
			t.Errorf("缺少%s：%s", name, want)
		}
	}
	if strings.Contains(doc, "title: x") || strings.Contains(doc, "[!TIP]") || strings.Contains(doc, "toc") {
		t.Error("front matter、提示块标记或目录注释不应进入正文")
	}
	if !strings.Contains(parts["word/footnotes.xml"], "脚注内容") {
		t.Error("脚注内容缺失")
	}
	if n := strings.Count(parts["word/numbering.xml"], `<w:num w:numId=`); n != 3 {
		t.Errorf("编号实例 %d 个，应为 3（无序 1 + 有序 2）", n)
	}
	if !strings.Contains(parts["word/numbering.xml"], `<w:lvlOverride w:ilvl="0"><w:startOverride w:val="3"/>`) {
		t.Error("“3) 再起”应从 3 开始编号")
	}
	media := 0
	for name := range parts {
		if strings.HasPrefix(name, "word/media/") {
			media++
		}
	}
	// 本地图片、SVG 与渲染结果（公式 2 张、图表 1 张内容相同，去重为 1 张）。
	if media != 3 {
		t.Errorf("媒体 %d 个", media)
	}
	kinds := map[string]bool{}
	for _, r := range reqs {
		kinds[r.Kind] = true
	}
	if !kinds[mdedit.RenderMathInline] || !kinds[mdedit.RenderMathDisplay] || !kinds[mdedit.RenderMermaid] {
		t.Errorf("渲染请求 %+v", reqs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "wordExportHtmlWarning") {
		t.Errorf("降级说明 %q", warnings)
	}
	if !strings.Contains(parts["docProps/core.xml"], "<dc:title>测试</dc:title>") {
		t.Error("文档标题")
	}
}

func TestBuildDocxFallbacks(t *testing.T) {
	// 没有渲染器、相对图片无目录、缺少脚注定义：保留源码或替代文字并提示。
	text := func(key string, args ...string) string { return key + ":" + strings.Join(args, ",") }
	data, warnings, err := Build("公式 $x$ ![替代](a.png) 引用[^nope]\n\n$$\ny\n$$\n", Options{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	doc := unzipDocx(t, data)["word/document.xml"]
	for _, want := range []string{"$x$", "替代", "[^nope]", ">y<"} {
		if !strings.Contains(doc, want) {
			t.Errorf("缺少回退内容 %q", want)
		}
	}
	// 公式 2 处、图片 1 处；缺少定义的 [^nope] 解析器按普通文字处理，原样保留、不算降级。
	if len(warnings) != 3 {
		t.Errorf("降级说明 %q", warnings)
	}
}

func TestBuildDocxBudget(t *testing.T) {
	dir := t.TempDir()
	big := image.NewGray(image.Rect(0, 0, 5000, 4000)) // 2000 万像素，超过单图上限
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Build("![](big.png)\n", Options{BaseDir: dir}); err != ErrBudget {
		t.Fatalf("应因预算失败：%v", err)
	}
}
