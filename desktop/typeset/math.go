package typeset

import "strings"

// 快览只接受有明确 Typst 对应写法的数学记号。每个函数与符号均由本表产生，未知语法不原样透传。
var mathSymbols = map[string]string{
	"alpha": "alpha", "beta": "beta", "gamma": "gamma", "delta": "delta", "lambda": "lambda", "mu": "mu", "sigma": "sigma", "theta": "theta", "pi": "pi", "epsilon": "epsilon",
	"Delta": "Delta", "Sigma": "Sigma", "Omega": "Omega", "sum": "sum", "prod": "product", "int": "integral", "infty": "infinity", "times": "times", "cdot": "dot.op", "le": "lt.eq", "leq": "lt.eq", "ge": "gt.eq", "geq": "gt.eq", "in": "in", "neq": "eq.not", "pm": "plus.minus", "to": "arrow.r",
}

func inlineMath(s string) (string, bool) {
	if len(s) > 1024 {
		return "", false
	}
	i := 0
	var expr func(int) (string, bool)
	expr = func(depth int) (string, bool) {
		if depth > 12 {
			return "", false
		}
		var out strings.Builder
		for i < len(s) {
			ch := s[i]
			i++
			switch {
			case ch == '}':
				return out.String(), depth > 0
			case ch == '{':
				v, ok := expr(depth + 1)
				if !ok {
					return "", false
				}
				out.WriteString("(" + v + ")")
			case ch == '\\':
				a := i
				for i < len(s) && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
					i++
				}
				cmd := s[a:i]
				if v, ok := mathSymbols[cmd]; ok {
					out.WriteString(" " + v + " ")
					continue
				}
				fn, argc := "", 1
				switch cmd {
				case "frac":
					fn, argc = "frac", 2
				case "sqrt":
					fn = "sqrt"
				case "mathbf":
					fn = "bold"
				case "mathbb":
					fn = "bb"
				default:
					return "", false
				}
				out.WriteString(" " + fn + "(")
				for k := 0; k < argc; k++ {
					for i < len(s) && s[i] == ' ' {
						i++
					}
					if i >= len(s) || s[i] != '{' {
						return "", false
					}
					i++
					v, ok := expr(depth + 1)
					if !ok {
						return "", false
					}
					if k > 0 {
						out.WriteByte(',')
					}
					out.WriteString(v)
				}
				out.WriteString(") ")
			case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z':
				// TeX treats adjacent Latin letters as variables; Typst otherwise interprets a name.
				out.WriteByte(ch)
				if i < len(s) && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
					out.WriteByte(' ')
				}
			case ch == '/':
				out.WriteString(" slash ")
			case ch >= '0' && ch <= '9' || strings.ContainsRune(" +-=*^_().,<>|", rune(ch)):
				out.WriteByte(ch)
			default:
				return "", false
			}
		}
		return out.String(), depth == 0
	}
	v, ok := expr(0)
	return v, ok && strings.TrimSpace(v) != "" && i == len(s)
}
