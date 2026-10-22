#!/usr/bin/env bash
# macOS 发布构建：MyGo 产出已签名的 Nomo.app 后，嵌入 Quick Look 扩展、重新封签并生成 DMG。
#
# MyGo 只把 resources 复制到 Contents/Resources，没有写入 Contents/PlugIns 的配置项；
# 其 `codesign --deep` 还会用主应用 entitlements 覆盖嵌套代码。因此这里让 MyGo 跳过 DMG，
# 由脚本按“扩展（自身沙盒 entitlements）→ 主应用（不带 --deep）”的由内向外顺序补签，
# 最后自行打包 DMG。主应用其余内容保持 MyGo 的签名不动。
#
# 环境变量：
#   APPLE_SIGNING_IDENTITY  签名身份，默认 ad hoc（-），与 mygo.config.ts 一致。
#   MACOS_PLATFORM          MyGo 目标平台，默认 darwin/universal；单架构时同步决定扩展架构。
#   SKIP_DMG=1              只产出 .app。
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS bundle can only be built on macOS." >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLATFORM="${MACOS_PLATFORM:-darwin/universal}"
TARGET_ARCH="${PLATFORM#darwin/}"
IDENTITY="${APPLE_SIGNING_IDENTITY:--}"
APP_NAME="Nomo"
OUT_DIR="$ROOT_DIR/target/mygo/darwin-$TARGET_ARCH"
APP_BUNDLE="$OUT_DIR/$APP_NAME.app"
EXTENSION_NAME="NomoQuickLookPreview.appex"
EXTENSION_SRC="$ROOT_DIR/target/quicklook/$EXTENSION_NAME"
EXTENSION_ENTITLEMENTS="$ROOT_DIR/desktop/packaging/macos/NomoQuickLookPreview/NomoQuickLookPreview.entitlements"

case "$TARGET_ARCH" in
  universal) QUICKLOOK_ARCH=universal ;;
  arm64) QUICKLOOK_ARCH=arm64 ;;
  amd64) QUICKLOOK_ARCH=x86_64 ;;
  *)
    echo "Unsupported macOS platform: $PLATFORM" >&2
    exit 1
    ;;
esac

cd "$ROOT_DIR"

# 1. 主应用：图标资源目录须先于 mygo build 生成，mygo.config.ts 按其是否存在决定图标来源。
bash scripts/compile-macos-appicon.sh
pnpm exec mygo build -platform "$PLATFORM" -skip-dmg

if [[ ! -d "$APP_BUNDLE" ]]; then
  echo "MyGo did not produce $APP_BUNDLE." >&2
  exit 1
fi

# 2. 扩展：构建后放入 Contents/PlugIns，版本以最终主应用 Info.plist 为准。
QUICKLOOK_ARCH="$QUICKLOOK_ARCH" bash scripts/build-macos-quicklook.sh

APP_INFO="$APP_BUNDLE/Contents/Info.plist"
PLUGINS_DIR="$APP_BUNDLE/Contents/PlugIns"
EXTENSION_BUNDLE="$PLUGINS_DIR/$EXTENSION_NAME"
mkdir -p "$PLUGINS_DIR"
rm -rf "$EXTENSION_BUNDLE"
ditto "$EXTENSION_SRC" "$EXTENSION_BUNDLE"

APP_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP_INFO")"
APP_BUNDLE_VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$APP_INFO")"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $APP_VERSION" "$EXTENSION_BUNDLE/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $APP_BUNDLE_VERSION" "$EXTENSION_BUNDLE/Contents/Info.plist"

# 3. 由内向外签名。参数与 MyGo 的 codesignArgs 保持一致：真实身份启用 hardened runtime 与时间戳，
#    公证要求二者；主应用不带 --deep，避免覆盖扩展的沙盒 entitlements。
SIGN_FLAGS=(--force --sign "$IDENTITY")
if [[ "$IDENTITY" != "-" ]]; then
  SIGN_FLAGS+=(--options runtime --timestamp)
fi
/usr/bin/codesign "${SIGN_FLAGS[@]}" --entitlements "$EXTENSION_ENTITLEMENTS" "$EXTENSION_BUNDLE"
/usr/bin/codesign "${SIGN_FLAGS[@]}" "$APP_BUNDLE"

bash scripts/verify-macos-bundle.sh "$APP_BUNDLE"

# 4. DMG：MyGo 的 DMG 步骤无法在补签后单独调用，这里按其产物命名生成等价的压缩镜像。
if [[ "${SKIP_DMG:-}" == "1" ]]; then
  exit 0
fi
if [[ "$TARGET_ARCH" == "universal" ]]; then
  DMG_PATH="$OUT_DIR/$APP_NAME $APP_VERSION.dmg"
else
  DMG_PATH="$OUT_DIR/$APP_NAME $APP_VERSION $TARGET_ARCH.dmg"
fi
DMG_STAGE="$(mktemp -d "$OUT_DIR/.dmg-XXXXXX")"
trap 'rm -rf "$DMG_STAGE"' EXIT
# ditto 保留签名封印内的扩展属性与符号链接，cp -R 可能破坏严格校验。
ditto "$APP_BUNDLE" "$DMG_STAGE/$APP_NAME.app"
ln -s /Applications "$DMG_STAGE/Applications"
rm -f "$DMG_PATH"
hdiutil create -volname "$APP_NAME" -srcfolder "$DMG_STAGE" -fs HFS+ -format ULMO -ov "$DMG_PATH" >/dev/null
if [[ "$IDENTITY" != "-" ]]; then
  /usr/bin/codesign --force --timestamp --sign "$IDENTITY" "$DMG_PATH"
fi

echo "Built $DMG_PATH"
