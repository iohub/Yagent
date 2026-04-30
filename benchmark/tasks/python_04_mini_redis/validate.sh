#!/bin/bash
set -e
REPO="${1:-/tmp/repos/python_04_mini_redis}"
cd "$REPO"
echo "=== Validating python_04_mini_redis ==="
python3 -m pytest tests/ -v 2>&1
echo "=== Validation complete ==="
