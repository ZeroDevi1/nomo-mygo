//go:build !windows

package main

import (
	"errors"

	"nomo/desktop/internal/i18n"
)

func isPackaged() bool { return false }

func markdownFileAssociationStatus(locale i18n.Locale) (*markdownAssociationStatus, error) {
	return unsupportedAssociationStatus(locale), nil
}

func registerMarkdownFileAssociation(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_default_only")
}

func unregisterMarkdownFileAssociation(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_default_only")
}

func windowsContextMenuStatusFor(locale i18n.Locale) (*windowsContextMenuStatus, error) {
	return unsupportedContextMenuStatus(locale), nil
}

func registerWindowsContextMenu(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_context_only")
}

func unregisterWindowsContextMenu(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_context_only")
}

func legacyInstallerNotice() (bool, error) { return false, nil }

func openWindowsSettings(uri string) error { return errors.New("该设置仅支持 Windows。") }

func currentInstallationKind() (installationKind, error) { return installationUnsupported, nil }

func isCurrentWindowsInstallerInstallation() (bool, error) { return false, nil }

func launchInstallerAndExit(path string) error {
	return errors.New("当前平台不支持 Windows 安装包更新。")
}
