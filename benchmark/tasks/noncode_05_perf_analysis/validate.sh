#!/bin/bash
set -e
REPO="${1:-/tmp/repos/noncode_05_perf_analysis}"
cd "$REPO"
echo "=== Validating noncode_05_perf_analysis ==="

FILE="PERF_ANALYSIS.md"
if [ ! -f "$FILE" ]; then
    echo "FAIL: $FILE not found"
    exit 1
fi

SIZE=$(wc -c < "$FILE")
if [ "$SIZE" -lt 600 ]; then
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

check_term "N+1\|N+1\|N.1"
check_term "O(n\|O.n.\|平方\|quadratic"
check_term "cache\|缓存"
check_term "memory\|内存\|memory.leak"
check_term "优化\|optimize\|improve"
check_term "regex\|正则\|re.compile"

if [ $errors -gt 3 ]; then
    echo "FAIL: Too many missing terms ($errors)"
    exit 1
fi

echo "PASS: Performance analysis validated ($SIZE chars)"
