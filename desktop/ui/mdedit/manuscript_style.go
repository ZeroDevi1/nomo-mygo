package mdedit

import (
	"context"
	"hash/maphash"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"nomo/desktop/typeset"
)

// ManuscriptProject supplies file lookup and the main file's unsaved preamble to block previews.
type ManuscriptProject struct {
	Path, Root, Main        string
	MainPreamble            string
	References              string
	Index                   *typeset.ManuscriptReferences
	DiskRevision            uint64
	SetupTitle              string
	ListingTitle            string
	PreviewUnavailable      string
	FigureTitle, TableTitle string
}

// ManuscriptBlockStyle contains only the declarations needed for an isolated block.
// Source text and editing positions remain in the document's shared buffer.
type ManuscriptBlockStyle struct {
	Project                ManuscriptProject
	Preamble, Bibliography string
	References             string
	Counter, Number        string
	key                    uint64
	ctx                    context.Context
}

const manuscriptSetupLimit = 64 << 10

type manuscriptDeclarations struct {
	version            uint64
	text, bibliography string
	ends               []declarationEnd
	complete           bool
	refAliases         map[string]manuscriptRefAlias
}

type manuscriptRefAlias struct {
	before         int
	prefix, suffix string
}

var typstLetName = regexp.MustCompile(`^#let[\t ]+([A-Za-z_][A-Za-z0-9_-]{0,63})`)
var typstRefAliasBody = regexp.MustCompile(`^\(([A-Za-z_][A-Za-z0-9_-]*)\)[\t ]*=[\t ]*\[([^#\[\]\\*_` + "`" + `$@]{0,30})#ref\(([A-Za-z_][A-Za-z0-9_-]*),[\t ]*supplement:[\t ]*none\)([^#\[\]\\*_` + "`" + `$@]{0,30})\]$`)

func (decl *manuscriptDeclarations) collectReferenceAlias(src string, end int) {
	src = strings.TrimSpace(src)
	if len(src) > 512 || !strings.HasPrefix(src, "#let ") {
		return
	}
	name := typstLetName.FindStringSubmatch(src)
	if name == nil || len(src) == len(name[0]) || !strings.ContainsRune("(= \t", rune(src[len(name[0])])) {
		return
	}
	if _, duplicate := decl.refAliases[name[1]]; duplicate {
		decl.refAliases[name[1]] = manuscriptRefAlias{}
		return
	}
	if body := typstRefAliasBody.FindStringSubmatch(src[len(name[0]):]); body != nil && body[1] == body[3] && len(decl.refAliases) < 64 {
		if decl.refAliases == nil {
			decl.refAliases = make(map[string]manuscriptRefAlias)
		}
		decl.refAliases[strings.Clone(name[1])] = manuscriptRefAlias{end, strings.Clone(body[2]), strings.Clone(body[4])}
	}
}

type declarationEnd struct {
	source, text int
}

// ManuscriptLaTeXPreamble locates the actual opening command and ignores commented ones.
func ManuscriptLaTeXPreamble(text string) string {
	off := 0
	for line := range strings.Lines(text) {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(latexBeforeComment(trimmed), `\begin{document}`) {
			return text[:off+len(line)-len(trimmed)]
		}
		off += len(line)
	}
	return ""
}

func (e *Editor) SetManuscriptProject(p ManuscriptProject) {
	m := e.Doc.manuscript
	if m.project == p {
		return
	}
	old, next := m.project, p
	old.References, next.References = "", ""
	old.Index, next.Index = nil, nil
	m.project = p
	if old != next {
		m.styleVersion++
	}
	e.layoutGen++
	e.dirty = true
}

func (d *Document) updateManuscriptDeclarations(old, updated []Block, previousEnd, delta int) {
	m := d.manuscript
	for _, blocks := range [][]Block{old, updated} {
		for _, b := range blocks {
			if b.Kind == manuscriptSetup || b.Kind == manuscriptBibliography || b.Index == manuscriptPreamble {
				m.styleVersion++
				return
			}
		}
	}
	decl := &m.declarations
	if m.latex || d.version == 0 || decl.version != d.version {
		return
	}
	// Body edits retain the declarations and adjust only the recorded source offsets.
	if delta != 0 {
		i := sort.Search(len(decl.ends), func(i int) bool { return decl.ends[i].source > previousEnd })
		for j := i; j < len(decl.ends); j++ {
			decl.ends[j].source += delta
		}
		for name, alias := range decl.refAliases {
			if alias.before > previousEnd {
				alias.before += delta
				decl.refAliases[name] = alias
			}
		}
	}
	decl.version = d.version + 1
}

// declarations shares one bounded string across the visible Typst blocks.
// Its prefix ending before a block contains the declarations in scope there.
func (d *Document) declarations() *manuscriptDeclarations {
	m := d.manuscript
	if m.declarations.version == d.version {
		return &m.declarations
	}
	decl := manuscriptDeclarations{version: d.version, complete: true}
	var text strings.Builder
	for _, b := range d.blocks {
		switch b.Kind {
		case manuscriptSetup:
			if text.Len()+b.End-b.Start+1 > manuscriptSetupLimit {
				decl.complete = false
				break
			}
			source := d.slice(b.Start, b.End)
			text.WriteString(source)
			decl.collectReferenceAlias(source, b.End)
			text.WriteByte('\n')
			decl.ends = append(decl.ends, declarationEnd{source: b.End, text: text.Len()})
		case manuscriptBibliography:
			if decl.bibliography == "" && b.End-b.Start <= 4096 {
				decl.bibliography = strings.Clone(d.slice(b.Start, b.End))
			}
		}
		if !decl.complete {
			break
		}
	}
	decl.text = text.String()
	m.declarations = decl
	return &m.declarations
}

var latexMacroDefinition = regexp.MustCompile(`\\(?:newcommand|renewcommand|providecommand|DeclareRobustCommand|DeclareMathOperator)\*?\s*\{?\s*(\\[A-Za-z@]+)|\\(?:def|gdef)\s*(\\[A-Za-z@]+)`)
var latexControlWord = regexp.MustCompile(`\\[A-Za-z@]+`)
var latexLeadingControlWord = regexp.MustCompile(`^\\[A-Za-z@]+`)
var latexReference = regexp.MustCompile(`\\(?:[a-zA-Z]*cite[a-zA-Z]*|[a-zA-Z]*ref|pageref)\*?\s*[\[{]`)

func (m *manuscript) latexSetup(d *Document) string {
	if m.preambleVersion != m.styleVersion {
		head := d.slice(0, min(d.len(), manuscriptSetupLimit))
		m.preamble = strings.Clone(ManuscriptLaTeXPreamble(head))
		if m.preamble == "" {
			m.preamble = m.project.MainPreamble
		}
		m.macros = nil
		for line := range strings.Lines(m.preamble) {
			for _, match := range latexMacroDefinition.FindAllStringSubmatch(latexBeforeComment(line), -1) {
				if m.macros == nil {
					m.macros = make(map[string]bool)
				}
				m.macros[match[1]+match[2]] = true
			}
		}
		m.preambleVersion = m.styleVersion
	}
	return m.preamble
}

func (e *Editor) manuscriptBlockStyle(b Block, source string, formula bool) *ManuscriptBlockStyle {
	d, m := e.Doc, e.Doc.manuscript
	if m.project.Path == "" {
		return nil
	}
	style := &ManuscriptBlockStyle{Project: m.project}
	style.Project.References, style.Project.MainPreamble = "", ""
	style.Project.Index = nil
	if !formula {
		style.Counter = typeset.ManuscriptCounterKind(source, m.latex)
		if style.Counter != "" && m.project.Index != nil {
			style.Number = m.project.Index.ByLabel[typeset.ManuscriptLabel(source, m.latex)].Number
		}
	}
	if m.latex {
		style.Preamble = m.latexSetup(d)
		ref := latexReference.MatchString(source)
		if formula && !ref {
			custom := false
			if len(m.macros) != 0 {
				for _, cmd := range latexControlWord.FindAllString(source, -1) {
					custom = custom || m.macros[cmd]
				}
			}
			if !custom {
				return nil
			}
		}
		if ref {
			style.References = m.project.References
		}
	} else {
		decl := d.declarations()
		i := sort.Search(len(decl.ends), func(i int) bool { return decl.ends[i].source >= b.Start })
		if !decl.complete && i == len(decl.ends) {
			return nil
		}
		if i > 0 {
			style.Preamble = decl.text[:decl.ends[i-1].text]
		}
		if formula && style.Preamble == "" {
			return nil
		}
		style.Bibliography = decl.bibliography
		if strings.ContainsAny(source, "@#") {
			style.References = m.project.References
		}
		if b.Kind == manuscriptBibliography {
			style.Bibliography = ""
		}
	}
	if m.blockContext == nil || m.blockStyleVersion != m.styleVersion {
		if m.cancelBlocks != nil {
			m.cancelBlocks()
		}
		m.blockContext, m.cancelBlocks = context.WithCancel(m.ctx)
		m.blockStyleVersion = m.styleVersion
	}
	style.ctx = m.blockContext
	var h maphash.Hash
	h.SetSeed(e.images.seed)
	for _, s := range []string{style.Project.Path, style.Project.Root, style.Project.Main, style.Preamble, style.Bibliography, style.References, style.Counter, style.Number,
		strconv.FormatUint(style.Project.DiskRevision, 10)} {
		h.WriteString(s)
		h.WriteByte(0)
	}
	style.key = h.Sum64()
	return style
}
