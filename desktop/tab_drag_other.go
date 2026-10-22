//go:build !windows

package main

// nativeTabDragSupported：跨窗口标签拖动依赖 Win32 光标与窗口命中，其他平台由页面内拖动处理。
const nativeTabDragSupported = false

func runNativeTabDrag(source string, input dragInput) { endTabDrag(source, input.DragID) }
