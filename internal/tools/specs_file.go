package tools

import (
	"fmt"

	"yagent/internal/toolspec"
)

// 本文件是 Spec 声明的公共支撑。工具的能力标签、频率档、授权要求全部集中在
// specs_*.go，与实现同包；Registry.Validate() 保证 schemas.json 与此处声明双向 1:1，
// 所以"声明了没人实现"和"实现了没声明"都不再可能静默存在。

// require 把"依赖没装配"变成 Build 错误。Surface 会把该工具跳过并记录原因，
// 而不是注册一个调用即 panic 的适配器。
func require[T any](v T, what string) error {
	if any(v) == nil {
		return fmt.Errorf("%s 未装配", what)
	}
	return nil
}

// fileSystemPathParams 是文件类工具的工作区边界检查参数名。
// 必须与 schemas.json 里对应的 properties 同名，Validate() 会校验。
//
// delete_file 历史上还挂着 "target_file"，但它的 schema 里从来只有 file_paths ——
// 那条是死声明，本次由 Validate() 抓出来后删掉。
var fileSystemPathParams = map[string][]string{
	"create_file":            {"target_file"},
	"search_replace_in_file": {"file_path"},
	"delete_file":            {"file_paths"},
	"rename_file":            {"file_path", "rename_file_path"},
}

func init() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "read_file",
		Effect:    toolspec.EffectFSRead,
		Frequency: toolspec.Hot,
		Trunc:     toolspec.TruncFirst,
		Display:   toolspec.DisplayCompact,
		Brief:     "按行预算读取文件，单次最多 1500 行/50KB",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Files != nil, "文件工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Files, "Files"); err != nil {
				return nil, err
			}
			return d.Files.ExecuteReadFile, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name: "list_dir",
		// 结果里路径的形态（蓝图证据集的出参侧按它分派，见 toolspec.Spec.ResultPaths）。
		ResultPaths: toolspec.ResultPathsBare,
		Effect:      toolspec.EffectFSRead,
		Frequency:   toolspec.Hot,
		Trunc:       toolspec.TruncNormal,
		Display:     toolspec.DisplayCompact,
		Brief:       "列出目录下的文件",
		Available:   func(d *toolspec.Deps) (bool, string) { return d.Files != nil, "文件工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Files, "Files"); err != nil {
				return nil, err
			}
			return d.Files.ExecuteListDir, nil
		},
	})

	// print_dir_tree 单次声明 1162B 却很少用到第二次，是 deferred 的首选。
	toolspec.MustRegister(toolspec.Spec{
		Name: "print_dir_tree",
		// 结果里路径的形态（蓝图证据集的出参侧按它分派，见 toolspec.Spec.ResultPaths）。
		ResultPaths: toolspec.ResultPathsBare,
		Effect:      toolspec.EffectFSRead,
		Frequency:   toolspec.Cold,
		Trunc:       toolspec.TruncFirst,
		Display:     toolspec.DisplayCompact,
		Brief:       "打印目录树（自动忽略构建产物）",
		Available:   func(d *toolspec.Deps) (bool, string) { return d.Files != nil, "文件工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Files, "Files"); err != nil {
				return nil, err
			}
			return d.Files.ExecutePrintDirTree, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:       "create_file",
		Effect:     toolspec.EffectFSWrite,
		Frequency:  toolspec.Hot,
		Mutates:    true,
		NeedsAuth:  true,
		PathParams: fileSystemPathParams["create_file"],
		Trunc:      toolspec.TruncFirst,
		Brief:      "新建文件（已存在则失败）",
		Available:  func(d *toolspec.Deps) (bool, string) { return d.Files != nil, "文件工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Files, "Files"); err != nil {
				return nil, err
			}
			return d.Files.ExecuteCreateFile, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:       "delete_file",
		Effect:     toolspec.EffectFSWrite,
		Frequency:  toolspec.Hot,
		Mutates:    true,
		NeedsAuth:  true,
		PathParams: fileSystemPathParams["delete_file"],
		Trunc:      toolspec.TruncNormal,
		Display:    toolspec.DisplayCompact,
		Brief:      "删除文件（可一次多个）",
		Available:  func(d *toolspec.Deps) (bool, string) { return d.Files != nil, "文件工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Files, "Files"); err != nil {
				return nil, err
			}
			return d.Files.ExecuteDeleteFile, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:       "rename_file",
		Effect:     toolspec.EffectFSWrite,
		Frequency:  toolspec.Hot,
		Mutates:    true,
		NeedsAuth:  true,
		PathParams: fileSystemPathParams["rename_file"],
		Trunc:      toolspec.TruncNormal,
		Display:    toolspec.DisplayCompact,
		Brief:      "移动或重命名文件",
		Available:  func(d *toolspec.Deps) (bool, string) { return d.Files != nil, "文件工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Files, "Files"); err != nil {
				return nil, err
			}
			return d.Files.ExecuteRenameFile, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:       "search_replace_in_file",
		Effect:     toolspec.EffectFSWrite,
		Frequency:  toolspec.Hot,
		Mutates:    true,
		NeedsAuth:  true,
		PathParams: fileSystemPathParams["search_replace_in_file"],
		Trunc:      toolspec.TruncFirst,
		Brief:      "精确唯一匹配替换并返回 diff",
		Available:  func(d *toolspec.Deps) (bool, string) { return d.Edit != nil, "编辑工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Edit, "Edit"); err != nil {
				return nil, err
			}
			return d.Edit.ExecuteReplaceBlock, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name: "search_by_regex",
		// 结果里路径的形态（蓝图证据集的出参侧按它分派，见 toolspec.Spec.ResultPaths）。
		ResultPaths: toolspec.ResultPathsRipgrep,
		Effect:      toolspec.EffectSearchRegex,
		Frequency:   toolspec.Hot,
		Trunc:       toolspec.TruncFirst,
		Display:     toolspec.DisplayCompact,
		Brief:       "ripgrep 全文正则搜索",
		Available:   func(d *toolspec.Deps) (bool, string) { return d.Search != nil, "搜索工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Search, "Search"); err != nil {
				return nil, err
			}
			return d.Search.ExecuteGrepSearch, nil
		},
	})

	// run_bash 的危险性由 LLM 的 is_dangerous + 工作区外路径判定，
	// 路径参数不是文件路径而是命令串，所以 PathParams 为空（见 workspace_guard 的 bash 分支）。
	toolspec.MustRegister(toolspec.Spec{
		Name:      "run_bash",
		Effect:    toolspec.EffectShell,
		Frequency: toolspec.Hot,
		Mutates:   true,
		NeedsAuth: true,
		Trunc:     toolspec.TruncFirst,
		Brief:     "执行 bash 命令",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Sys != nil, "系统工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Sys, "Sys"); err != nil {
				return nil, err
			}
			return d.Sys.ExecuteRunBash, nil
		},
	})
}
