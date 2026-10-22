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
	"nomo/desktop/internal/i18n"
)

const (
	nomoMarkdownProgID           = "Nomo.Markdown"
	nomoAppName                  = "Nomo"
	appCapabilitiesKey           = `Software\Nomo\Capabilities`
	registeredApplicationsKey    = `Software\RegisteredApplications`
	contextMenuCommandName       = "Nomo.Open"
	folderContextMenuCommandName = "Nomo.OpenFolder"
	contextMenuDisabledMarker    = "shell-context-menu.disabled"
	legacyInstallerCheckMarker   = "legacy-installer-check-complete"
	uninstallKey                 = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Nomo`
)

var (
	kernel32                                = winapi.NewLazySystemDLL("kernel32.dll")
	procGetCurrentPackageFamilyName         = kernel32.NewProc("GetCurrentPackageFamilyName")
	shell32                                 = winapi.NewLazySystemDLL("shell32.dll")
	procSHChangeNotify                      = shell32.NewProc("SHChangeNotify")
	procShellExecuteW                       = shell32.NewProc("ShellExecuteW")
	errNoPackage                            = syscall.Errno(15700) // APPMODEL_ERROR_NO_PACKAGE
	errInsufficientBuffer                   = syscall.Errno(122)
	shcneAssocChanged               uintptr = 0x08000000
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

func packageLocalStateFile(name string) (string, error) {
	family, err := currentPackageFamilyName()
	if err != nil {
		return "", err
	}
	if family == "" {
		return "", errors.New("当前进程没有 MSIX 包身份。")
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", errors.New("无法读取 LOCALAPPDATA。")
	}
	return filepath.Join(local, "Packages", family, "LocalState", name), nil
}

func writeMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 MSIX LocalState 目录失败：%w", err)
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return nil
	}
	temp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), os.Getpid()))
	if err := os.WriteFile(temp, []byte("1"), 0o644); err != nil {
		return fmt.Errorf("写入 MSIX LocalState 标记失败：%w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("提交 MSIX LocalState 标记失败：%w", err)
	}
	return nil
}

func contextMenuEnabled() (bool, error) {
	marker, err := packageLocalStateFile(contextMenuDisabledMarker)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(marker)
	return statErr != nil, nil
}

func setContextMenuEnabled(enabled bool) error {
	marker, err := packageLocalStateFile(contextMenuDisabledMarker)
	if err != nil {
		return err
	}
	if enabled {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("启用 MSIX 右键菜单失败：%w", err)
		}
	} else if err := writeMarker(marker); err != nil {
		return err
	}
	procSHChangeNotify.Call(shcneAssocChanged, 0, 0, 0)
	return nil
}

func openWindowsSettings(uri string) error {
	cmd := exec.Command("explorer.exe", uri)
	hideConsoleWindow(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("打开 Windows 设置失败：%w", err)
	}
	go cmd.Wait()
	return nil
}

func executableInfo() (path, fileName string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("读取 Nomo 可执行文件路径失败：%w", err)
	}
	return exe, filepath.Base(exe), nil
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

func writeRegistryString(path, name, value string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("写入 Windows 文件关联失败：%w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(name, value); err != nil {
		return fmt.Errorf("写入 Windows 文件关联失败：%w", err)
	}
	return nil
}

// deleteRegistryTree 删除 HKCU 下的键及其子键；键不存在视为成功。
func deleteRegistryTree(path string) {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		return
	}
	children, _ := key.ReadSubKeyNames(-1)
	key.Close()
	for _, child := range children {
		deleteRegistryTree(path + `\` + child)
	}
	_ = registry.DeleteKey(registry.CURRENT_USER, path)
}

func deleteRegistryValue(path, name string) {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	_ = key.DeleteValue(name)
}

func progIDMatchesNomo(progID, exeFileName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(progID))
	exe := strings.ToLower(exeFileName)
	return normalized == "nomo.markdown" || normalized == `applications\`+exe || normalized == exe
}

func markdownFileAssociationStatus(locale i18n.Locale) (*markdownAssociationStatus, error) {
	exe, exeName, err := executableInfo()
	if err != nil {
		return nil, err
	}
	defaultProgID, ok := readRegistryString(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\.md\UserChoice`, "ProgId")
	if !ok {
		defaultProgID, ok = readRegistryString(registry.CURRENT_USER, `Software\Classes\.md`, "")
	}
	managed := isPackaged()
	registered := managed || isNomoRegistered(exe)
	isDefault := ok && progIDMatchesNomo(defaultProgID, exeName)
	status := &markdownAssociationStatus{Supported: true, Registered: registered, IsDefault: isDefault, ManagedByPackage: managed}
	if ok {
		status.DefaultProgID = &defaultProgID
	}
	switch {
	case managed:
		status.Message = i18n.Text(locale, "md_assoc_managed_by_package")
	case isDefault:
		status.Message = i18n.Text(locale, "md_assoc_registered_default")
	case registered:
		status.Message = i18n.Text(locale, "md_assoc_registered_optional")
	default:
		status.Message = i18n.Text(locale, "md_assoc_not_registered")
	}
	return status, nil
}

func isNomoRegistered(exe string) bool {
	registeredApp, _ := readRegistryString(registry.CURRENT_USER, registeredApplicationsKey, nomoAppName)
	command, _ := readRegistryString(registry.CURRENT_USER, `Software\Classes\Nomo.Markdown\shell\open\command`, "")
	return strings.EqualFold(registeredApp, appCapabilitiesKey) && strings.Contains(strings.ToLower(command), strings.ToLower(exe))
}

// registerMarkdownFileAssociation 写入 ProgId、扩展名与默认应用能力，再打开系统默认应用页：
// Windows 10/11 保护 UserChoice 哈希，桌面应用不能可靠地静默修改默认应用。
func registerMarkdownFileAssociation(locale i18n.Locale) (*desktopActionPayload, error) {
	if isPackaged() {
		if err := openWindowsSettings("ms-settings:defaultapps"); err != nil {
			return nil, err
		}
		return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "md_assoc_managed_by_package")}, nil
	}
	exe, exeName, err := executableInfo()
	if err != nil {
		return nil, err
	}
	openCommand := fmt.Sprintf(`"%s" "%%1"`, exe)
	icon := fmt.Sprintf(`"%s",0`, exe)
	writes := []struct{ path, name, value string }{
		{`Software\Classes\Nomo.Markdown`, "", "Markdown Document"},
		{`Software\Classes\Nomo.Markdown`, "FriendlyTypeName", "Markdown Document"},
		{`Software\Classes\Nomo.Markdown\DefaultIcon`, "", icon},
		{`Software\Classes\Nomo.Markdown\shell\open\command`, "", openCommand},
	}
	for _, extension := range []string{".md", ".markdown"} {
		writes = append(writes,
			struct{ path, name, value string }{`Software\Classes\` + extension, "", nomoMarkdownProgID},
			struct{ path, name, value string }{`Software\Classes\` + extension, "PerceivedType", "text"},
			struct{ path, name, value string }{`Software\Classes\` + extension + `\OpenWithProgids`, nomoMarkdownProgID, ""},
		)
	}
	writes = append(writes,
		struct{ path, name, value string }{`Software\Classes\Applications\` + exeName + `\shell\open\command`, "", openCommand},
		struct{ path, name, value string }{appCapabilitiesKey, "ApplicationName", nomoAppName},
		struct{ path, name, value string }{appCapabilitiesKey, "ApplicationDescription", i18n.Text(locale, "file_assoc_description")},
		struct{ path, name, value string }{appCapabilitiesKey + `\FileAssociations`, ".md", nomoMarkdownProgID},
		struct{ path, name, value string }{appCapabilitiesKey + `\FileAssociations`, ".markdown", nomoMarkdownProgID},
		struct{ path, name, value string }{registeredApplicationsKey, nomoAppName, appCapabilitiesKey},
	)
	for _, write := range writes {
		if err := writeRegistryString(write.path, write.name, write.value); err != nil {
			return nil, err
		}
	}
	if err := openWindowsSettings("ms-settings:defaultapps"); err != nil {
		return nil, err
	}
	return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "md_assoc_registered_message")}, nil
}

func unregisterMarkdownFileAssociation(locale i18n.Locale) (*desktopActionPayload, error) {
	if isPackaged() {
		if err := openWindowsSettings("ms-settings:defaultapps"); err != nil {
			return nil, err
		}
		return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "md_assoc_managed_by_package")}, nil
	}
	_, exeName, err := executableInfo()
	if err != nil {
		return nil, err
	}
	deleteRegistryTree(`Software\Classes\Nomo.Markdown`)
	deleteRegistryTree(`Software\Classes\Applications\` + exeName)
	for _, extension := range []string{".md", ".markdown"} {
		if value, ok := readRegistryString(registry.CURRENT_USER, `Software\Classes\`+extension, ""); ok && progIDMatchesNomo(value, exeName) {
			deleteRegistryValue(`Software\Classes\`+extension, "")
		}
		deleteRegistryValue(`Software\Classes\`+extension+`\OpenWithProgids`, nomoMarkdownProgID)
	}
	deleteRegistryTree(appCapabilitiesKey)
	deleteRegistryValue(registeredApplicationsKey, nomoAppName)
	return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "md_assoc_unregistered_message")}, nil
}

var contextMenuCommandKeys = []string{
	`Software\Classes\SystemFileAssociations\.md\shell\Nomo.Open`,
	`Software\Classes\SystemFileAssociations\.markdown\shell\Nomo.Open`,
	`Software\Classes\Directory\shell\Nomo.OpenFolder`,
	`Software\Classes\Directory\Background\shell\Nomo.OpenFolder`,
}

func windowsContextMenuStatusFor(locale i18n.Locale) (*windowsContextMenuStatus, error) {
	if isPackaged() {
		enabled, err := contextMenuEnabled()
		if err != nil {
			return nil, err
		}
		key := "context_menu_package_disabled"
		if enabled {
			key = "context_menu_package_enabled"
		}
		return &windowsContextMenuStatus{Supported: true, Registered: true, Enabled: enabled, ManagedByPackage: true, Message: i18n.Text(locale, key)}, nil
	}
	exe, _, err := executableInfo()
	if err != nil {
		return nil, err
	}
	registered := true
	for _, key := range contextMenuCommandKeys {
		command, _ := readRegistryString(registry.CURRENT_USER, key+`\command`, "")
		registered = registered && strings.Contains(strings.ToLower(command), strings.ToLower(exe))
	}
	message := i18n.Text(locale, "context_menu_not_registered")
	if registered {
		message = i18n.Text(locale, "context_menu_registered")
	}
	return &windowsContextMenuStatus{Supported: true, Registered: registered, Enabled: registered, Message: message}, nil
}

func registerWindowsContextMenu(locale i18n.Locale) (*desktopActionPayload, error) {
	if isPackaged() {
		if err := setContextMenuEnabled(true); err != nil {
			return nil, err
		}
		return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "context_menu_package_enabled")}, nil
	}
	exe, _, err := executableInfo()
	if err != nil {
		return nil, err
	}
	openCommand := fmt.Sprintf(`"%s" "%%1"`, exe)
	backgroundCommand := fmt.Sprintf(`"%s" "%%V"`, exe)
	icon := fmt.Sprintf(`"%s",0`, exe)
	entries := []struct{ key, label, command string }{
		{contextMenuCommandKeys[0], i18n.Text(locale, "open_with_nomo"), openCommand},
		{contextMenuCommandKeys[1], i18n.Text(locale, "open_with_nomo"), openCommand},
		{contextMenuCommandKeys[2], i18n.Text(locale, "open_folder_with_nomo"), openCommand},
		{contextMenuCommandKeys[3], i18n.Text(locale, "open_folder_with_nomo"), backgroundCommand},
	}
	for _, entry := range entries {
		for _, write := range []struct{ path, name, value string }{
			{entry.key, "MUIVerb", entry.label},
			{entry.key, "Icon", icon},
			{entry.key + `\command`, "", entry.command},
		} {
			if err := writeRegistryString(write.path, write.name, write.value); err != nil {
				return nil, err
			}
		}
	}
	return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "context_menu_registered")}, nil
}

func unregisterWindowsContextMenu(locale i18n.Locale) (*desktopActionPayload, error) {
	if isPackaged() {
		if err := setContextMenuEnabled(false); err != nil {
			return nil, err
		}
		return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "context_menu_package_disabled")}, nil
	}
	for _, key := range contextMenuCommandKeys {
		deleteRegistryTree(key)
	}
	return &desktopActionPayload{OK: true, Message: i18n.Text(locale, "context_menu_unregistered_message")}, nil
}

// legacyInstallerNotice 在 Store 包首次运行时检测是否仍存在旧的安装版，只提示一次。
func legacyInstallerNotice() (bool, error) {
	if !isPackaged() {
		return false, nil
	}
	marker, err := packageLocalStateFile(legacyInstallerCheckMarker)
	if err != nil {
		return false, err
	}
	if info, err := os.Stat(marker); err == nil && info.Mode().IsRegular() {
		return false, nil
	}
	prompt := legacyWindowsInstallerExists()
	return prompt, writeMarker(marker)
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

func legacyWindowsInstallerExists() bool {
	for _, location := range installLocations() {
		if info, err := os.Stat(filepath.Join(location, "uninstall.exe")); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// isCurrentWindowsInstallerInstallation 判断当前可执行文件位于安装器登记的安装目录（且存在卸载程序）。
func isCurrentWindowsInstallerInstallation() (bool, error) {
	exe, _, err := executableInfo()
	if err != nil {
		return false, err
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

// launchInstallerAndExit 以被动模式启动安装器（/P /R：显示进度并在完成后重启应用），随后退出。
func launchInstallerAndExit(path string) error {
	file, _ := syscall.UTF16PtrFromString(path)
	operation, _ := syscall.UTF16PtrFromString("open")
	parameters, _ := syscall.UTF16PtrFromString("/P /R")
	result, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(operation)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(parameters)), 0, 5 /* SW_SHOW */)
	if result <= 32 {
		return fmt.Errorf("启动更新安装器失败，系统错误码：%d", result)
	}
	go mygo.App.Exit(0)
	return nil
}
