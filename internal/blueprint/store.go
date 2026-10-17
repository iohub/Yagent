package blueprint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"yagent/internal/projectid"
)

const (
	// RootEnv 覆盖存储根，只为测试与"把数据放在别的盘上"这一条真实需求存在
	// （与 YAGENT_NOTES_ROOT 同族）。
	RootEnv = "YAGENT_BLUEPRINT_ROOT"

	yagentDirName    = ".yagent"
	dataDirName      = "data"
	blueprintDirName = "blueprint"
)

var unsafeIDChars = regexp.MustCompile(`[^A-Za-z0-9_.\-]`)

// SanitizeProjectID 把调用方传来的 ID 收敛成安全目录名：非法字符换下划线、空值报错。
// 这里**不**像 blackboard 那样"空值自动生成一个 adhoc id" —— 蓝图的目录名是项目的身份，
// 猜一个出来就等于把两份蓝图混进同一个目录。
func SanitizeProjectID(id string) (string, error) {
	clean := unsafeIDChars.ReplaceAllString(strings.TrimSpace(id), "_")
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("blueprint: 非法 projectID %q", id)
	}
	if len(clean) > 128 {
		clean = clean[:128]
	}
	return clean, nil
}

// Root 返回 {YAGENT_BLUEPRINT_ROOT 或 ~/.yagent/data/blueprint}/{projectID}。
//
// 与 `blackboard.NotesRootDir()` 有意不同的一处：**拿不到 home 时报错而不是退回当前目录**。
// 当前目录就是用户的项目仓库，退回去写等于同时违反 I7（存储根不在项目内）与
// "绝不往用户仓库写" —— 蓝图的真源必须在 ~/.yagent，进仓库的那一份是显式导出的渲染产物（R8）。
func Root(projectID string) (string, error) {
	id, err := SanitizeProjectID(projectID)
	if err != nil {
		return "", err
	}
	if override := os.Getenv(RootEnv); override != "" {
		return filepath.Join(override, id), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("blueprint: 找不到用户主目录，蓝图存储根不能退回当前目录: %w", err)
	}
	return filepath.Join(home, yagentDirName, dataDirName, blueprintDirName, id), nil
}

// Prepare 是装配层唯一该调的入口：项目根护栏（I16）→ projectID → 存储根 → 存储根护栏（I7）。
// 顺序有讲究：先拒绝危险的项目根，再算 ID —— 反过来会把 "$HOME" 折算成一个目录名去创建。
func Prepare(projectPath string) (string, error) {
	if err := CheckProjectRoot(projectPath); err != nil {
		return "", err
	}
	root, err := Root(projectid.Compute(projectPath))
	if err != nil {
		return "", err
	}
	if err := CheckStoreRoot(root, projectPath); err != nil {
		return "", err
	}
	return root, nil
}
