#!/bin/bash
set -e
REPO="${1:-/tmp/repos/rust_02_json_parser}"
cd "$REPO"
echo "=== Validating rust_02_json_parser ==="
cargo test 2>&1
echo "=== Validation complete ==="
