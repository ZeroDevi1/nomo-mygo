package main

import (
	"fmt"
	"runtime"

	"nomo/desktop/internal/applog"
	"nomo/desktop/internal/i18n"
	"nomo/desktop/ui/shell"
)

// Windows 集成：.md 文件关联与资源管理器右键菜单，供原生设置页“Windows 集成”使用。
// 平台实现在 windows_integration_windows.go；其他平台不提供（nativeWindowsIntegration 返回 nil）。

type markdownAssociationStatus struct {
	Supported        bool
	Registered       bool
	IsDefault        bool
	DefaultProgID    *string
	ManagedByPackage bool
	Message          string
}

type windowsContextMenuStatus struct {
	Supported        bool
	Registered       bool
	Enabled          bool
	ManagedByPackage bool
	Message          string
}

// desktopActionPayload 是注册、取消注册操作的结果文案。
type desktopActionPayload struct {
	OK      bool
	Message string
}

// nativeWindowsIntegration 把平台实现接到原生外壳；文案按当时的界面语言生成。
func nativeWindowsIntegration() *shell.WindowsIntegration {
	if runtime.GOOS != "windows" {
		return nil
	}
	result := func(p *desktopActionPayload, err error) (string, error) {
		if err != nil {
			return "", err
		}
		return p.Message, nil
	}
	return &shell.WindowsIntegration{
		AssociationStatus: func() (shell.IntegrationStatus, error) {
			st, err := markdownFileAssociationStatus(effectiveLocale())
			if err != nil {
				return shell.IntegrationStatus{}, err
			}
			return shell.IntegrationStatus{On: st.Registered, Managed: st.ManagedByPackage, Message: st.Message}, nil
		},
		SetAssociation: func(on bool) (string, error) {
			if on {
				return result(registerMarkdownFileAssociation(effectiveLocale()))
			}
			return result(unregisterMarkdownFileAssociation(effectiveLocale()))
		},
		ContextMenuStatus: func() (shell.IntegrationStatus, error) {
			st, err := windowsContextMenuStatusFor(effectiveLocale())
			if err != nil {
				return shell.IntegrationStatus{}, err
			}
			return shell.IntegrationStatus{On: st.Enabled, Managed: st.ManagedByPackage, Message: st.Message}, nil
		},
		SetContextMenu: func(on bool) (string, error) {
			if on {
				return result(registerWindowsContextMenu(effectiveLocale()))
			}
			return result(unregisterWindowsContextMenu(effectiveLocale()))
		},
		OpenDefaultApps:   func() error { return openWindowsSettings("ms-settings:defaultapps") },
		OpenInstalledApps: func() error { return openWindowsSettings("ms-settings:appsfeatures") },
		LegacyInstaller: func() bool {
			prompt, err := legacyInstallerNotice()
			if err != nil {
				applog.Warn("Windows", "检测旧安装版失败："+err.Error())
			}
			return prompt
		},
	}
}

// errWindowsOnly 是非 Windows 平台调用集成操作时的错误。
func errWindowsOnly(locale i18n.Locale, key string) error {
	return fmt.Errorf("%s", i18n.Text(locale, key))
}
