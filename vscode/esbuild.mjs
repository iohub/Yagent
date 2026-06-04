#!/usr/bin/env node

/**
 * esbuild bundler for the CodeActor VSCode extension.
 *
 * Bundles src/extension.ts and all its imports (including web-tree-sitter)
 * into a single out/extension.js, so the .vsix needs no node_modules.
 *
 * WASM files are NOT inlined — they are copied separately by copy-wasm.js
 * and loaded at runtime via Parser.init({ locateFile }) in languageParser.ts.
 */

import esbuild from 'esbuild';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const isProd = process.argv.includes('--production');

/** @type {import('esbuild').BuildOptions} */
const baseConfig = {
  entryPoints: [path.join(__dirname, 'src', 'extension.ts')],
  bundle: true,
  outfile: path.join(__dirname, 'out', 'extension.js'),
  platform: 'node',
  format: 'cjs',
  target: 'node18',
  // vscode is injected at runtime by the extension host — never bundle it.
  external: [
    'vscode',
    // WASM and native binaries are loaded from disk at runtime.
    '*.wasm',
    '*.node',
  ],
  // web-tree-sitter ships as ESM; esbuild handles the ESM→CJS conversion.
  // The package also has a CJS build (web-tree-sitter.cjs) but esbuild
  // picks up the correct entry via the exports map.
  mainFields: ['main', 'module'],
  conditions: ['require', 'node', 'default'],
  sourcemap: !isProd,
  minify: isProd,
  treeShaking: true,
  logLevel: 'info',
};

if (process.argv.includes('--watch')) {
  const ctx = await esbuild.context(baseConfig);
  await ctx.watch();
  console.log('[esbuild] Watching for changes...');
} else {
  await esbuild.build(baseConfig);
}
