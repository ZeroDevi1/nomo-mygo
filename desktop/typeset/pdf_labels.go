package typeset

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// PDFPageLabel reads the optional printed label for a one-based physical page.
// An empty result lets the reader fall back to the physical page number.
func PDFPageLabel(ctx context.Context, path string, page int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	pdf, err := api.ReadContext(ctx, file, model.NewDefaultConfiguration())
	if err != nil {
		return ""
	}
	catalog, err := pdf.Catalog()
	if err != nil {
		return ""
	}
	best, visits := -1, 0
	var label types.Dict
	var walk func(types.Object, int)
	walk = func(obj types.Object, depth int) {
		if obj == nil || depth > 24 || visits >= 4096 || ctx.Err() != nil {
			return
		}
		visits++
		node, err := pdf.DereferenceDict(obj)
		if err != nil {
			return
		}
		if limits, _ := pdf.DereferenceArray(node["Limits"]); len(limits) == 2 {
			start, _ := pdf.DereferenceInteger(limits[0])
			if start != nil && int(*start) >= page {
				return
			}
		}
		items, _ := pdf.DereferenceArray(node["Nums"])
		for i := 0; i+1 < len(items); i += 2 {
			n, _ := pdf.DereferenceInteger(items[i])
			if n != nil && int(*n) > best && int(*n) < page {
				if value, err := pdf.DereferenceDict(items[i+1]); err == nil && value != nil {
					best, label = int(*n), value
				}
			}
		}
		children, _ := pdf.DereferenceArray(node["Kids"])
		for _, child := range children {
			walk(child, depth+1)
			if visits >= 4096 || ctx.Err() != nil {
				break
			}
		}
	}
	walk(catalog["PageLabels"], 0)
	if label == nil || ctx.Err() != nil {
		return ""
	}
	prefix, _ := pdf.DereferenceStringOrHexLiteral(label["P"], model.V10, nil)
	prefix = strings.Join(strings.Fields(prefix), " ")
	if len(prefix) > 128 {
		return ""
	}
	style := label.NameEntry("S")
	if style == nil {
		return prefix
	}
	n := page - best
	if start := label.IntEntry("St"); start != nil && *start > 0 && *start < 1_000_000 {
		n += *start - 1
	}
	switch *style {
	case "D":
		return prefix + strconv.Itoa(n)
	case "R", "r":
		if n < 1 || n > 3999 {
			return ""
		}
		var roman strings.Builder
		for _, digit := range []struct {
			value int
			text  string
		}{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}} {
			for n >= digit.value {
				roman.WriteString(digit.text)
				n -= digit.value
			}
		}
		number := roman.String()
		if *style == "r" {
			number = strings.ToLower(number)
		}
		return prefix + number
	case "A", "a":
		if n < 1 || n > 26*32 {
			return ""
		}
		first := (*style)[0]
		return prefix + strings.Repeat(string(first+byte((n-1)%26)), (n-1)/26+1)
	}
	return ""
}
