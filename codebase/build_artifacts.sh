#!/bin/bash
set -e

# Define targets
MACOS_INTEL="x86_64-apple-darwin"
MACOS_ARM="aarch64-apple-darwin"
LINUX_INTEL="x86_64-unknown-linux-gnu"
LINUX_ARM="aarch64-unknown-linux-gnu"

BINARY_NAME="codeactor-codebase"
OUTPUT_DIR="release_artifacts"

# Ensure output directory exists
mkdir -p "$OUTPUT_DIR"

echo "=========================================="
echo "Build Script for CodeActor Codebase"
echo "=========================================="

# Check for prerequisites
if ! command -v cargo &> /dev/null; then
    echo "Error: 'cargo' is not installed."
    exit 1
fi

# Function to check and install cross if needed
check_cross() {
    if ! command -v cross &> /dev/null; then
        echo "Tool 'cross' is not found. Installing it now via cargo..."
        cargo install cross
    fi
}

# Function to build for a specific target
build_target() {
    local target=$1
    local use_cross=$2
    
    echo ""
    echo ">>> Building for target: $target"
    
    if [ "$use_cross" = true ]; then
        check_cross
        # Check if Docker is running (cross requires Docker)
        if ! docker info &> /dev/null; then
            echo "Error: Docker is not running or not accessible. 'cross' requires Docker."
            exit 1
        fi
        
        echo "Using 'cross' for build..."
        cross build --release --target "$target"
    else
        echo "Using 'cargo' for build..."
        # Add target via rustup if not present
        if ! rustup target list --installed | grep -q "^$target$"; then
            echo "Adding target $target via rustup..."
            rustup target add "$target"
        fi
        
        cargo build --release --target "$target"
    fi
    
    # Verify and move artifact
    local src_path="target/$target/release/$BINARY_NAME"
    local dest_path="$OUTPUT_DIR/${BINARY_NAME}-${target}"
    
    if [ -f "$src_path" ]; then
        cp "$src_path" "$dest_path"
        echo "✅ Build successful. Artifact: $dest_path"
    else
        echo "❌ Build failed. Binary not found at $src_path"
        exit 1
    fi
}

# Detect Host OS
HOST_OS=$(uname -s)
echo "Host OS: $HOST_OS"

# 1. macOS Builds
if [ "$HOST_OS" = "Darwin" ]; then
    # On macOS, we can build both Intel and ARM natively/via Rosetta
    build_target "$MACOS_INTEL" false
    build_target "$MACOS_ARM" false
else
    # On Linux, building for macOS is complicated. Skipping for now in this script.
    echo "⚠️  Skipping macOS targets (Building macOS binaries requires a macOS host)."
fi

# 2. Linux Builds
# Always use cross for Linux targets to ensure compatibility (glibc versions etc.)
build_target "$LINUX_INTEL" true
build_target "$LINUX_ARM" true

echo ""
echo "=========================================="
echo "All builds completed successfully!"
echo "Artifacts are available in '$OUTPUT_DIR/'"
ls -lh "$OUTPUT_DIR/"
echo "=========================================="
