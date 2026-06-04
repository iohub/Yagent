#!/bin/bash

# 构建React应用并复制到扩展目录

echo "Building CodeActor WebUI..."

# 进入webui目录
cd webui

# 安装依赖
echo "Installing dependencies..."
# npm install

# 构建React应用
echo "Building React application..."
npm run build

# 确保目标目录存在
echo "Creating target directory..."
mkdir -p ../dist

# 复制构建文件到扩展目录
echo "Copying build files to extension..."
cp -r build/* ../dist/

# 复制package.json中的版本信息
cp package.json ../dist/

echo "WebUI build completed successfully!"
echo "Files copied to: ../dist/"