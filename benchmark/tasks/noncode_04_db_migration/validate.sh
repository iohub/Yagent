#!/bin/bash
set -e
REPO="${1:-/tmp/repos/noncode_04_db_migration}"
cd "$REPO"
echo "=== Validating noncode_04_db_migration ==="

FILE="MIGRATION_PLAN.md"
if [ ! -f "$FILE" ]; then
    echo "FAIL: $FILE not found"
    exit 1
fi

SIZE=$(wc -c < "$FILE")
if [ "$SIZE" -lt 800 ]; then
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

check_term "迁移\|migration\|migrate"
check_term "回滚\|rollback"
check_term "users.*profiles\|profiles.*users"
check_term "roles"
check_term "CREATE\|INSERT\|SELECT\|ALTER"
check_term "验证\|verif\|check\|valid"

if [ $errors -gt 2 ]; then
    echo "FAIL: Too many missing terms ($errors)"
    exit 1
fi

echo "PASS: Migration plan validated ($SIZE chars)"
