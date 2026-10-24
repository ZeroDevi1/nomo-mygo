//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// uninstallKeys 是安装器登记卸载信息的键：MyGo 的 NSIS 安装器按应用标识（com.nomo.desktop）登记；Tauri 时期的
// 安装器登记为 Nomo，升级前装的版本仍在那里。
var uninstallKeys = []string{
	`Software\Microsoft\Windows\CurrentVersion\Uninstall\` + appIdentifier,
	`Software\Microsoft\Windows\CurrentVersion\Uninstall\Nomo`,
}

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
		for _, key := range uninstallKeys {
			if location, ok := readRegistryString(root, key, "InstallLocation"); ok {
				locations = append(locations, strings.Trim(strings.TrimSpace(location), `"`))
			}
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

// launchInstallerAndExit 启动更新安装器并退出应用：经隐藏的 cmd 等应用退出（约 2 秒）后静默安装（MyGo 的 NSIS
// 安装器支持 /S，装到登记的同一目录），完成后重新打开 Nomo；随后应用立即退出，释放被占用的可执行文件。
// 调用方应先保存工作区（未保存文档的恢复副本），重新打开后照常恢复。
func launchInstallerAndExit(installer string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("读取 Nomo 可执行文件路径失败：%w", err)
	}
	if strings.ContainsAny(installer+exe, `"%^&|<>`) {
		return errors.New("更新路径包含 cmd 无法安全传递的字符")
	}
	script := fmt.Sprintf(`/c timeout /t 2 /nobreak >nul & "%s" /S & start "" "%s"`, installer, exe)
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe ` + script, HideWindow: true,
		CreationFlags: winapi.CREATE_NEW_PROCESS_GROUP | winapi.DETACHED_PROCESS}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动更新安装器失败：%w", err)
	}
	go func() {
		destroyAppTray()
		mygo.App.Exit(0)
	}()
	return nil
}
