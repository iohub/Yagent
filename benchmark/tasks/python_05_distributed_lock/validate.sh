#!/bin/bash
set -e
REPO="${1:-/tmp/repos/python_05_distributed_lock}"
cd "$REPO"
echo "=== Validating python_05_distributed_lock ==="
python3 -m pytest tests/ -v 2>&1
echo "=== Validation complete ==="
