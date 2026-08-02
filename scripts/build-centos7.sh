#!/bin/bash
set -euxo pipefail

# 在 CentOS 7 容器内部执行的构建脚本
# 工作目录通过 docker run -w /workspace 设置

echo "=== Step 1: Fix CentOS 7 yum repositories ==="
sed -i 's/mirrorlist/#mirrorlist/g' /etc/yum.repos.d/CentOS-*.repo
sed -i 's|#baseurl=http://mirror.centos.org|baseurl=http://vault.centos.org|g' /etc/yum.repos.d/CentOS-*.repo
yum clean all && yum makecache

echo "=== Step 2: Install system dependencies and devtoolset-9 ==="
yum install -y centos-release-scl epel-release
yum install -y devtoolset-9-gcc devtoolset-9-gcc-c++ devtoolset-9-binutils \
  make cmake3 openssl-devel zlib-devel perl curl wget git which unzip
ln -sf /usr/bin/cmake3 /usr/bin/cmake

echo "=== Step 3: Install Rust ==="
source /opt/rh/devtoolset-9/enable
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y \
  --default-toolchain stable --profile minimal
export PATH="$HOME/.cargo/bin:$PATH"
rustc --version
cargo --version

echo "=== Step 4: Install Go ==="
GO_VERSION=$(grep '^go ' go.mod | awk '{print $2}')
echo "Parsed Go version: ${GO_VERSION}"
curl -fLo /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" || \
  curl -fLo /tmp/go.tar.gz "https://go.dev/dl/go1.23.8.linux-amd64.tar.gz"
tar -C /usr/local -xzf /tmp/go.tar.gz
rm -f /tmp/go.tar.gz
export PATH="/usr/local/go/bin:$PATH"
go version

echo "=== Step 5: Install protoc ==="
curl -fLo /tmp/protoc.zip "https://github.com/protocolbuffers/protobuf/releases/download/v25.3/protoc-25.3-linux-x86_64.zip"
unzip /tmp/protoc.zip -d /usr/local
rm -f /tmp/protoc.zip
protoc --version

echo "=== Step 6: Build Rust codeseek ==="
source /opt/rh/devtoolset-9/enable
cd codeseek/rust-core
cargo build --release

echo "=== Step 7: Prepare embedded binaries ==="
cd ..
mkdir -p dist/bin
cp codeseek/rust-core/target/release/codeseek dist/bin/

# Download fzf
curl -fLo /tmp/fzf.tar.gz "https://github.com/junegunn/fzf/releases/download/v0.73.1/fzf-0.73.1-linux_amd64.tar.gz"
tar -xzf /tmp/fzf.tar.gz -C /tmp
mv /tmp/fzf dist/bin/fzf
rm -f /tmp/fzf.tar.gz

# Download ripgrep (musl build for static linking)
curl -fLo /tmp/rg.tar.gz "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-x86_64-unknown-linux-musl.tar.gz"
mkdir -p /tmp/rg_extract
tar -xzf /tmp/rg.tar.gz -C /tmp/rg_extract
find /tmp/rg_extract -name rg -type f -exec mv {} dist/bin/rg \;
rm -rf /tmp/rg_extract /tmp/rg.tar.gz

chmod +x dist/bin/codeseek dist/bin/fzf dist/bin/rg

echo "=== Step 8: Build Go codeactor ==="
export PATH="/usr/local/go/bin:$PATH"
go build -ldflags="-s -w" -o codeactor .

echo "=== Build completed ==="
ls -lh codeactor dist/bin/
