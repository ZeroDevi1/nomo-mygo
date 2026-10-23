package shell

import (
	"nomo/desktop/ui/mdedit"
)

// diagramTypes 与 diagramTemplates 取自原版 diagramTemplates.ts（DIAGRAM_TEMPLATES），用于“插入图表”与设置项
// defaultDiagramType；diagramLabels 为各类型的界面文案键（原版 getDiagramTypeLabel）。
var (
	diagramTypes     = []string{"flowchart", "sequenceDiagram", "classDiagram", "stateDiagram", "pie", "gantt", "erDiagram"}
	diagramLabels    = []string{"diagramFlowchart", "diagramSequence", "diagramClass", "diagramState", "diagramPie", "diagramGantt", "diagramEr"}
	diagramTemplates = map[string]string{
		"flowchart":       "flowchart TD\n  A[Start] --> B{Continue?}\n  B -- Yes --> C[Process]\n  B -- No --> D[End]\n  C --> D",
		"sequenceDiagram": "sequenceDiagram\n  participant User\n  participant App\n  User->>App: Request action\n  App-->>User: Return result",
		"classDiagram":    "classDiagram\n  class Document {\n    +string title\n    +save()\n  }\n  class Editor {\n    +render()\n  }\n  Document --> Editor",
		"stateDiagram":    "stateDiagram-v2\n  [*] --> Draft\n  Draft --> Review\n  Review --> Published\n  Published --> [*]",
		"pie":             "pie title Content share\n  \"Writing\" : 45\n  \"Editing\" : 30\n  \"Organizing\" : 25",
		"gantt":           "gantt\n  title Project plan\n  dateFormat  YYYY-MM-DD\n  section Phase one\n  Design :a1, 2026-06-01, 3d\n  Build :after a1, 5d",
		"erDiagram":       "erDiagram\n  USER ||--o{ DOCUMENT : owns\n  DOCUMENT ||--o{ ASSET : references\n  USER {\n    string id\n    string name\n  }\n  DOCUMENT {\n    string id\n    string title\n  }",
	}
)

// editorOptions 把设置转为语义编辑区选项（原版 updateOptions 与插入命令读取的偏好）。
func (s *Shell) editorOptions() mdedit.Options {
	p := s.prefs
	indent := map[string]string{"spaces-2": "  ", "spaces-4": "    ", "tab": "\t"}[p.CodeBlockIndent]
	return mdedit.Options{
		CodeLanguage:    p.DefaultCodeBlockLanguage,
		DiagramCode:     diagramTemplates[p.DefaultDiagramType],
		CodeIndent:      indent,
		HideLineNumbers: !p.CodeBlockLineNumbersVisible,
		RawInlineCode:   !p.InlineCodeRenderingEnabled,
		PlainCopy:       !p.CopyMarkdownSyntaxEnabled,
		Typography:      p.TypographyEnabled,
	}
}

// wheelStep 是 Ctrl 滚轮每缩放一档（10%）累计的滚动量。经验值：鼠标滚轮一格约 40，触控板连续滚动按累计量换档，
// 不会一次跳过多档。
const wheelStep = 40

// ctrlWheel 处理按住 Ctrl 的滚动（原版 ctrlWheelZoomEnabled）：关闭该设置时返回 false，按普通滚动处理。
func (s *Shell) ctrlWheel(dy float32) bool {
	if !s.prefs.CtrlWheelZoomEnabled {
		return false
	}
	s.wheelAcc += dy
	for s.wheelAcc <= -wheelStep {
		s.wheelAcc += wheelStep
		s.zoom(10)
	}
	for s.wheelAcc >= wheelStep {
		s.wheelAcc -= wheelStep
		s.zoom(-10)
	}
	return true
}
