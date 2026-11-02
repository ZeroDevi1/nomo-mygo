package typeset

import (
	"bufio"
	"context"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// 只翻译能独立解释的正文与简单行内公式；未知命令、引用和环境交给完整编译。
// 输出全部是 Typst 字符串和固定函数名，用户源码不能变成 Typst 指令。
func paragraphContent(source string) (string, bool) {
	if len(source) > 8192 || strings.TrimSpace(source) == "" {
		return "", false
	}
	var out, plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out.WriteString(typString(plain.String()))
			out.WriteByte(',')
			plain.Reset()
		}
	}
	src := strings.Join(strings.Fields(source), " ")
	depth := 0
	for i := 0; i < len(src); {
		switch src[i] {
		case '\\':
			flush()
			i++
			start := i
			for i < len(src) && src[i] >= 'a' && src[i] <= 'z' {
				i++
			}
			var fn string
			switch src[start:i] {
			case "textbf":
				fn = "strong"
			case "emph", "textit":
				fn = "emph"
			case "textrm":
				fn = "text"
			default:
				return "", false
			}
			if i >= len(src) || src[i] != '{' || depth >= 12 {
				return "", false
			}
			i++
			depth++
			out.WriteString(fn + "((")
		case '}':
			if depth == 0 {
				return "", false
			}
			i++
			depth--
			flush()
			out.WriteString(").join()),")
		case '$':
			end := strings.IndexByte(src[i+1:], '$')
			if end <= 0 {
				return "", false
			}
			math, ok := inlineMath(src[i+1 : i+1+end])
			if !ok {
				return "", false
			}
			flush()
			out.WriteString("$" + math + "$,")
			i += end + 2
		case '{', '#', '%', '~', '_', '&', '^':
			return "", false
		default:
			plain.WriteByte(src[i])
			i++
		}
	}
	if depth != 0 {
		return "", false
	}
	flush()
	// TeX 的空白、连字与断行和 Typst 不完全相同，故这里只显示独立试排卡片。
	return "#(" + out.String() + ").join()", true
}
func typString(s string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s) + "\""
}

var mainFont = regexp.MustCompile(`(?m)^\s*\\setmainfont\{([^{}\r\n]{1,100})\}`)
var cjkFont = regexp.MustCompile(`(?m)^\s*\\setCJKmainfont\{([^{}\r\n]{1,100})\}`)

func paragraphDocument(header, body string) (string, bool) {
	content, ok := paragraphContent(body)
	if !ok || len(header) > 32768 {
		return "", false
	}
	fonts := []string{}
	for _, re := range []*regexp.Regexp{mainFont, cjkFont} {
		if m := re.FindStringSubmatch(header); len(m) > 0 && strings.IndexFunc(m[1], unicode.IsControl) < 0 {
			fonts = append(fonts, typString(m[1]))
		}
	}
	fonts = append(fonts, `"Libertinus Serif","Songti SC","SimSun","Noto Serif CJK SC"`)
	return "#set page(width: 410pt, height: auto, margin: 4pt)\n#set text(size: 12pt, lang: \"zh\", font: (" + strings.Join(fonts, ",") + "))\n" + content, true
}

type paragraphEngine struct {
	dir    string
	cancel context.CancelFunc
	done   chan struct{}
	ready  chan bool
}

func startParagraph(ctx context.Context, dir, doc string) (*paragraphEngine, error) {
	bin, e := tool("typst")
	if e != nil {
		return nil, e
	}
	dir = filepath.Join(dir, "fast")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	if e = os.WriteFile(filepath.Join(dir, "paragraph.typ"), []byte(doc), 0600); e != nil {
		return nil, e
	}
	child, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(child, bin, "watch", "--root", dir, "--diagnostic-format", "short", "--ppi", "144", "paragraph.typ", "paragraph.png")
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	stream, e := cmd.StdoutPipe()
	if e != nil {
		cancel()
		return nil, e
	}
	cmd.Stderr = cmd.Stdout
	if e = cmd.Start(); e != nil {
		cancel()
		return nil, e
	}
	p := &paragraphEngine{dir: dir, cancel: cancel, ready: make(chan bool, 1), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer cmd.Wait()
		defer cancel()
		sc := bufio.NewScanner(stream)
		for sc.Scan() {
			l := sc.Text()
			i := strings.Index(l, "] ")
			if i < 0 {
				continue
			}
			status := l[i+2:]
			ok := strings.HasPrefix(status, "compiled successfully") || strings.HasPrefix(status, "compiled with warnings")
			if !ok && !strings.HasPrefix(status, "compiled with errors") {
				continue
			}
			select {
			case p.ready <- ok:
			case <-child.Done():
				return
			}
		}
	}()
	return p, nil
}
func (p *paragraphEngine) close() { p.cancel(); <-p.done }

// Paragraph 用常驻 Typst 试排 LaTeX 正文片段，返回临时预览而非最终分页。
// 仅在首次可试排编辑时启动进程；ctx 应贯穿会话。不可转换、编译失败或超时时返回 nil，交给整页编译。
func (s *Session) Paragraph(ctx context.Context, header, body string) image.Image {
	if !strings.EqualFold(filepath.Ext(s.path), ".tex") || s.fastUnavailable {
		return nil
	}
	doc, ok := paragraphDocument(header, body)
	if !ok {
		return nil
	}
	if s.fast == nil {
		p, e := startParagraph(ctx, s.dir, doc)
		if e != nil {
			s.fastUnavailable = true
			return nil
		}
		s.fast = p
	} else if s.lastDoc == doc {
		return s.lastParagraph
	} else if e := os.WriteFile(filepath.Join(s.fast.dir, "paragraph.typ"), []byte(doc), 0600); e != nil {
		return nil
	}
	p := s.fast
	select {
	case ok = <-p.ready:
		if !ok {
			return nil
		}
	case <-ctx.Done():
		return nil
	case <-p.done:
		s.fast = nil
		s.fastUnavailable = true
		return nil
	case <-time.After(3 * time.Second):
		p.close()
		s.fast = nil
		s.fastUnavailable = true
		return nil
	}
	f, e := os.Open(filepath.Join(p.dir, "paragraph.png"))
	if e != nil {
		return nil
	}
	defer f.Close()
	cfg, e := png.DecodeConfig(f)
	if e != nil || cfg.Width > 1200 || cfg.Height > 640 {
		return nil
	}
	if _, e = f.Seek(0, 0); e != nil {
		return nil
	}
	img, e := png.Decode(f)
	if e != nil {
		return nil
	}
	s.lastDoc, s.lastParagraph = doc, img
	return img
}
