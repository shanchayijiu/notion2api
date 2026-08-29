import { cpSync, existsSync, mkdirSync, rmSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const frontendRoot = resolve(__dirname, '..');
const outDir = resolve(frontendRoot, 'out');
const targetDir = resolve(frontendRoot, '..', 'static', 'admin');

if (!existsSync(outDir)) {
  throw new Error(`Next export output not found: ${outDir}`);
}

rmSync(targetDir, { recursive: true, force: true });
mkdirSync(targetDir, { recursive: true });
cpSync(outDir, targetDir, { recursive: true });
console.log(`Synced static admin build to ${targetDir}`);
