package typeset

import (
	"strings"
	"testing"
)

func TestPDFAnchorUniqueness(t *testing.T) {
	block := func(body string) string {
		return `<block xMin="11" yMin="22" xMax="90" yMax="40"><line xMin="18" yMin="22" yMax="34"><word>` + body + `</word></line></block>`
	}
	page := func(s string) string { return `<page width="100" height="200"><flow>` + s + `</flow></page>` }
	idx := parseAnchors(strings.NewReader("<doc>" + page(block("重复")+block("A\x01B")) + page(block("重复")+block("中文 A")) + "</doc>"))
	if idx[blockKey("重复")].Page != 0 || idx[blockKey("AB")].Page != 0 {
		t.Fatal("ambiguous block matched")
	}
	a := idx[blockKey("中文\n A")]
	if a.Page != 2 || a.X != 10.5 || a.FontPt != 12 || a.IndentPt != 7 {
		t.Fatal(a)
	}
}
