package shell

import (
	"time"

	"golang.org/x/sys/windows"
)

var scholarGetDoubleClickTime = windows.NewLazySystemDLL("user32.dll").NewProc("GetDoubleClickTime")

func scholarDoubleClickInterval() time.Duration {
	milliseconds, _, _ := scholarGetDoubleClickTime.Call()
	return time.Duration(milliseconds) * time.Millisecond
}
