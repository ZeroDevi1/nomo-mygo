package main

import (
	_ "embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"nomo/desktop/internal/i18n"
)

// Windows 集成：MSIX 包身份、Microsoft Store、.md 文件关联与资源管理器右键菜单。
// 平台相关实现在 windows_integration_windows.go / windows_integration_other.go。

//go:embed packaging/msix/package-identity.json
var packageIdentityJSON []byte

type packageIdentity struct {
	State                string  `json:"state"`
	Name                 string  `json:"name"`
	DisplayName          string  `json:"displayName"`
	Publisher            string  `json:"publisher"`
	PublisherDisplayName string  `json:"publisherDisplayName"`
	ApplicationID        string  `json:"applicationId"`
	PackageFamilyName    *string `json:"packageFamilyName"`
	StoreProductID       *string `json:"storeProductId"`
}

type markdownAssociationStatus struct {
	Supported        bool    `json:"supported"`
	Registered       bool    `json:"registered"`
	IsDefault        bool    `json:"is_default"`
	DefaultProgID    *string `json:"default_prog_id"`
	ManagedByPackage bool    `json:"managedByPackage"`
	Message          string  `json:"message"`
}

type windowsContextMenuStatus struct {
	Supported        bool   `json:"supported"`
	Registered       bool   `json:"registered"`
	Enabled          bool   `json:"enabled"`
	ManagedByPackage bool   `json:"managedByPackage"`
	Message          string `json:"message"`
}

func storeProductID() *string {
	var identity packageIdentity
	if json.Unmarshal(packageIdentityJSON, &identity) != nil || identity.StoreProductID == nil || strings.TrimSpace(*identity.StoreProductID) == "" {
		return nil
	}
	return identity.StoreProductID
}

// microsoftStoreURI 校验 Product ID 只含字母数字后生成商店详情页地址。
func microsoftStoreURI(productID string) (string, error) {
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return "", errors.New("Microsoft Store Product ID 无效。")
	}
	for _, r := range productID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "", errors.New("Microsoft Store Product ID 无效。")
		}
	}
	return "ms-windows-store://pdp/?ProductId=" + productID, nil
}

func init() {
	register("get_markdown_file_association_status", func(_ *commandCall, _ noArgs) (*markdownAssociationStatus, error) {
		return markdownFileAssociationStatus(effectiveLocale())
	})
	register("register_markdown_file_association", func(_ *commandCall, _ noArgs) (*desktopActionPayload, error) {
		return registerMarkdownFileAssociation(effectiveLocale())
	})
	register("unregister_markdown_file_association", func(_ *commandCall, _ noArgs) (*desktopActionPayload, error) {
		return unregisterMarkdownFileAssociation(effectiveLocale())
	})
	register("get_windows_context_menu_status", func(_ *commandCall, _ noArgs) (*windowsContextMenuStatus, error) {
		return windowsContextMenuStatusFor(effectiveLocale())
	})
	register("register_windows_context_menu", func(_ *commandCall, _ noArgs) (*desktopActionPayload, error) {
		return registerWindowsContextMenu(effectiveLocale())
	})
	register("unregister_windows_context_menu", func(_ *commandCall, _ noArgs) (*desktopActionPayload, error) {
		return unregisterWindowsContextMenu(effectiveLocale())
	})
	register("get_legacy_installer_notice", func(_ *commandCall, _ noArgs) (map[string]bool, error) {
		prompt, err := legacyInstallerNotice()
		return map[string]bool{"shouldPrompt": prompt}, err
	})
	register("open_windows_installed_apps", func(_ *commandCall, _ noArgs) (*desktopActionPayload, error) {
		if err := openWindowsSettings("ms-settings:appsfeatures"); err != nil {
			return nil, err
		}
		return &desktopActionPayload{OK: true, Message: "已打开 Windows 已安装的应用。"}, nil
	})
	register("open_microsoft_store_product", func(_ *commandCall, _ noArgs) (*desktopActionPayload, error) {
		productID := storeProductID()
		if productID == nil {
			return nil, errors.New("尚未配置 Microsoft Store Product ID。")
		}
		uri, err := microsoftStoreURI(*productID)
		if err != nil {
			return nil, err
		}
		if err := openWindowsSettings(uri); err != nil {
			return nil, err
		}
		return &desktopActionPayload{OK: true, Message: "已打开 Microsoft Store。"}, nil
	})
}

// unsupportedAssociationStatus 是非 Windows 平台的文件关联状态。
func unsupportedAssociationStatus(locale i18n.Locale) *markdownAssociationStatus {
	return &markdownAssociationStatus{Message: i18n.Text(locale, "windows_default_only")}
}

func unsupportedContextMenuStatus(locale i18n.Locale) *windowsContextMenuStatus {
	return &windowsContextMenuStatus{Message: i18n.Text(locale, "windows_context_only")}
}

func errWindowsOnly(locale i18n.Locale, key string) error {
	return fmt.Errorf("%s", i18n.Text(locale, key))
}
