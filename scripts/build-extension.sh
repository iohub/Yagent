#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WEBUI_DIR="$ROOT_DIR/webui"
VSCODE_DIR="$ROOT_DIR/vscode"

echo "============================================"
echo "  CodeActor VSCode Extension Build Script"
echo "============================================"
echo ""

# Step 0: Build CodeActor binary for the current platform
echo "=== Step 0/4: Building CodeActor binary ==="
cd "$ROOT_DIR"

# 确定目标平台和架构
OS="${TARGET_OS:-$(go env GOOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]')}"
GO_BUILD_ARCH="${TARGET_ARCH:-$(go env GOARCH 2>/dev/null || uname -m)}"

# 文件名的架构名（与 Node.js process.arch 命名一致）
case "$GO_BUILD_ARCH" in
    amd64|x86_64) FILE_ARCH="x64" ;;
    arm64|aarch64) FILE_ARCH="arm64" ;;
    *) FILE_ARCH="$GO_BUILD_ARCH" ;;
esac

# 设置扩展名
EXT=""
if [ "$OS" = "windows" ]; then EXT=".exe"; fi

BINARY_NAME="codeactor-${OS}-${FILE_ARCH}${EXT}"
BINARY_DIR="$VSCODE_DIR/bin"
mkdir -p "$BINARY_DIR"

echo "[0/4] Building CodeActor binary for ${OS}/${GO_BUILD_ARCH}..."

# 构建 Go 二进制，使用 -s -w 减小体积
# 注意：GOARCH 必须使用 Go 原生架构名（如 amd64），不能使用 x64
GOOS="$OS" GOARCH="$GO_BUILD_ARCH" go build -ldflags="-s -w" -o "${BINARY_DIR}/${BINARY_NAME}" .

if [ -f "${BINARY_DIR}/${BINARY_NAME}" ]; then
    chmod +x "${BINARY_DIR}/${BINARY_NAME}"
    echo "[0/4] CodeActor binary built: ${BINARY_DIR}/${BINARY_NAME}"
else
    echo "[0/4] ERROR: Failed to build CodeActor binary!"
    exit 1
fi

echo ""

# Step 1: Build WebUI
echo "=== Step 1/4: Building WebUI ==="
cd "$WEBUI_DIR"
echo "[1/4] Installing webui dependencies..."
npm ci --silent 2>/dev/null || npm install --silent
echo "[1/4] Building React application..."
npm run build
echo "[1/4] WebUI build complete."
echo ""

# Step 2: Copy WebUI to VSCode extension
echo "=== Step 2/4: Copying WebUI to VSCode extension ==="
cd "$VSCODE_DIR"
node scripts/copy-webui.js
echo ""

# Step 3: Build VSCode extension
echo "=== Step 3/4: Building VSCode extension ==="
echo "[3/4] Bundling extension with esbuild..."
npm run bundle -- --production
echo "[3/4] Copying WASM files..."
npm run copy-wasm
echo "[3/4] Packaging extension as .vsix..."
npx vsce package --no-yarn --no-git-tag-version
echo ""

echo "============================================"
echo "  ✅ Build Complete!"
echo "  The .vsix file is in: $VSCODE_DIR/"
echo "============================================"
