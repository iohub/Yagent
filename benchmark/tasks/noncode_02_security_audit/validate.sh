#!/bin/bash
set -e
REPO="${1:-/tmp/repos/noncode_02_security_audit}"
cd "$REPO"
echo "=== Validating noncode_02_security_audit ==="

FILE="SECURITY_AUDIT.md"
if [ ! -f "$FILE" ]; then
    echo "FAIL: $FILE not found"
    exit 1
fi

SIZE=$(wc -c < "$FILE")
if [ "$SIZE" -lt 500 ]; then
    echo "FAIL: $FILE too short ($SIZE chars)"
    exit 1
fi

errors=0
check_term() {
    if grep -qi "$1" "$FILE"; then
        echo "  ✓ found: $1"
    else
        echo "  ✗ missing: $1"
        errors=$((errors + 1))
    fi
}

check_term "SQL"
check_term "JWT\|jwt\|token"
check_term "明文\|plaintext\|plain.text"
check_term "hardcode\|硬编码\|hard.code"
check_term "CORS"
check_term "输入验证\|input valid\|input.valid"
check_term "secret\|Secret\|密钥\|密码"

if [ $errors -gt 3 ]; then
    echo "FAIL: Too many missing terms ($errors)"
    exit 1
fi

echo "PASS: Security audit validated ($SIZE chars)"
