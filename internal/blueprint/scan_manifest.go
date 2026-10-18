package blueprint

import (
	"encoding/json"
	"sort"
	"strings"
)

// 五个纯文本解析器（§4.3 的 manifest 事实来源）。全部确定性输出：排序、去重、零随机。
// 它们只做"读名字"，绝不执行任何读到的命令 —— 蓝图派生层不跑构建（§4.3）。

// parseGoModule 取 go.mod 的 module 值。没有则返回空串（非 Go 仓库 —— 边为零不是错误，I9）。
func parseGoModule(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"'`)
		}
	}
	return ""
}

// parseGitmodules 取 .gitmodules 里每个子节的 path = 值（相对仓库路径，排序）。
// 这条只喂目录分支的跳过判定（§4.1 实测 bug：写在文件分支会多出 14 片假模块）。
func parseGitmodules(data []byte) []string {
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		rest := strings.TrimPrefix(trimmed, "path")
		if rest == trimmed { // 行首不是 path —— url/branch 等行不碰
			continue
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(rest[1:]), `"'`)
		if value != "" {
			paths = append(paths, value)
		}
	}
	sort.Strings(paths)
	return paths
}

// parsePackageJSON 取 package.json 的 main、bin、scripts 名字（§4.3：Node 入口与脚本）。
// 解析失败静默跳过并保持确定 —— manifest 坏了不该让扫描失败，但也绝不能猜。
func parsePackageJSON(data []byte) (main string, bins, scripts []string) {
	var doc struct {
		Main    string            `json:"main"`
		Bin     json.RawMessage   `json:"bin"`
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", nil, nil
	}
	main = doc.Main
	// bin 可以是 string 或 {"name": "path"}，两种都要收。
	var one string
	if err := json.Unmarshal(doc.Bin, &one); err == nil && one != "" {
		bins = append(bins, one)
	} else {
		var many map[string]string
		if err := json.Unmarshal(doc.Bin, &many); err == nil {
			for name := range many {
				bins = append(bins, name)
			}
		}
	}
	for name := range doc.Scripts {
		scripts = append(scripts, name)
	}
	sort.Strings(bins)
	sort.Strings(scripts)
	return main, bins, scripts
}

// parseCargoBins 取 Cargo.toml 里每个 [[bin]] 节的 name（§4.3：Rust 入口）。
func parseCargoBins(data []byte) []string {
	var bins []string
	inBin := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "[[bin]]":
			inBin = true
		case strings.HasPrefix(trimmed, "["):
			inBin = false // 任何其他节（[package] 等）都终止当前 [[bin]]
		case inBin:
			if name, ok := cutTomlStringKey(trimmed, "name"); ok {
				bins = append(bins, name)
			}
		}
	}
	sort.Strings(bins)
	return bins
}

// cutTomlStringKey 匹配 `key = "value"`（容忍空格），取出去引号后的 value。
// TrimPrefix 只在行首匹配，所以 `nameX = ...` 不会被当成 name —— 不匹配返回 false。
func cutTomlStringKey(line, key string) (string, bool) {
	rest := strings.TrimPrefix(line, key)
	if rest == line {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "=") {
		return "", false
	}
	value := strings.Trim(strings.TrimSpace(rest[1:]), `"'`)
	if value == "" {
		return "", false
	}
	return value, true
}

// parseMakefileTargets 取根 Makefile 的 target 名（§4.3：构建/测试命令，只列名字）。
// 规则：行首无空白、非注释、非 `.` 开头（.PHONY 等特殊目标不算）、`name:` 且不是 `:=` 赋值。
// 超过 cap 截断并置 truncated —— 截断必须被说出来（I3/I4 口径）。
func parseMakefileTargets(data []byte, capN int) (targets []string, truncated bool) {
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' || line[0] == '.' {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		if colon+1 < len(line) && (line[colon+1] == '=' || line[colon+1] == ':') {
			continue // := 与 ::= 是赋值不是 target
		}
		name := strings.TrimSpace(line[:colon])
		if name == "" || strings.ContainsAny(name, " $(){}") {
			continue // 多 target 一行、变量引用 —— 不猜
		}
		if !seen[name] {
			seen[name] = true
			targets = append(targets, name)
		}
	}
	sort.Strings(targets)
	if len(targets) > capN {
		targets = targets[:capN]
		truncated = true
	}
	return targets, truncated
}
