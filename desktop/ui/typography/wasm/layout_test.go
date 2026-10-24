package main

import (
	"strings"
	"testing"
)

// TestLayoutJustifies：纯中文段落两端对齐，非末行的间距调整之和等于行宽余量；末行不拉伸、句号行末悬挂。
func TestLayoutJustifies(t *testing.T) {
	text := strings.Repeat("精细排版让中文段落两端对齐，标点挤压与行末悬挂更自然。好的排版让阅读更舒适，段落右边缘应当整齐。", 3)
	var ps []piece
	for _, r := range text {
		ps = append(ps, piece{T: string(r), W: 16, S: 16})
	}
	res := layout(request{Width: 530, MinLineHeight: 28, FontSize: 16, SpaceWidth: 4, HyphenWidth: 5, Pieces: ps})
	if !res.OK || len(res.Lines) < 2 {
		t.Fatalf("排版失败：%+v", res)
	}
	for k, l := range res.Lines {
		sum := 0.0
		for _, o := range l.Ops {
			sum += o.Px
			if o.N != 1 {
				t.Errorf("调整区间应为单个字符：%+v", o)
			}
		}
		natural := float64(l.U1-l.U0) * 16
		last := k == len(res.Lines)-1
		switch {
		case !last && (sum <= 0 || natural+sum > 530.01 || natural+sum < 529.99):
			t.Errorf("第 %d 行应撑满 530：自然宽 %.1f，调整 %.2f", k, natural, sum)
		case last && sum > 0:
			t.Errorf("末行不应拉伸：%.2f", sum)
		}
	}
	// 不支持的文字（如阿拉伯文）不排版，页面保持浏览器排版。
	if res := layout(request{Width: 530, FontSize: 16, MinLineHeight: 28, Pieces: []piece{{T: "مرحبا", W: 30, S: 16}}}); res.OK {
		t.Error("不支持的文字应返回 ok=false")
	}
}
