package mdedit

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/ui/icons"
)

// 文档元数据卡片（原版 v0.6.2 的 FrontMatterCard）：front matter 收起时显示标题（无标题时为“文档元数据”）、状态、
// 创建与更新日期、其余字段数与标签，点击进入原文编辑。字段解析同原版 parseFrontMatterFields：只认 title、created、
// updated、tags、status，其余顶层键计入“更多元数据”；不是简单键值的行不影响卡片，原文保持不变。

// frontMatterFields 是卡片显示的字段。
type frontMatterFields struct {
	title, created, updated, status string
	tags                            []string
	extra                           int // 其余顶层键的个数
}

var (
	fmKeyRe  = regexp.MustCompile(`^([A-Za-z0-9_-]+):(?:\s*(.*))?$`)
	fmListRe = regexp.MustCompile(`^\s*-\s+(.+)$`)
)

// parseFrontMatter 解析 front matter 原文（含首尾 --- 行）的已知字段。
func parseFrontMatter(src string) frontMatterFields {
	var f frontMatterFields
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		lines = lines[1:]
	}
	if n := len(lines); n > 0 && (strings.TrimSpace(lines[n-1]) == "---" || strings.TrimSpace(lines[n-1]) == "...") {
		lines = lines[:n-1]
	}
	seen := map[string]bool{}
	activeList := ""
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if m := fmListRe.FindStringSubmatch(line); m != nil && activeList == "tags" {
			f.tags = append(f.tags, unquoteYAML(strings.TrimSpace(m[1])))
			continue
		}
		m := fmKeyRe.FindStringSubmatch(line)
		if m == nil {
			activeList = ""
			continue
		}
		key, value := m[1], unquoteYAML(strings.TrimSpace(m[2]))
		activeList = ""
		if value == "" {
			activeList = key
		}
		switch key {
		case "title":
			f.title = value
		case "created":
			f.created = value
		case "updated":
			f.updated = value
		case "status":
			f.status = value
		case "tags":
			if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
				for _, t := range strings.Split(value[1:len(value)-1], ",") {
					if t = unquoteYAML(strings.TrimSpace(t)); t != "" {
						f.tags = append(f.tags, t)
					}
				}
			}
		default:
			seen[key] = true
		}
	}
	f.extra = len(seen)
	return f
}

// unquoteYAML 去掉 YAML 标量两端成对的单引号或双引号。
func unquoteYAML(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// 卡片版式：内边距 14/16，图标 18 px，标题 15 px 粗体，其余 12 px。
const (
	fmPadX, fmPadY = 16, 14
	fmIcon         = 18
	fmGap          = 6
)

// frontMatter 排版元数据卡片，返回高度。行按代码框缩进了 codePadX（展开为原文时的内边距），卡片向两侧补回，
// 与其他卡片同宽。
func (c *hpCtx) frontMatter(src string, w float32) float32 {
	e := c.e
	f := parseFrontMatter(src)
	small := ui.Font{Family: e.fontBody, Size: 12 * e.scale}
	ox := float32(-codePadX)
	w += 2 * codePadX
	x := ox + fmPadX + fmIcon + 10
	inner := max(40, ox+w-x-fmPadX)
	y := float32(fmPadY)
	var ops []hpOp

	// 标题行：标题（无标题时为“文档元数据”）与状态标签。
	title := f.title
	if title == "" {
		title = e.labelOr(e.labels.Metadata, "Document metadata")
	}
	tl := ui.ShapeRichTextWith([]ui.Span{{Text: title, Weight: 700}}, ui.Font{Family: e.fontBody, Size: 15 * e.scale}, ui.TextShape{Width: inner})
	tw, th := tl.Size()
	ops = append(ops, hpOp{kind: hpText, rect: ui.Rect{X: x, Y: y}, text: tl, color: e.pal.heading})
	if f.status != "" {
		sl := ui.ShapeRichTextWith([]ui.Span{{Text: f.status, Weight: 600}}, small, ui.TextShape{})
		sw, sh := sl.Size()
		chip := ui.Rect{X: x + min(tw, inner) + 8, Y: y + (th-sh)/2 - 2, W: sw + 12, H: sh + 4}
		ops = append(ops, hpOp{kind: hpFill, rect: chip, color: e.pal.codeBg, radius: chip.H / 2},
			hpOp{kind: hpText, rect: ui.Rect{X: chip.X + 6, Y: chip.Y + 2}, text: sl, color: e.pal.muted})
	}
	y += th

	// 日期与其余字段数。
	var meta []string
	if f.created != "" {
		meta = append(meta, strings.ReplaceAll(e.labelOr(e.labels.MetadataCreated, "Created {date}"), "{date}", f.created))
	}
	if f.updated != "" {
		meta = append(meta, strings.ReplaceAll(e.labelOr(e.labels.MetadataUpdated, "Updated {date}"), "{date}", f.updated))
	}
	if f.extra > 0 {
		meta = append(meta, strings.ReplaceAll(e.labelOr(e.labels.MetadataMore, "{count} more fields"), "{count}", strconv.Itoa(f.extra)))
	}
	if len(meta) > 0 {
		ml := ui.ShapeRichTextWith([]ui.Span{{Text: strings.Join(meta, "  ·  ")}}, small, ui.TextShape{Width: inner})
		_, mh := ml.Size()
		y += fmGap
		ops = append(ops, hpOp{kind: hpText, rect: ui.Rect{X: x, Y: y}, text: ml, color: e.pal.muted})
		y += mh
	}

	// 标签：逐个圆角标签，超出宽度换行。
	if len(f.tags) > 0 {
		y += fmGap + 2
		cx := x
		var rowH float32
		for _, tag := range f.tags {
			l := ui.ShapeRichTextWith([]ui.Span{{Text: tag}}, small, ui.TextShape{})
			lw, lh := l.Size()
			cw, ch := lw+14, lh+6
			if cx > x && cx+cw > x+inner {
				cx, y = x, y+rowH+6
			}
			ops = append(ops, hpOp{kind: hpFill, rect: ui.Rect{X: cx, Y: y, W: cw, H: ch}, color: e.pal.codeBg, radius: ch / 2},
				hpOp{kind: hpText, rect: ui.Rect{X: cx + 7, Y: y + 3}, text: l, color: e.pal.text})
			cx += cw + 6
			rowH = max(rowH, ch)
		}
		y += rowH
	}
	h := y + fmPadY
	c.lay.ops = append(c.lay.ops,
		hpOp{kind: hpFill, rect: ui.Rect{X: ox, W: w, H: h}, color: e.pal.surface, radius: 10},
		hpOp{kind: hpStroke, rect: ui.Rect{X: ox, W: w, H: h}, color: e.pal.border, radius: 10},
		hpOp{kind: hpIcon, rect: ui.Rect{X: ox + fmPadX, Y: fmPadY + (th-fmIcon)/2, W: fmIcon, H: fmIcon}, icon: icons.Get("file-text"), color: e.pal.muted})
	c.lay.ops = append(c.lay.ops, ops...)
	c.lay.bytes += 512 + len(src)*2
	return h
}

// labelOr 返回界面文字，未设置时用英文默认值。
func (e *Editor) labelOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
