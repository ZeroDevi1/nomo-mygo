package shell

import "testing"

func TestSanitizeExportHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<p>a<script>alert(1)</script>b</p>`, `<p>ab</p>`},
		{`<img src="x.png" onerror="alert(1)" width="50">`, `<img src="x.png" width="50">`},
		{`<a href=" java&#x09;script:alert(1)">x</a><a href="https://e.com">y</a>`, `<a>x</a><a href="https://e.com">y</a>`},
		{`<iframe src="https://e.com"><p>t</p></iframe><meta charset="x"><p>after</p>`, `<p>after</p>`},
		{`<img src="data:image/png;base64,AAA"><a href="data:text/html,x">d</a>`, `<img src="data:image/png;base64,AAA"><a>d</a>`},
		// 未改动的标签原样保留（SVG 属性大小写、单引号）。
		{`<svg viewBox='0 0 10 10'><circle cx="5" cy="5" r="4"/></svg><style>p{color:red}</style>`, `<svg viewBox='0 0 10 10'><circle cx="5" cy="5" r="4"/></svg><style>p{color:red}</style>`},
		{`<svg><a xlink:href="javascript:x"><text>t</text></a></svg>`, `<svg><a><text>t</text></a></svg>`},
	}
	for _, c := range cases {
		if got := sanitizeExportHTML(c.in); got != c.want {
			t.Errorf("%s\n got  %s\n want %s", c.in, got, c.want)
		}
	}
}
