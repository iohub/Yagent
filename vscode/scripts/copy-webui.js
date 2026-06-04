#!/usr/bin/env node

/**
 * Copy webui build to extension media directory
 */

import fs from 'fs';
import path from 'path';

const __dirname = path.dirname(new URL(import.meta.url).pathname);
const ROOT_DIR = path.resolve(__dirname, '..', '..');
const WEBUI_DIR = path.join(ROOT_DIR, 'webui');
const VSCODE_DIR = path.join(ROOT_DIR, 'vscode');
const DEST_DIR = path.join(VSCODE_DIR, 'media', 'webui');

function copyDir(src, dest) {
  if (!fs.existsSync(dest)) {
    fs.mkdirSync(dest, { recursive: true });
  }
  const entries = fs.readdirSync(src, { withFileTypes: true });
  for (const entry of entries) {
    const srcPath = path.join(src, entry.name);
    const destPath = path.join(dest, entry.name);
    if (entry.isDirectory()) {
      copyDir(srcPath, destPath);
    } else {
      fs.copyFileSync(srcPath, destPath);
    }
  }
}

const buildDir = path.join(WEBUI_DIR, 'build');
if (!fs.existsSync(buildDir)) {
  console.error('[copy-webui] Error: webui build not found. Run "npm run build" in webui first.');
  process.exit(1);
}

if (fs.existsSync(DEST_DIR)) {
  fs.rmSync(DEST_DIR, { recursive: true, force: true });
}

copyDir(buildDir, DEST_DIR);
console.log(`[copy-webui] Copied webui to: ${DEST_DIR}`);
