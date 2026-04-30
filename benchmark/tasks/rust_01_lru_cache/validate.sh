#!/bin/bash
set -e
REPO="${1:-/tmp/repos/rust_01_lru_cache}"
cd "$REPO"
echo "=== Validating rust_01_lru_cache ==="
cargo test 2>&1
echo "=== Validation complete ==="
