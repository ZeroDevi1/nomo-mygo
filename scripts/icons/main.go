// Command icons 从 SVG 母版生成 Nomo 的浅深色、托盘、Windows 与 macOS 图标位图（用 MyGo 的无头渲染栅格化）。
//
// 用法（仓库根目录）：
//
//	go run ./scripts/icons              # 更新全部图标
//	go run ./scripts/icons -macos-only  # 只更新 macOS 图标
//	go run ./scripts/icons -out <目录>  # 写到其他目录，便于与现有资源对比
//
// 应用造型只改 desktop/packaging/icons/nomo/source/nomo-app-light.svg；深色配色、桌面描边、平台边距与托盘造型
// 在本文件维护。每个尺寸直接从矢量渲染，不缩放位图。.icns / .ico 由 mygo build 从 mygo.json 的 icon 生成。
package main

import (
	"flag"
	"fmt"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
)

var (
	outDir    = flag.String("out", "desktop/packaging/icons", "图标资源根目录")
	macosOnly = flag.Bool("macos-only", false, "只生成 macOS 图标")
)

var (
	appSizes     = []int{128, 256, 512, 1024}
	targetSizes  = []int{16, 20, 24, 30, 32, 36, 40, 44, 48, 60, 64, 72, 80, 96, 256}
	scales       = []int{125, 150, 200, 400}
	packageLogos = []struct {
		name string
		size int
	}{{"StoreLogo", 50}, {"Square44x44Logo", 44}, {"Square71x71Logo", 71}, {"Square150x150Logo", 150}}
)

func main() {
	log.SetFlags(0)
	flag.Parse()
	src := filepath.Join("desktop/packaging/icons/nomo/source", "nomo-app-light.svg")
	raw, err := os.ReadFile(src)
	if err != nil {
		log.Fatalf("读取 SVG 母版失败（须在仓库根目录运行）：%v", err)
	}
	lightLogo := string(raw)
	darkLogo := strings.NewReplacer(
		"Nomo 浅色图标", "Nomo 深色图标",
		"圆角白色底板、深色 N", "圆角深色底板、白色 N",
		"#fefefe", "#2b313b",
		"#252c36", "#f8f9fb",
		"#4d86fd", "#5b9bff",
		"#999ca3", "#b6bac3",
	).Replace(lightLogo)
	write(filepath.Join("nomo/source", "nomo-app-dark.svg"), []byte(darkLogo))
	light := desktopIcon(lightLogo, "#fefefe", "#c8cdd4")
	dark := desktopIcon(darkLogo, "#2b313b", "#7b8594")

	if !*macosOnly {
		for theme, svg := range map[string]string{"light": lightLogo, "dark": darkLogo} {
			for _, size := range appSizes {
				render(svg, size, fmt.Sprintf("nomo/source/nomo-app-%s-%d.png", theme, size))
			}
		}
		// 无限定符的基础 Logo 即 scale-100，不再额外生成重复的 scale-100 候选。
		for _, l := range packageLogos {
			render(light, l.size, l.name+".png")
			for _, scale := range scales {
				render(light, (l.size*scale+50)/100, fmt.Sprintf("nomo/windows/%s.scale-%d.png", l.name, scale))
			}
		}
		for _, size := range targetSizes {
			base := fmt.Sprintf("nomo/windows/Square44x44Logo.targetsize-%d", size)
			render(light, size, base+".png")
			render(dark, size, base+"_altform-unplated.png")
			render(light, size, base+"_altform-lightunplated.png")
		}
		generateTray()
	}
	for theme, svg := range map[string]string{"light": light, "dark": dark} {
		for _, size := range []int{256, 512, 1024} {
			render(macosIcon(svg), size, fmt.Sprintf("nomo/macos/nomo-app-%s-%d.png", theme, size))
		}
		render(catalogLayer(svg), 1024, fmt.Sprintf("nomo/macos/AppIcon.icon/Assets/nomo-app-%s.png", theme))
	}
	if *macosOnly {
		log.Print("已从 SVG 更新 macOS 图标。")
	} else {
		log.Print("已从 SVG 更新浅深色、托盘、Windows 与 macOS 图标。")
	}
}

const plate = `<rect x="16" y="16" width="224" height="224" rx="48" fill="%s"/>`

// desktopIcon 为桌面、任务栏 / Dock 与文件关联图标在底板内侧派生描边；应用内 Logo 保持母版造型。
func desktopIcon(svg, background, border string) string {
	old := fmt.Sprintf(plate, background)
	if !strings.Contains(svg, old) {
		log.Fatal("应用 Logo 底板结构已变化，请同步更新桌面图标的内侧描边。")
	}
	return strings.Replace(svg, old, fmt.Sprintf(plate, border)+"\n  "+
		fmt.Sprintf(`<rect x="24" y="24" width="208" height="208" rx="40" fill="%s"/>`, background), 1)
}

var svgBody = regexp.MustCompile(`(?s)(<svg\b[^>]*>)(.*)(</svg>)`)

// macosIcon 按 macOS 图标网格缩放：底板占 256 画布的 218px，与运行时 Dock 图标边距一致。
func macosIcon(svg string) string {
	return svgBody.ReplaceAllString(svg, "$1\n  <g transform=\"translate(3.428571 3.428571) scale(0.9732142857)\">$2  </g>\n$3")
}

// catalogLayer 是 Icon Composer 的满幅图层：直接裁到底板边界，平台遮罩由系统应用。
func catalogLayer(svg string) string {
	return strings.Replace(svg, `viewBox="0 0 256 256"`, `viewBox="16 16 224 224"`, 1)
}

// generateTray 生成浅深色 × 激活/未激活托盘图标：不透明圆角底板与对比边框保证任意任务栏背景下可辨认，
// 未激活时只把 N 与横线改为灰色。运行时嵌入 48px，24px 仅作预览。
func generateTray() {
	for _, theme := range []string{"light", "dark"} {
		for _, state := range []string{"active", "inactive"} {
			isDark, active := theme == "dark", state == "active"
			pick := func(darkV, lightV string) string {
				if isDark {
					return darkV
				}
				return lightV
			}
			background, border := pick("#2b313b", "#fefefe"), pick("#7b8594", "#c2c8d0")
			foreground := pick("#b6bac3", "#646b76")
			underline := foreground
			if active {
				foreground, underline = pick("#f8f9fb", "#252c36"), pick("#5b9bff", "#4d86fd")
			}
			svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">
  <title>Nomo 托盘图标 · %s · %s</title>
  <rect x="1.5" y="1.5" width="21" height="21" rx="4.5" fill="%s" stroke="%s" stroke-width="1"/>
  <path d="M 7 14 V 5.8 L 17 14 V 5.8" fill="none" stroke="%s" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>
  <rect x="5.5" y="18.5" width="13" height="1.5" rx="0.75" fill="%s"/>
</svg>
`, theme, state, background, border, foreground, underline)
			name := "nomo/tray/nomo-tray-" + theme + "-" + state
			write(name+".svg", []byte(svg))
			render(svg, 24, name+"-24-preview.png")
			render(svg, 48, name+"-48.png")
		}
	}
}

// render 把 SVG 渲染为 size×size 的透明底 PNG，写到 outDir 下的 rel。
func render(svg string, size int, rel string) {
	doc, err := ui.ParseSVG([]byte(svg))
	if err != nil {
		log.Fatalf("解析 %s 的 SVG 失败：%v", rel, err)
	}
	t := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Image(c, doc).Fill()
	}, size, size)
	t.SetScale(1)
	t.Frame()
	path := filepath.Join(*outDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, t.Image()); err != nil {
		log.Fatalf("写入 %s 失败：%v", path, err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

func write(rel string, data []byte) {
	path := filepath.Join(*outDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
}
