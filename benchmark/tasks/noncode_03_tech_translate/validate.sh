#!/bin/bash
set -e
REPO="${1:-/tmp/repos/noncode_03_tech_translate}"
cd "$REPO"
echo "=== Validating noncode_03_tech_translate ==="

FILE="SPEC_CN.md"
if [ ! -f "$FILE" ]; then
    echo "FAIL: $FILE not found"
    exit 1
fi

SIZE=$(wc -c < "$FILE")
if [ "$SIZE" -lt 1000 ]; then
    echo "FAIL: $FILE too short ($SIZE chars)"
    exit 1
fi

# Check for Chinese characters (Unicode range)
CN_COUNT=$(python3 -c "
import re
with open('$FILE') as f:
    text = f.read()
cn = len(re.findall(r'[\u4e00-\u9fff]', text))
print(cn)
")
if [ "$CN_COUNT" -lt 100 ]; then
    echo "FAIL: Too few Chinese characters ($CN_COUNT)"
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

check_term "限流器\|Rate.Limit"
check_term "熔断器\|Circuit.Break"
check_term "API.*网关\|API.Gateway"
check_term "故障\|failure"
check_term "监控\|monitor"

if [ $errors -gt 2 ]; then
    echo "FAIL: Too many missing terms ($errors)"
    exit 1
fi

echo "PASS: Translation validated ($SIZE chars, $CN_COUNT Chinese chars)"
