// 把随安装包分发的示例文档及其图片复制到 target/desktop-resources/samples，
// 由 mygo.config.ts 的 resources 打包为应用资源目录下的 samples/。
import { cpSync, mkdirSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const samples = join(root, 'target', 'desktop-resources', 'samples');

rmSync(samples, { recursive: true, force: true });
mkdirSync(join(samples, 'assets'), { recursive: true });
cpSync(join(root, 'sample.md'), join(samples, 'sample.md'));
cpSync(join(root, 'assets', '128x128.png'), join(samples, 'assets', '128x128.png'));
