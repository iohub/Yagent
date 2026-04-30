#!/bin/bash
set -e
REPO="${1:-/tmp/repos/rust_03_matrix_multiply}"
cd "$REPO"
echo "=== Validating rust_03_matrix_multiply ==="
cargo test 2>&1
echo "=== Validation complete ==="
