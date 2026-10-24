//go:build !windows

package main

import (
	"errors"

	"nomo/desktop/internal/i18n"
)

// 非 Windows 平台：nativeWindowsIntegration 不会调用这些函数，保留签名只为编译。

func markdownFileAssociationStatus(locale i18n.Locale) (*markdownAssociationStatus, error) {
	return nil, errWindowsOnly(locale, "windows_default_only")
}

func registerMarkdownFileAssociation(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_default_only")
}

func unregisterMarkdownFileAssociation(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_default_only")
}

func windowsContextMenuStatusFor(locale i18n.Locale) (*windowsContextMenuStatus, error) {
	return nil, errWindowsOnly(locale, "windows_context_only")
}

func registerWindowsContextMenu(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_context_only")
}

func unregisterWindowsContextMenu(locale i18n.Locale) (*desktopActionPayload, error) {
	return nil, errWindowsOnly(locale, "windows_context_only")
}

func openWindowsSettings(string) error { return errors.New("该设置仅支持 Windows。") }
