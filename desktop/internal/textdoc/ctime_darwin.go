package textdoc

import "syscall"

func statCtime(stat *syscall.Stat_t) (int64, int64) {
	return stat.Ctimespec.Sec, stat.Ctimespec.Nsec
}
