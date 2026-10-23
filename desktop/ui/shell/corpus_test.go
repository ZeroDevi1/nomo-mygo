package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/config"
	"nomo/desktop/internal/i18n"
)

// TestCorpusShell 用外部测试集检查外壳层的场景（设 NOMO_CORPUS=<测试集目录> 运行）：
//
//   - 08_边界与兼容 下各编码/换行样本：复制到临时目录，经外壳打开、不编辑直接保存，字节应不变；
//     编辑一个字再撤销后保存，字节同样不变。
//   - 07_多标签与交互 的四标签 A/B/C/D：同时打开，循环切换 20 轮，记录每次切换后一帧的耗时与第 1、20 轮
//     后的 Go 堆；每个标签输入一字再撤销，检查原文恢复。
//   - 打开—编辑—关闭 20 轮：同一文件反复打开、输入、撤销、关闭，记录堆变化。
//
// 无头渲染、单进程 Go 堆，不代表实机进程内存。
func TestCorpusShell(t *testing.T) {
	root := os.Getenv("NOMO_CORPUS")
	if root == "" {
		t.Skip("设 NOMO_CORPUS=<测试集目录> 运行")
	}
	t.Run("encodings", func(t *testing.T) { corpusEncodings(t, root) })
	t.Run("fourTabs", func(t *testing.T) { corpusFourTabs(t, root) })
	t.Run("openEditClose", func(t *testing.T) { corpusOpenEditClose(t, root) })
	t.Run("folderTree", func(t *testing.T) { corpusFolderTree(t, root) })
}

// corpusFolderTree 打开 256 文件的目录树，全部展开并画出，再依次打开其中 32 个文件（每次只保留一个标签）。
func corpusFolderTree(t *testing.T, root string) {
	dir := filepath.Join(root, "09_目录树256文件")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("缺少目录树样本")
	}
	s, tt, settle := newCorpusShell(t)
	s.openFolder(dir)
	if s.folder == nil {
		t.Fatal("打开文件夹失败")
	}
	var files []string
	var expand func(n *folderNode)
	expand = func(n *folderNode) {
		if !n.dir {
			files = append(files, n.path)
			return
		}
		if !n.loaded {
			if err := n.load(); err != nil {
				t.Fatal(err)
			}
		}
		n.open = true
		for _, c := range n.children {
			expand(c)
		}
	}
	start := time.Now()
	expand(s.folder)
	tt.Frame()
	expandAll := time.Since(start)
	md := 0
	for _, f := range files {
		if strings.HasSuffix(f, ".md") {
			md++
		}
	}
	if md != 256 {
		t.Fatalf("目录树应有 256 份 Markdown，实际 %d", md)
	}
	var opens []time.Duration
	for k, f := range files {
		if !strings.HasSuffix(f, ".md") || k%8 != 0 {
			continue
		}
		start := time.Now()
		s.OpenFiles([]string{f})
		tt.Frame()
		opens = append(opens, time.Since(start))
		s.requestClose([]int{s.activeTab().ID}, false)
		settle()
	}
	slices.Sort(opens)
	t.Logf("目录树：全部展开并绘制 %v；打开文件 n=%d p50=%v max=%v", expandAll, len(opens), opens[len(opens)/2], opens[len(opens)-1])
}

func newCorpusShell(t *testing.T, files ...string) (*Shell, *ui.Tester, func()) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Config: cfg, Locale: i18n.ZhCN, Files: files})
	posted := make(chan func(), 64)
	s.post = func(fn func()) { posted <- fn }
	tt := ui.NewTester(s.view, 1200, 800)
	tt.Frame()
	settle := func() {
		for {
			select {
			case fn := <-posted:
				fn()
				tt.Frame()
			case <-time.After(80 * time.Millisecond):
				tt.Frame()
				return
			}
		}
	}
	return s, tt, settle
}

func corpusEncodings(t *testing.T, root string) {
	matches, _ := filepath.Glob(filepath.Join(root, "08_边界与兼容", "编码_*.md"))
	if len(matches) == 0 {
		t.Skip("测试集中没有编码样本")
	}
	for _, src := range matches {
		name := filepath.Base(src)
		orig, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, orig, 0o644); err != nil {
			t.Fatal(err)
		}
		s, tt, settle := newCorpusShell(t, p)
		tab := s.activeTab()
		s.save(tab, false, nil)
		settle()
		if got, _ := os.ReadFile(p); !bytes.Equal(got, orig) {
			t.Errorf("%s：打开后直接保存，字节改变（%d → %d）", name, len(orig), len(got))
		}
		tab.Doc.SetSelection(selection(len(tab.Doc.Source())))
		tt.Type("改")
		tab.Ed.Run("undo")
		tt.Frame()
		s.save(tab, false, nil)
		settle()
		if got, _ := os.ReadFile(p); !bytes.Equal(got, orig) {
			t.Errorf("%s：输入再撤销后保存，字节改变", name)
		}
	}
}

func heapMB() float64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapInuse) / (1 << 20)
}

func corpusFourTabs(t *testing.T, root string) {
	var files []string
	for _, n := range []string{"A_文字", "B_表格", "C_图片", "D_代码图表"} {
		files = append(files, filepath.Join(root, "07_多标签与交互", "四标签_"+n+".md"))
	}
	s, tt, settle := newCorpusShell(t, files...)
	if len(s.tabs) != 4 {
		t.Fatalf("应打开 4 个标签，实际 %d", len(s.tabs))
	}
	originals := make([]string, 4)
	for i, tab := range s.tabs {
		originals[i] = tab.Doc.Source()
	}
	settle()
	var switches []time.Duration
	var heap1, heap20 float64
	for round := 1; round <= 20; round++ {
		for i := range s.tabs {
			start := time.Now()
			s.activate(i)
			tt.Frame()
			switches = append(switches, time.Since(start))
		}
		switch round {
		case 1:
			heap1 = heapMB()
		case 20:
			heap20 = heapMB()
		}
	}
	slices.Sort(switches)
	t.Logf("四标签切换 n=%d p50=%v p95=%v max=%v；Go 堆 第1轮 %.1f MB、第20轮 %.1f MB", len(switches),
		switches[len(switches)/2], switches[len(switches)*95/100], switches[len(switches)-1], heap1, heap20)
	// 每个标签输入一字再撤销：原文恢复、标签不再算未保存。
	for i, tab := range s.tabs {
		s.activate(i)
		tt.Frame()
		tab.Doc.SetSelection(selection(0))
		tt.Type("中")
		tab.Ed.Run("undo")
		tt.Frame()
		if tab.Doc.Source() != originals[i] || tab.Dirty() {
			t.Errorf("标签 %d 输入撤销后原文或未保存状态不对", i)
		}
	}
	if heap20 > heap1*1.5+20 {
		t.Errorf("20 轮切换后 Go 堆增长明显：%.1f → %.1f MB", heap1, heap20)
	}
}

func corpusOpenEditClose(t *testing.T, root string) {
	file := filepath.Join(root, "06_综合混合压力", "真实风格_技术方案.md")
	if _, err := os.Stat(file); err != nil {
		t.Skip("缺少样本")
	}
	s, tt, settle := newCorpusShell(t)
	var heaps []float64
	var opens []time.Duration
	for round := 1; round <= 20; round++ {
		start := time.Now()
		s.OpenFiles([]string{file})
		tt.Frame()
		opens = append(opens, time.Since(start))
		tab := s.activeTab()
		tab.Doc.SetSelection(selection(len(tab.Doc.Source())))
		tt.Type("编辑")
		tab.Ed.Run("undo")
		tt.Frame()
		if tab.Dirty() {
			t.Fatalf("第 %d 轮：撤销后仍为未保存", round)
		}
		s.requestClose([]int{tab.ID}, false)
		settle()
		if len(s.tabs) != 0 {
			t.Fatalf("第 %d 轮关闭后仍有 %d 个标签", round, len(s.tabs))
		}
		if round == 1 || round == 20 {
			heaps = append(heaps, heapMB())
		}
	}
	slices.Sort(opens)
	t.Logf("打开—编辑—关闭 20 轮：打开+首帧 p50=%v max=%v；Go 堆 第1轮 %.1f MB、第20轮 %.1f MB", opens[10], opens[19], heaps[0], heaps[1])
	if heaps[1] > heaps[0]*1.5+20 {
		t.Errorf("20 轮后 Go 堆增长明显：%v", heaps)
	}
}
