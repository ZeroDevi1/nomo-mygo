import { existsSync } from 'node:fs';
import { defineConfig } from 'mygo-cli';
import pkg from './package.json' with { type: 'json' };

// 由 scripts/compile-macos-appicon.sh 生成；缺失时回退到由 icon 生成的 .icns。
const macosAssetCatalog = 'target/appicon/Assets.car';

export default defineConfig({
  name: 'Nomo',
  identifier: 'com.nomo.desktop',
  version: pkg.version,
  icon: 'desktop/packaging/icons/nomo/source/nomo-app-light-1024.png',
  main: './desktop',
  out: 'target/mygo',
  bindings: 'src/lib/desktop/runtime/bindings.gen.ts',

  devUrl: 'http://127.0.0.1:5173',
  devCommand: 'pnpm dev',
  buildCommand: 'pnpm build && node scripts/prepare-desktop-resources.mjs',
  frontendDist: 'dist',

  // 示例文档与其图片以 samples/ 目录随包分发（见 desktop/commands_files.go 的 resolveSampleDocument）。
  resources: ['target/desktop-resources/samples'],
  fileAssociations: [
    { ext: ['md', 'markdown'], name: 'Markdown Document', mimeType: 'text/markdown', role: 'Editor' },
    { ext: ['txt'], name: 'Text Document', mimeType: 'text/plain', role: 'Editor' },
    { ext: ['json'], name: 'JSON Document', mimeType: 'application/json', role: 'Editor' },
  ],

  macos: {
    minimumSystemVersion: '12.0',
    signingIdentity: process.env.APPLE_SIGNING_IDENTITY ?? '-',
    ...(existsSync(macosAssetCatalog) ? { icon: macosAssetCatalog } : {}),
    infoPlist: {
      // 声明内嵌的 Quick Look 扩展（由 scripts/build-macos.sh 在 MyGo 构建后放入），
      // 使 App 首次被 LaunchServices 注册时交给 PlugInKit 扫描；主应用不主动执行 pluginkit。
      CFBundlePlugIns: ['Contents/PlugIns/NomoQuickLookPreview.appex'],
      // 导出 Markdown UTI，供 Quick Look 扩展的 QLSupportedContentTypes 匹配 .md 文件。
      UTExportedTypeDeclarations: [
        {
          UTTypeIdentifier: 'net.daringfireball.markdown',
          UTTypeDescription: 'Markdown Document',
          UTTypeConformsTo: ['public.plain-text', 'public.text'],
          UTTypeTagSpecification: {
            'public.filename-extension': ['md', 'markdown', 'mdown', 'mkd'],
            'public.mime-type': ['text/markdown'],
          },
        },
      ],
    },
  },
});
