import {
  copyFileSync,
  cpSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { dirname, join, resolve, sep } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const icons = join(root, 'desktop', 'packaging', 'icons');
const source = join(icons, 'nomo', 'source');
const options = process.argv.slice(2);
if (options.some((option) => option !== '--macos-only')) {
  throw new Error('用法：node scripts/generate-icons.mjs [--macos-only]');
}
const macosOnly = options.includes('--macos-only');
const appSizes = [128, 256, 512, 1024];
const targetSizes = [16, 20, 24, 30, 32, 36, 40, 44, 48, 60, 64, 72, 80, 96, 256];
const scales = [125, 150, 200, 400];
const packageLogos = { Square44x44Logo: 44, Square71x71Logo: 71, Square150x150Logo: 150, StoreLogo: 50 };

mkdirSync(join(root, '.tmp'), { recursive: true });
const temporary = mkdtempSync(join(root, '.tmp', 'nomo-icons-'));

function copy(from, to) {
  mkdirSync(dirname(to), { recursive: true });
  copyFileSync(from, to);
}

function render(svg, name, sizes) {
  const input = join(temporary, `${name}.svg`);
  const output = join(temporary, name);
  // CLI 内部会复用源位图；先把矢量渲染到最高所需分辨率，避免放大小图。
  const resolution = Math.max(1024, ...(sizes ?? []));
  writeFileSync(
    input,
    svg.replace(/\bwidth="\d+" height="\d+"/, `width="${resolution}" height="${resolution}"`),
    'utf8',
  );
  // 栅格化与 ico/icns 打包沿用 Tauri CLI 的 icon 子命令；它不再是项目依赖，按需经 pnpm dlx 运行。
  const args = ['dlx', '@tauri-apps/cli@2.11.2', 'icon', input, '--output', output];
  for (const size of sizes ?? []) args.push('--png', String(size));
  const result = spawnSync(process.platform === 'win32' ? 'pnpm.cmd' : 'pnpm', args, {
    cwd: root,
    encoding: 'utf8',
    windowsHide: true,
    maxBuffer: 8 * 1024 * 1024,
  });
  if (result.error || result.status !== 0) {
    throw new Error(`生成 ${name} 失败：${result.error?.message ?? (result.stderr + result.stdout).trim()}`);
  }
  return output;
}

function png(output, size) {
  return join(output, `${size}x${size}.png`);
}

function desktopIconSvg(svg, background, border) {
  const plate = `<rect x="16" y="16" width="224" height="224" rx="48" fill="${background}"/>`;
  if (!svg.includes(plate)) {
    throw new Error('应用 Logo 底板结构已变化，请同步更新桌面图标的内侧描边。');
  }
  // 桌面图标派生描边，应用内引用的 SVG 和 PNG 保持母版造型。
  return svg.replace(
    plate,
    `<rect x="16" y="16" width="224" height="224" rx="48" fill="${border}"/>\n  <rect x="24" y="24" width="208" height="208" rx="40" fill="${background}"/>`,
  );
}

function macosSvg(svg) {
  // macOS 图标底板占 256 画布的 218px；ICNS 与运行时 Dock 共用相同边距。
  return svg.replace(
    /(<svg\b[^>]*>)([\s\S]*)(<\/svg>)/,
    '$1\n  <g transform="translate(3.428571 3.428571) scale(0.9732142857)">$2  </g>\n$3',
  );
}

function catalogSvg(svg) {
  // Icon Composer 自己提供平台遮罩；直接用矢量裁切的满幅 1024px 图层。
  return svg.replace('viewBox="0 0 256 256"', 'viewBox="16 16 224 224"');
}

function generateMacos(light, dark) {
  const directory = join(icons, 'nomo', 'macos');
  for (const [theme, svg] of [['light', light], ['dark', dark]]) {
    const output = render(macosSvg(svg), `macos-${theme}`, [256, 512, 1024]);
    for (const size of [256, 512, 1024]) {
      copy(png(output, size), join(directory, `nomo-app-${theme}-${size}.png`));
    }
    const catalog = render(catalogSvg(svg), `catalog-${theme}`, [1024]);
    copy(png(catalog, 1024), join(directory, `nomo-app-${theme}-catalog-1024.png`));
  }
  const bundle = render(macosSvg(light), 'macos-bundle');
  copy(join(bundle, 'icon.icns'), join(icons, 'icon.icns'));
}

function generateTray() {
  const directory = join(icons, 'nomo', 'tray');
  for (const theme of ['light', 'dark']) {
    for (const state of ['active', 'inactive']) {
      const isDark = theme === 'dark';
      const background = isDark ? '#2b313b' : '#fefefe';
      const border = isDark ? '#7b8594' : '#c2c8d0';
      const foreground = state === 'active'
        ? (isDark ? '#f8f9fb' : '#252c36')
        : (isDark ? '#b6bac3' : '#646b76');
      const underline = state === 'active' ? (isDark ? '#5b9bff' : '#4d86fd') : foreground;
      // 应用主题与任务栏外观可能不同，底板和边框保证两种背景下都能辨认。
      const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">
  <title>Nomo 托盘图标 · ${theme} · ${state}</title>
  <rect x="1.5" y="1.5" width="21" height="21" rx="4.5" fill="${background}" stroke="${border}" stroke-width="1"/>
  <path d="M 7 14 V 5.8 L 17 14 V 5.8" fill="none" stroke="${foreground}" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>
  <rect x="5.5" y="18.5" width="13" height="1.5" rx="0.75" fill="${underline}"/>
</svg>
`;
      const name = `nomo-tray-${theme}-${state}`;
      mkdirSync(directory, { recursive: true });
      writeFileSync(join(directory, `${name}.svg`), svg, 'utf8');
      const output = render(svg, name, [24, 48]);
      copy(png(output, 24), join(directory, `${name}-24-preview.png`));
      copy(png(output, 48), join(directory, `${name}-48.png`));
    }
  }
}

try {
  const lightLogo = readFileSync(join(source, 'nomo-app-light.svg'), 'utf8');
  const darkLogo = lightLogo
    .replace('Nomo 浅色图标', 'Nomo 深色图标')
    .replace('圆角白色底板、深色 N', '圆角深色底板、白色 N')
    .replaceAll('#fefefe', '#2b313b')
    .replaceAll('#252c36', '#f8f9fb')
    .replaceAll('#4d86fd', '#5b9bff')
    .replaceAll('#999ca3', '#b6bac3');
  writeFileSync(join(source, 'nomo-app-dark.svg'), darkLogo, 'utf8');
  const light = desktopIconSvg(lightLogo, '#fefefe', '#c8cdd4');
  const dark = desktopIconSvg(darkLogo, '#2b313b', '#7b8594');

  if (!macosOnly) {
    const bundle = render(light, 'default-bundle');
    for (const entry of readdirSync(bundle, { withFileTypes: true })) {
      if (['icon.icns', 'android', 'ios'].includes(entry.name)) continue;
      cpSync(join(bundle, entry.name), join(icons, entry.name), { recursive: true });
    }
    const packageSizes = Object.values(packageLogos).flatMap((size) =>
      scales.map((scale) => Math.round((size * scale) / 100)),
    );
    const lightPngs = render(light, 'light', [...new Set([...appSizes, ...targetSizes, ...packageSizes])]);
    const darkPngs = render(dark, 'dark', [...new Set([...appSizes, ...targetSizes])]);
    const lightLogoPngs = render(lightLogo, 'logo-light', appSizes);
    const darkLogoPngs = render(darkLogo, 'logo-dark', appSizes);
    for (const [theme, output] of [['light', lightLogoPngs], ['dark', darkLogoPngs]]) {
      for (const size of appSizes) copy(png(output, size), join(source, `nomo-app-${theme}-${size}.png`));
    }
    const mobileBundle = render(lightLogo, 'mobile-bundle');
    for (const platform of ['android', 'ios']) {
      cpSync(join(mobileBundle, platform), join(icons, platform), { recursive: true });
    }
    copy(png(lightPngs, 64), join(icons, '64x64.png'));
    copy(png(lightPngs, 512), join(icons, 'app-icon.png'));

    const windows = join(icons, 'nomo', 'windows');
    for (const size of targetSizes) {
      for (const [suffix, output] of [['', lightPngs], ['_altform-unplated', darkPngs], ['_altform-lightunplated', lightPngs]]) {
        copy(png(output, size), join(windows, `Square44x44Logo.targetsize-${size}${suffix}.png`));
      }
    }
    // 无限定符的基础 Logo 已表示 scale-100，避免 PRI 中出现重复候选。
    for (const [name, size] of Object.entries(packageLogos)) {
      for (const scale of scales) {
        copy(png(lightPngs, Math.round((size * scale) / 100)), join(windows, `${name}.scale-${scale}.png`));
      }
    }
    generateTray();
  }
  generateMacos(light, dark);
  console.log(macosOnly ? '已从 SVG 更新 macOS 图标。' : '已从 SVG 更新浅深色、托盘、Windows、macOS 及各尺寸图标。');
} finally {
  if (!resolve(temporary).startsWith(resolve(root, '.tmp') + sep)) {
    throw new Error(`拒绝清理临时目录之外的路径：${temporary}`);
  }
  rmSync(temporary, { recursive: true, force: true });
}
