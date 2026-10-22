#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS bundle verification can only run on macOS." >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 默认校验 build-macos.sh 的 universal 产物；单架构构建传入 .app 路径。
APP_BUNDLE="${1:-$ROOT_DIR/target/mygo/darwin-universal/Nomo.app}"

if [[ ! -d "$APP_BUNDLE" ]]; then
  echo "macOS app bundle not found: $APP_BUNDLE." >&2
  exit 1
fi

APP_INFO="$APP_BUNDLE/Contents/Info.plist"
EXTENSION_BUNDLE="$APP_BUNDLE/Contents/PlugIns/NomoQuickLookPreview.appex"
EXTENSION_INFO="$EXTENSION_BUNDLE/Contents/Info.plist"

if [[ ! -d "$EXTENSION_BUNDLE" || ! -f "$EXTENSION_INFO" ]]; then
  echo "Quick Look extension is missing from $APP_BUNDLE." >&2
  exit 1
fi

DECLARED_EXTENSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundlePlugIns:0' "$APP_INFO")"
if [[ "$DECLARED_EXTENSION" != "Contents/PlugIns/NomoQuickLookPreview.appex" ]]; then
  echo "Containing app does not declare the embedded Quick Look extension." >&2
  exit 1
fi

APP_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP_INFO")"
APP_BUNDLE_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$APP_INFO")"
EXTENSION_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$EXTENSION_INFO")"
EXTENSION_BUNDLE_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$EXTENSION_INFO")"
if [[ "$EXTENSION_VERSION" != "$APP_VERSION" ]]; then
  echo "Quick Look version mismatch: app=$APP_VERSION extension=$EXTENSION_VERSION." >&2
  exit 1
fi
if [[ "$EXTENSION_BUNDLE_VERSION" != "$APP_BUNDLE_VERSION" ]]; then
  echo "Quick Look build version mismatch: app=$APP_BUNDLE_VERSION extension=$EXTENSION_BUNDLE_VERSION." >&2
  exit 1
fi

ICON_NAME="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIconName' "$APP_INFO")"
if [[ "$ICON_NAME" != "AppIcon" ]]; then
  echo "Containing app is missing CFBundleIconName=AppIcon." >&2
  exit 1
fi
if [[ ! -f "$APP_BUNDLE/Contents/Resources/Assets.car" ]]; then
  echo "Appearance-aware AppIcon catalog is missing from $APP_BUNDLE." >&2
  exit 1
fi

/usr/bin/codesign --verify --deep --strict --verbose=2 "$APP_BUNDLE"

# PlugInKit 拒绝加载未沙盒化的扩展；对主应用 --deep 重签会静默抹掉这些 entitlements。
if ! /usr/bin/codesign -d --entitlements - --xml "$EXTENSION_BUNDLE" 2>/dev/null | grep -q "com.apple.security.app-sandbox"; then
  echo "Quick Look extension lost its App Sandbox entitlement." >&2
  exit 1
fi

echo "Verified embedded Quick Look extension: $EXTENSION_BUNDLE"
