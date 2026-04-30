#!/bin/bash
set -e
REPO="${1:-/tmp/repos/noncode_01_arch_docs}"
cd "$REPO"
echo "=== Validating noncode_01_arch_docs ==="

ARCH_FILE="ARCHITECTURE.md"
if [ ! -f "$ARCH_FILE" ]; then
    echo "FAIL: $ARCH_FILE not found"
    exit 1
fi

SIZE=$(wc -c < "$ARCH_FILE")
if [ "$SIZE" -lt 500 ]; then
    echo "FAIL: $ARCH_FILE too short ($SIZE chars, need >500)"
    exit 1
fi

# Check for expected key terms
errors=0
check_term() {
    if grep -qi "$1" "$ARCH_FILE"; then
        echo "  ✓ found: $1"
    else
        echo "  ✗ missing: $1"
        errors=$((errors + 1))
    fi
}

check_term "handler"
check_term "service"
check_term "repo"
check_term "middleware"
check_term "JWT\|jwt"
check_term "SQLite"
check_term "数据流\|data flow\|数据.*流"
check_term "依赖\|dependency\|depend"

if [ $errors -gt 2 ]; then
    echo "FAIL: Too many missing terms ($errors)"
    exit 1
fi

echo "PASS: Architecture documentation validated ($SIZE chars)"
