#!/bin/bash
set -e
REPO="${1:-/tmp/repos/python_03_regex_engine}"
cd "$REPO"
echo "=== Validating python_03_regex_engine ==="
python3 -m pytest tests/ -v 2>&1
echo "=== Validation complete ==="
