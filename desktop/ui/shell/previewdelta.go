package shell

import (
	"iter"

	"github.com/egoist/mygo/ui/editor"
)

// 快照共享 rope；跳过相同的块定位首个变化的 UTF-8 字符，不在输入时拼接全文。
func changedOffset(a, b editor.Snapshot) int {
	chunks := func(s editor.Snapshot) iter.Seq[string] {
		return func(yield func(string) bool) { s.Chunks(0, s.Len(), yield) }
	}
	ai, ac := iter.Pull(chunks(a))
	defer ac()
	bi, bc := iter.Pull(chunks(b))
	defer bc()
	x, _ := ai()
	y, _ := bi()
	off := 0
	for len(x) > 0 && len(y) > 0 {
		n := min(len(x), len(y))
		if x[:n] != y[:n] {
			for i := 0; i < n; i++ {
				if x[i] != y[i] {
					off += i
					break
				}
			}
			for off > 0 && a.Slice(off, off+1)[0]&0xc0 == 0x80 {
				off--
			}
			break
		}
		off += n
		x, y = x[n:], y[n:]
		if x == "" {
			x, _ = ai()
		}
		if y == "" {
			y, _ = bi()
		}
	}
	return off
}

func sameSuffix(a editor.Snapshot, i int, b editor.Snapshot, j int) bool {
	if a.Len()-i != b.Len()-j {
		return false
	}
	seq := func(yield func(string) bool) { a.Chunks(i, a.Len(), yield) }
	next, stop := iter.Pull(seq)
	defer stop()
	x, _ := next()
	ok := true
	b.Chunks(j, b.Len(), func(y string) bool {
		for len(y) > 0 {
			n := min(len(x), len(y))
			if n == 0 || x[:n] != y[:n] {
				ok = false
				return false
			}
			y, x = y[n:], x[n:]
			if x == "" {
				x, _ = next()
			}
		}
		return true
	})
	return ok
}
