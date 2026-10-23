//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
	winapi "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"nomo/desktop/internal/applog"
)

// Windows 安装形态（供更新检查判断是否提供更新）：Store 包、NSIS 安装版或免安装版。

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Nomo`

var (
	kernel32                        = winapi.NewLazySystemDLL("kernel32.dll")
	procGetCurrentPackageFamilyName = kernel32.NewProc("GetCurrentPackageFamilyName")
	errNoPackage                    = syscall.Errno(15700) // APPMODEL_ERROR_NO_PACKAGE
	errInsufficientBuffer           = syscall.Errno(122)
)

// currentPackageFamilyName 返回 MSIX 包系列名称；进程没有包身份时返回空串。
func currentPackageFamilyName() (string, error) {
	var length uint32
	result, _, _ := procGetCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&length)), 0)
	if syscall.Errno(result) == errNoPackage {
		return "", nil
	}
	if syscall.Errno(result) != errInsufficientBuffer || length == 0 {
		return "", fmt.Errorf("读取当前 MSIX 包系列名称长度失败，Windows 错误码：%d", result)
	}
	buffer := make([]uint16, length)
	result, _, _ = procGetCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0])))
	if result != 0 {
		return "", fmt.Errorf("读取当前 MSIX 包系列名称失败，Windows 错误码：%d", result)
	}
	return winapi.UTF16ToString(buffer), nil
}

func isPackaged() bool {
	name, err := currentPackageFamilyName()
	return err == nil && name != ""
}

// readRegistryString 读取字符串值；键或值不存在时返回空串与 false。
func readRegistryString(root registry.Key, path, name string) (string, bool) {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return "", false
	}
	return value, true
}

func installLocations() []string {
	var locations []string
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if location, ok := readRegistryString(root, uninstallKey, "InstallLocation"); ok {
			locations = append(locations, strings.Trim(strings.TrimSpace(location), `"`))
		}
	}
	return locations
}

// isCurrentWindowsInstallerInstallation 判断当前可执行文件位于安装器登记的安装目录（且存在卸载程序）。
func isCurrentWindowsInstallerInstallation() (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("读取 Nomo 可执行文件路径失败：%w", err)
	}
	normalize := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		return strings.ToLower(strings.Trim(path, `"`))
	}
	normalizedExe := normalize(exe)
	for _, location := range installLocations() {
		if location == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(location, "uninstall.exe")); err != nil {
			continue
		}
		if strings.HasPrefix(normalizedExe, normalize(location)) {
			applog.Info("Update", "检测到安装版环境")
			return true, nil
		}
	}
	return false, nil
}

// currentInstallationKind：开发构建不做远程更新；Store 包交给商店；否则按安装器登记区分安装版与免安装版。
func currentInstallationKind() (installationKind, error) {
	if isPackaged() {
		return installationStore, nil
	}
	if mygo.IsDev() {
		return installationUnsupported, nil
	}
	installer, err := isCurrentWindowsInstallerInstallation()
	if err != nil {
		return "", err
	}
	if installer {
		return installationInstaller, nil
	}
	return installationPortable, nil
}
