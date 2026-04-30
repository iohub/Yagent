#!/bin/bash
set -e
REPO="${1:-/tmp/repos/python_02_async_eventloop}"
cd "$REPO"
echo "=== Validating python_02_async_eventloop ==="
python3 -m pytest tests/ -v 2>&1
echo "=== Validation complete ==="
