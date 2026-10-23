// Package typography 是精细排版（Knuth–Plass 段落断行）的纯计算部分，移植自原版 src/lib/typography 的
// knuthPlass.ts、rules.ts、hyphenation.ts 与 types.ts：候选行评估、按类别分配间距、两端/左对齐选择、汉字标点
// 禁则与压缩、行末悬挂、中西文空白、英文 Liang 断词。与界面无关；测量与绘制由 mdedit 完成（kp.go）。
//
// 偏移一律为 UTF-16 单元（与原版及 mdedit 的显示偏移一致）。宽度单位为 DIP（原版为 CSS px）。
package typography

// Profile 是标点规则档案（原版 TypographyProfile）。
type Profile string

const (
	ProfileZhCN Profile = "zh-CN"
	ProfileZhTW Profile = "zh-TW"
)

// Options 是排版选项（原版 TypographyOptions；Enabled 由调用方处理）。原版设置只保留总开关，固定使用简体档案
// 与行末悬挂。
type Options struct {
	Profile Profile
	Hanging bool
}

// DefaultOptions 与原版 DEFAULT_TYPOGRAPHY 的 profile、hanging 一致。
var DefaultOptions = Options{Profile: ProfileZhCN, Hanging: true}

// Kind 是排版单元的类别。
type Kind uint8

const (
	Box Kind = iota
	Glue
	Penalty
)

// Item 是一个排版单元（原版 LayoutItem）：From、To 为显示 UTF-16 偏移，零长度的胶只影响视觉。
type Item struct {
	Kind           Kind
	From, To       int
	Text           string
	Width          float64
	Ascent         float64
	Descent        float64
	FontSize       float64
	Stretch        float64
	Shrink         float64
	Weight         float64 // 0 视为 1
	ComfortStretch float64 // 舒适调整量，区别于硬性伸缩上限；HasComfort 为 false 时不参与评分
	ComfortShrink  float64
	HasComfort     bool
	Code           bool // 行内代码：占比高时整段改用左对齐
	Hyphenatable   bool
	Discardable    bool
	// SourceWhitespace 表示原文中的空白（区别于自动胶与软换行投影）。
	SourceWhitespace bool
	TrimStart        float64
	TrimEnd          float64
	Hang             float64
	LeadingSpace     float64 // 字形墨迹左侧空白（原版 actualBoundingBoxLeft 推得）；HasLeading 为 false 视为无穷
	TrailingSpace    float64
	HasLeading       bool
	HasTrailing      bool
	// BreakWidth 为在此断行时显示的连字符宽度（仅该单元作为行末时使用）。
	BreakWidth float64
}

func (it *Item) weight() float64 {
	if it.Weight == 0 {
		return 1
	}
	return it.Weight
}

// BreakPoint 是断点（原版 BreakPoint）：At 为不含的单元下标。
type BreakPoint struct {
	At       int
	Penalty  float64
	Required bool
	Flagged  bool
	Width    float64
}

// Alignment 是对齐方式。
type Alignment uint8

const (
	AlignJustify Alignment = iota // 零值：两端对齐（原版未指定 alignment 时的行为）
	AlignAuto                     // 先尝试两端对齐，无解时左对齐
	AlignRagged
)

// Input 是一个段落的排版输入（原版 ParagraphInput）。
type Input struct {
	Items           []Item
	Breaks          []BreakPoint
	Width           float64
	FirstLineIndent float64
	LineWidths      []float64
	MinLineHeight   float64
	Hanging         bool
	// EditableWhitespace 让行首行尾的原文空白保持实际宽度（可编辑视图中占有光标位置）。
	EditableWhitespace bool
	Alignment          Alignment
	Tolerance          float64 // 0 视为 10000
	LastLineMinWidth   float64 // 0 视为行宽的 20%
	HasLastLineMin     bool
	LastLinePenalty    float64 // 0 视为 1000
	MaxCandidates      int     // 0 视为 100000，负数为零预算
	TimeBudgetMs       float64 // 0 视为 100
}

// Line 是一行的排版结果（原版 LineLayout）：[Start, End) 为单元下标，Adjustments[i-Start] 为单元 i 的宽度调整。
type Line struct {
	Start, End               int
	VisibleStart, VisibleEnd int
	Width, NaturalWidth      float64
	Ratio, Badness           float64
	Fitness                  int
	Adjustments              []float64
	TrimStart, TrimEnd, Hang float64
	HyphenWidth              float64
	Ascent, Descent          float64
	Height, Baseline         float64
	Last                     bool
}

// Fallback 是回退原因。
type Fallback string

const (
	FallbackInvalidMetrics Fallback = "invalid-metrics"
	FallbackNoSolution     Fallback = "no-solution"
	FallbackBudget         Fallback = "budget-exceeded"
	FallbackUnsupported    Fallback = "unsupported-content"
)

// Result 是段落排版结果：Ready 为 false 时 Reason 给出回退原因，调用方保留原生布局。
type Result struct {
	Ready      bool
	Reason     Fallback
	Lines      []Line
	Demerits   float64
	Candidates int
	Alignment  Alignment // Ready 时为 AlignJustify 或 AlignRagged
}
