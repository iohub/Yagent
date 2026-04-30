#!/bin/bash
set -e
REPO="${1:-/tmp/repos/rust_05_command_pipeline}"
cd "$REPO"
echo "=== Validating rust_05_command_pipeline ==="
cargo test 2>&1
echo "=== Validation complete ==="
