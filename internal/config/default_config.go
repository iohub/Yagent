package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed default_config.toml
var defaultConfigTemplate string

// EnsureConfigExists 检查指定路径的配置文件是否存在，如果不存在则自动生成模板配置。
// 生成的配置来自嵌入到二进制中的 default_config.toml。
// 采用原子写入方式（先写 .tmp 文件，再 rename）避免写入中断导致文件损坏。
func EnsureConfigExists(path string) error {
	// 检查文件是否已存在
	if _, err := os.Stat(path); err == nil {
		return nil // 已存在，不做任何操作
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查配置文件 %s 时出错: %w", path, err)
	}

	// 创建父目录
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
	}

	// 原子写入：先写临时文件，再重命名
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(defaultConfigTemplate), 0644); err != nil {
		return fmt.Errorf("写入临时配置文件 %s 失败: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // 清理临时文件
		return fmt.Errorf("重命名配置文件 %s 失败: %w", path, err)
	}

	return nil
}
