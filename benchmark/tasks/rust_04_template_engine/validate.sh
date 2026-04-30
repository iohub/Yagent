#!/bin/bash
set -e
REPO="${1:-/tmp/repos/rust_04_template_engine}"
cd "$REPO"
echo "=== Validating rust_04_template_engine ==="
cargo test 2>&1
echo "=== Validation complete ==="
