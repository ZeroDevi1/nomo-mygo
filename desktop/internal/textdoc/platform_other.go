//go:build !darwin

package textdoc

import "errors"

// cloneFileCOW 在没有文件级 clone 的平台返回错误，调用方回退为后台流式复制。
func cloneFileCOW(source, target string) error {
	return errors.ErrUnsupported
}
