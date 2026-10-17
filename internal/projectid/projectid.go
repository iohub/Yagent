// Package projectid 把项目的绝对路径折算成文件系统安全的目录名。
//
// 为什么单独成一个包：这个 ID 同时是 rollout 目录（`agents/director.go:462`）、
// artifact 目录（`agents/result.go:144`）与新落地的蓝图目录（`~/.yagent/data/blueprint/{id}`）
// 的主键。它原来住在 `agents/director` 里，而 `internal/blueprint` 不能 import 那个包
// （`agents` ↔ `director` ↔ 子系统之间靠方向守卫隔开），于是每个新子系统都会面临
// "要么反向依赖、要么再抄一份"的选择 —— 本仓已经因此有了第二份实现
// （`app.go:913-939` 的 `sha[:12]`，只管 shared_memory）。
//
// 算法与迁移前**逐字节相同**：闸门 `TestComputeProjectID` 钉死了具体期望串与长度，
// 抽包后必须一字不改仍然绿（I12）。
package projectid

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Compute 从项目路径计算文件系统安全的 projectID。
// 空路径返回 "default"；base 为 "." 或 "/" 时用 "root"；非字母数字替换为下划线；
// base 截断至 100 字符，后缀为整个路径 sha256 的前 8 个 hex 字符。
func Compute(projectPath string) string {
	if projectPath == "" {
		return "default"
	}
	base := filepath.Base(projectPath)
	if base == "." || base == "/" {
		base = "root"
	}
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, base)
	if len(sanitized) > 100 {
		sanitized = sanitized[:100]
	}
	h := sha256.Sum256([]byte(projectPath))
	shortHash := hex.EncodeToString(h[:])[:8]
	return sanitized + "_" + shortHash
}
