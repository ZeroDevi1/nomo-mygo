package typeset

import (
	"errors"
	"fmt"
	"strings"
)

// TypstFormulaBlock supplies the shared setup for standalone Typst math.
func TypstFormulaBlock(source string, size float32, display, dark bool) (BlockPreview, error) {
	if len(source) > 1024 || strings.ContainsAny(source, "#$`\\") || strings.Contains(source, "//") || strings.Contains(source, "/*") {
		return BlockPreview{}, errors.New("formula requires document context")
	}
	color, space := "#141414", ""
	if dark {
		color = "#ebebeb"
	}
	if display {
		space = " "
	}
	return BlockPreview{
		Source:      "$" + space + strings.TrimSpace(source) + space + "$",
		Preamble:    fmt.Sprintf("#set text(size: %.2fpt, fill: rgb(%q))", size, color),
		Inline:      !display,
		Transparent: true,
	}, nil
}
