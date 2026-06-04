#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WEBUI_DIR="$ROOT_DIR/webui"
VSCODE_DIR="$ROOT_DIR/vscode"

echo "============================================"
echo "  CodeActor VSCode Extension Build Script"
echo "============================================"
echo ""

# Step 1: Build WebUI
echo "=== Step 1/3: Building WebUI ==="
cd "$WEBUI_DIR"
echo "[1/3] Installing webui dependencies..."
npm ci --silent 2>/dev/null || npm install --silent
echo "[1/3] Building React application..."
npm run build
echo "[1/3] WebUI build complete."
echo ""

# Step 2: Copy WebUI to VSCode extension
echo "=== Step 2/3: Copying WebUI to VSCode extension ==="
cd "$VSCODE_DIR"
node scripts/copy-webui.js
echo ""

# Step 3: Build VSCode extension
echo "=== Step 3/3: Building VSCode extension ==="
echo "[3/3] Bundling extension with esbuild..."
npm run bundle -- --production
echo "[3/3] Copying WASM files..."
npm run copy-wasm
echo "[3/3] Packaging extension as .vsix..."
npx vsce package --no-yarn --no-git-tag-version
echo ""

echo "============================================"
echo "  ✅ Build Complete!"
echo "  The .vsix file is in: $VSCODE_DIR/"
echo "============================================"
