#!/bin/bash
set -e
REPO="${1:-/tmp/repos/python_01_di_framework}"
cd "$REPO"
echo "=== Validating python_01_di_framework ==="
python3 -m pytest tests/ -v 2>&1
echo "=== Validation complete ==="
