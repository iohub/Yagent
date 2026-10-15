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
//
// created 报告"这次真的生成了文件"。首启判定必须有这个信号：模板里的 api_key
// 是占位符而校验只查非空，少了它就只能猜这台机器是不是新的。
// 权限是 0600 而不是 0644 —— 里面可能有明文密钥，而 config.toml 常被人整份贴进提问里。
func EnsureConfigExists(path string) (created bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil // 已存在，不做任何操作
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("检查配置文件 %s 时出错: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(defaultConfigTemplate), 0600); err != nil {
		return false, fmt.Errorf("写入临时配置文件 %s 失败: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // 清理临时文件
		return false, fmt.Errorf("重命名配置文件 %s 失败: %w", path, err)
	}

	return true, nil
}
