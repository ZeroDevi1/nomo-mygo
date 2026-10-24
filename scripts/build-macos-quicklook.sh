#!/usr/bin/env bash
# 构建未签名的 Quick Look 预览扩展 target/quicklook/NomoQuickLookPreview.appex。
# 签名与嵌入由 scripts/build-macos.sh 在 MyGo 产出 .app 之后完成：扩展必须带自己的沙盒
# entitlements 单独签名，而 MyGo 对 .app 的 `codesign --deep` 会以主应用身份覆盖嵌套签名。
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "Nomo Quick Look extension can only be built on macOS." >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXTENSION_SRC_DIR="$ROOT_DIR/desktop/packaging/macos/NomoQuickLookPreview"
OUTPUT_DIR="$ROOT_DIR/target/quicklook/NomoQuickLookPreview.appex"
CONTENTS_DIR="$OUTPUT_DIR/Contents"
MACOS_DIR="$CONTENTS_DIR/MacOS"
RESOURCES_DIR="$CONTENTS_DIR/Resources"
BUILD_DIR="$ROOT_DIR/target/quicklook/build"

cd "$ROOT_DIR"

rm -rf "$OUTPUT_DIR" "$BUILD_DIR"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR" "$BUILD_DIR"

cp "$EXTENSION_SRC_DIR/Info.plist" "$CONTENTS_DIR/Info.plist"

# MyGo 以 mygo.json 的 version 同时写入主应用的 CFBundleShortVersionString 与 CFBundleVersion；
# 扩展构建版本须与主应用一致，覆盖同一营销版本时 PlugInKit 才会刷新扩展。
# 嵌入时 build-macos.sh 还会按最终 .app 的 Info.plist 再同步一次，防止 infoPlist 覆盖造成漂移。
APP_VERSION="$(plutil -extract version raw -o - mygo.json)"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $APP_VERSION" "$CONTENTS_DIR/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $APP_VERSION" "$CONTENTS_DIR/Info.plist"

# 主应用默认以 darwin/universal 构建，扩展默认同样产出双架构；单架构构建可显式指定。
ARCH="${QUICKLOOK_ARCH:-universal}"
case "$ARCH" in
  universal)
    SWIFT_TARGET_ARCHS=(arm64 x86_64)
    ;;
  arm64|aarch64)
    SWIFT_TARGET_ARCHS=(arm64)
    ;;
  x86_64|amd64)
    SWIFT_TARGET_ARCHS=(x86_64)
    ;;
  *)
    echo "Unsupported Quick Look architecture: $ARCH" >&2
    exit 1
    ;;
esac

SLICES=()
for SWIFT_TARGET_ARCH in "${SWIFT_TARGET_ARCHS[@]}"; do
  SLICE="$BUILD_DIR/NomoQuickLookPreview-$SWIFT_TARGET_ARCH"
  # Xcode 的 App Extension target 会自动把入口设为 _NSExtensionMain；
  # 直接调用 swiftc 时必须显式设置，否则生成的进程会启动后立即退出，导致 PlugInKit XPC Code=4097。
  # 预览为原生 AppKit 渲染（MarkdownRenderer.swift），不依赖 WebKit；BuiltinThemes.swift 与
  # desktop/ui/themes/builtin.go 的内置主题保持一致（两边同步维护）。
  xcrun swiftc \
    "$EXTENSION_SRC_DIR/PreviewViewController.swift" \
    "$EXTENSION_SRC_DIR/MarkdownRenderer.swift" \
    "$EXTENSION_SRC_DIR/BuiltinThemes.swift" \
    -emit-executable \
    -parse-as-library \
    -module-name NomoQuickLookPreview \
    -application-extension \
    -target "${SWIFT_TARGET_ARCH}-apple-macosx12.0" \
    -Xlinker -e \
    -Xlinker _NSExtensionMain \
    -Xlinker -rpath \
    -Xlinker @executable_path/../Frameworks \
    -Xlinker -rpath \
    -Xlinker @executable_path/../../../../Frameworks \
    -o "$SLICE" \
    -framework Cocoa \
    -framework QuickLook \
    -framework QuickLookUI
  SLICES+=("$SLICE")
done

xcrun lipo -create "${SLICES[@]}" -output "$MACOS_DIR/NomoQuickLookPreview"
rm -rf "$BUILD_DIR"

echo "Built $OUTPUT_DIR (unsigned, ${ARCH})"
