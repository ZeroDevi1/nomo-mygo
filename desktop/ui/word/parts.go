package word

import (
	"fmt"
	"strings"
	"time"
)

// 固定的包部件：内容类型、关系、样式、编号、设置与文档属性。版式与原版 Word 导出一致：A4 纵向、页边距 20 mm、
// 正文 11 pt、1.5 倍行距；中文宋体、西文 Calibri、代码 Consolas 9 pt（字体不嵌入，缺失时由 Word/WPS 替换）。

const (
	nsW   = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsR   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsWP  = "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
	nsA   = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsPic = "http://schemas.openxmlformats.org/drawingml/2006/picture"
	relNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

	xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

	// 版心（A4 宽 210 mm 减左右各 20 mm）：twip（1/1440 英寸）与 EMU（1/914400 英寸）。
	contentTwips = 9638
	contentEMU   = 6120000
	emuPerPx     = 9525 // 96 dpi 下一个 CSS 像素
)

func contentTypes() string {
	return xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Default Extension="png" ContentType="image/png"/>` +
		`<Default Extension="jpeg" ContentType="image/jpeg"/>` +
		`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
		`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
		`<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>` +
		`<Override PartName="/word/footnotes.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footnotes+xml"/>` +
		`<Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
		`</Types>`
}

func packageRels() string {
	return xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="` + relNS + `/officeDocument" Target="word/document.xml"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
		`<Relationship Id="rId3" Type="` + relNS + `/extended-properties" Target="docProps/app.xml"/>` +
		`</Relationships>`
}

// rel 是 document.xml 的一条关系；external 为外部链接（TargetMode="External"）。
type rel struct {
	id, typ, target string
	external        bool
}

func documentRels(rels []rel) string {
	var b strings.Builder
	b.WriteString(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, r := range rels {
		mode := ""
		if r.external {
			mode = ` TargetMode="External"`
		}
		fmt.Fprintf(&b, `<Relationship Id="%s" Type="%s/%s" Target="%s"%s/>`, r.id, relNS, r.typ, esc(r.target), mode)
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

func coreProps(title string, now time.Time) string {
	ts := now.UTC().Format("2006-01-02T15:04:05Z")
	return xmlHeader + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" ` +
		`xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:title>` + esc(title) + `</dc:title><dc:creator>Nomo</dc:creator>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + ts + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + ts + `</dcterms:modified>` +
		`</cp:coreProperties>`
}

func appProps() string {
	return xmlHeader + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>Nomo</Application></Properties>`
}

func settings() string {
	return xmlHeader + `<w:settings xmlns:w="` + nsW + `"><w:defaultTabStop w:val="420"/>` +
		`<w:footnotePr><w:footnote w:id="-1"/><w:footnote w:id="0"/></w:footnotePr>` +
		`<w:compat><w:compatSetting w:name="compatibilityMode" w:uri="http://schemas.microsoft.com/office/word" w:val="15"/></w:compat>` +
		`</w:settings>`
}

// headingSizes 是 Heading 1–6 的字号（半磅）。
var headingSizes = [6]int{36, 32, 28, 26, 24, 22}

func styles() string {
	var b strings.Builder
	b.WriteString(xmlHeader + `<w:styles xmlns:w="` + nsW + `">`)
	b.WriteString(`<w:docDefaults><w:rPrDefault><w:rPr>` +
		`<w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="宋体" w:cs="Calibri"/>` +
		`<w:sz w:val="22"/><w:szCs w:val="22"/><w:lang w:val="en-US" w:eastAsia="zh-CN"/></w:rPr></w:rPrDefault>` +
		`<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="360" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>`)
	b.WriteString(`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>`)
	for i, sz := range headingSizes {
		fmt.Fprintf(&b, `<w:style w:type="paragraph" w:styleId="Heading%d"><w:name w:val="heading %d"/><w:basedOn w:val="Normal"/>`+
			`<w:next w:val="Normal"/><w:uiPriority w:val="9"/><w:qFormat/><w:pPr><w:keepNext/><w:keepLines/>`+
			`<w:spacing w:before="%d" w:after="120"/><w:outlineLvl w:val="%d"/></w:pPr>`+
			`<w:rPr><w:b/><w:bCs/><w:sz w:val="%d"/><w:szCs w:val="%d"/></w:rPr></w:style>`, i+1, i+1, 360-i*40, i, sz, sz)
	}
	b.WriteString(`<w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/><w:qFormat/>` +
		`<w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F6F8FA"/><w:spacing w:after="0" w:line="240" w:lineRule="auto"/>` +
		`<w:ind w:left="120" w:right="120"/></w:pPr>` +
		`<w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:eastAsia="宋体" w:cs="Consolas"/><w:sz w:val="18"/><w:szCs w:val="18"/></w:rPr></w:style>`)
	b.WriteString(`<w:style w:type="character" w:styleId="CodeChar"><w:name w:val="Code Char"/>` +
		`<w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:cs="Consolas"/><w:sz w:val="20"/><w:shd w:val="clear" w:color="auto" w:fill="EFF1F3"/></w:rPr></w:style>`)
	b.WriteString(`<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/>` +
		`<w:rPr><w:color w:val="0563C1"/><w:u w:val="single"/></w:rPr></w:style>`)
	b.WriteString(`<w:style w:type="paragraph" w:styleId="FootnoteText"><w:name w:val="footnote text"/><w:basedOn w:val="Normal"/>` +
		`<w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:rPr><w:sz w:val="18"/><w:szCs w:val="18"/></w:rPr></w:style>`)
	b.WriteString(`<w:style w:type="character" w:styleId="FootnoteReference"><w:name w:val="footnote reference"/>` +
		`<w:rPr><w:vertAlign w:val="superscript"/></w:rPr></w:style>`)
	b.WriteString(`<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:tblPr><w:tblBorders>` +
		`<w:top w:val="single" w:sz="4" w:space="0" w:color="D0D7DE"/><w:left w:val="single" w:sz="4" w:space="0" w:color="D0D7DE"/>` +
		`<w:bottom w:val="single" w:sz="4" w:space="0" w:color="D0D7DE"/><w:right w:val="single" w:sz="4" w:space="0" w:color="D0D7DE"/>` +
		`<w:insideH w:val="single" w:sz="4" w:space="0" w:color="D0D7DE"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="D0D7DE"/>` +
		`</w:tblBorders><w:tblCellMar><w:left w:w="100" w:type="dxa"/><w:right w:w="100" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>`)
	b.WriteString(`</w:styles>`)
	return b.String()
}

// bulletChars 是各层无序列表的符号。
var bulletChars = [3]string{"•", "◦", "▪"}

// numbering 生成编号定义：abstractNum 0 为无序列表（numId 1 共用），abstractNum 1 为有序列表，每个有序列表一个
// numId（从 2 开始，按 starts 依次给出起始序号与层级），使各列表从自己的起始数字开始计数。
func numbering(ordered []orderedList) string {
	var b strings.Builder
	b.WriteString(xmlHeader + `<w:numbering xmlns:w="` + nsW + `">`)
	level := func(i int, fmtVal, text string) string {
		return fmt.Sprintf(`<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="%s"/><w:lvlText w:val="%s"/><w:lvlJc w:val="left"/>`+
			`<w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, i, fmtVal, text, 420*(i+1))
	}
	b.WriteString(`<w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="hybridMultilevel"/>`)
	for i := range 9 {
		b.WriteString(level(i, "bullet", bulletChars[i%len(bulletChars)]))
	}
	b.WriteString(`</w:abstractNum><w:abstractNum w:abstractNumId="1"><w:multiLevelType w:val="hybridMultilevel"/>`)
	for i := range 9 {
		b.WriteString(level(i, "decimal", fmt.Sprintf("%%%d.", i+1)))
	}
	b.WriteString(`</w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`)
	for k, o := range ordered {
		fmt.Fprintf(&b, `<w:num w:numId="%d"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="%d"><w:startOverride w:val="%d"/></w:lvlOverride></w:num>`,
			k+2, o.level, max(o.start, 0))
	}
	b.WriteString(`</w:numbering>`)
	return b.String()
}

// orderedList 是一个有序列表的编号实例：level 为层级，start 为起始序号。
type orderedList struct{ level, start int }

func footnotesPart(bodies []string) string {
	var b strings.Builder
	b.WriteString(xmlHeader + `<w:footnotes xmlns:w="` + nsW + `" xmlns:r="` + nsR + `">` +
		`<w:footnote w:type="separator" w:id="-1"><w:p><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:r><w:separator/></w:r></w:p></w:footnote>` +
		`<w:footnote w:type="continuationSeparator" w:id="0"><w:p><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:r><w:continuationSeparator/></w:r></w:p></w:footnote>`)
	for k, body := range bodies {
		fmt.Fprintf(&b, `<w:footnote w:id="%d">%s</w:footnote>`, k+1, body)
	}
	b.WriteString(`</w:footnotes>`)
	return b.String()
}

// esc 转义 XML 文本与属性值，并去掉 XML 1.0 不允许的控制字符。
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r', r == 0xFFFE, r == 0xFFFF:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
