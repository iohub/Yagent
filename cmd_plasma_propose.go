package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"yagent/internal/agents"
	"yagent/internal/config"

	"github.com/spf13/cobra"
)

// 本文件是 `yagent plasma propose` 的命令层：flag、可选注入、渲染。
// 编排本体在 cmd_plasma_propose_run.go，材料装配在 cmd_plasma_propose_input.go
// —— 拆开的理由是每个文件都守住 300 行，读的人不必在一屏半里找自己要改的那段。
//
// 三条带牙齿的：
//  1. **绝不会改任何 *.prompt.md**。内建 Agent 的 prompt 与 Go 代码是"改行为要同时改
//     两处"的耦合（根 AGENTS.md §4），所以这份产物只是提案，审批只改 JSON 里的 status。
//     本文件没有写 prompt 文件的代码路径 —— 这条保证靠测试的"产物只有 .md/.json"钉住。
//  2. **失败必须可见**（ADR-15 形状）：解释器/zipapp 不可用、Python exit 非 0、stdout
//     不是 JSON 信封，三种都返回带修复建议的 error，绝不 slog.Debug 了事。
//  3. **子进程层复用既有实现**：propose 没有 workdir 契约（Python 侧 CLI 是
//     `propose --in --out`），所以用 `agents.PlasmaCLI`（退出码 + stdout 一份 JSON）
//     而不是 `agents.ExecSpawner`（它**强制**追加 `--workdir`，argparse 会直接 exit 2）。

var plasmaProposeCmd = &cobra.Command{
	Use:   "propose --agent <name> [--out <dir>] [--json]",
	Short: "Offline proposal: a prompt diff for a built-in agent (never auto-applies)",
	Long: `Propose reads a built-in agent's prompt (the embedded file, byte for byte) plus the
tail of telemetry.jsonl, and asks the offline Python proposer for a diff proposal.

Two files land in --out: <agent>-<ts>.json (status is always "pending") and .md.
Nothing under internal/agents/*.prompt.md is ever written: the prompt and the Go code
must change together (AGENTS.md §4), so approval only flips the status field.`,
	Args: cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		failPlasma(runPlasmaWithLogging(func() error {
			return runPlasmaProposeCommand(plasmaFlags.proposeAgent, plasmaFlags.proposeOut,
				plasmaFlags.asJSON, os.Stdout)
		}))
	},
}

func init() {
	plasmaProposeCmd.Flags().StringVar(&plasmaFlags.proposeAgent, "agent", "",
		"内建 agent 名（"+strings.Join(agents.PromptAgents(), " / ")+"；默认 "+plasmaProposeDefaultAgent+"）")
	plasmaProposeCmd.Flags().StringVar(&plasmaFlags.proposeOut, "out", "",
		"提案目录（默认 [plasma] proposal_dir，即 ~/.yagent/agents/proposals）")
	plasmaProposeCmd.Flags().BoolVar(&plasmaFlags.asJSON, "json", false,
		"emit a JSON summary array instead of text")
}

// plasmaProposeOptions 是一次提案的全部外部输入。两个接缝（Runner / Available）是
// **必要**的：真 Python 进程在单测里不可复现（解释器版本、zipapp 是否重建过都会让用例
// 时红时绿），而"Python 不可用"那条分支只能靠注入才测得到。
type plasmaProposeOptions struct {
	// Agent 是内建 agent 名（遥测账本里的 delegate_ 后缀）。
	Agent string
	// Out 是提案目录；空 ⇒ [plasma] proposal_dir。
	Out string
	// Settings 是 [plasma] 段（解释器、超时等）。
	Settings config.PlasmaConfig
	// Pyz 是 plasma.pyz 路径；空 ⇒ agents.PlasmaZipappPath()（唯一来源）。
	Pyz string
	// Runner 为 nil ⇒ 生产实现 agents.PlasmaCLI。
	Runner plasmaPythonRunner
	// Available 为 nil ⇒ 按本机事实与配置判一次（ADR-15 唯一判据）。
	Available func() (bool, string)
	// Notice 收"账本缺失/跳过坏行"这类必须可见的旁注；nil ⇒ os.Stderr。
	Notice io.Writer
	// TempBase 是临时 workdir 的父目录；空 ⇒ os.TempDir()。
	TempBase string
	// TelemetryPath 空 ⇒ <store>/telemetry.jsonl。
	TelemetryPath string
}

// plasmaProposeOutput 是一次提案的观测结果（渲染在 renderPlasmaPropose）。
type plasmaProposeOutput struct {
	ID            string   `json:"id"`
	Agent         string   `json:"agent"`
	Status        string   `json:"status"`
	BaseHash      string   `json:"base_hash"`
	RuleIDs       []string `json:"rule_ids,omitempty"`
	JSONPath      string   `json:"json"`
	MDPath        string   `json:"md"`
	TelemetryPath string   `json:"telemetry_path"`
	Runs          int      `json:"runs"`
	Skipped       int      `json:"skipped"`
}

// runPlasmaProposeCommand 是 cobra 那一层之下的执行体（测试直接调它）。
func runPlasmaProposeCommand(agent, out string, asJSON bool, stdout io.Writer) error {
	settings, _ := plasmaRuntime()
	ctx, cancel := context.WithTimeout(context.Background(), plasmaProposeTimeout(settings))
	defer cancel()
	result, err := runPlasmaPropose(ctx, plasmaProposeOptions{
		Agent:    agent,
		Out:      out,
		Settings: settings,
	})
	if err != nil {
		return err
	}
	return renderPlasmaPropose(stdout, result, asJSON)
}

// plasmaProposeSummary 是 --json 的数组元素（字段：agent/path/base_hash/status）。
// 刻意是**数组**：一次提案今天只有一个产物，但调用方（TUI/脚本）按数组写就不会在
// 将来多产物时被迫改解析。
type plasmaProposeSummary struct {
	ID       string `json:"id,omitempty"`
	Agent    string `json:"agent"`
	Path     string `json:"path"`
	Markdown string `json:"markdown,omitempty"`
	BaseHash string `json:"base_hash"`
	Status   string `json:"status"`
}

// renderPlasmaPropose 是唯一的输出点：文本给人看，--json 给管道。
func renderPlasmaPropose(out io.Writer, result plasmaProposeOutput, asJSON bool) error {
	if asJSON {
		payload := []plasmaProposeSummary{{
			ID:       result.ID,
			Agent:    result.Agent,
			Path:     result.JSONPath,
			Markdown: result.MDPath,
			BaseHash: result.BaseHash,
			Status:   result.Status,
		}}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("渲染 --json 输出失败: %w", err)
		}
		_, err = out.Write(append(raw, '\n'))
		return err
	}
	_, err := fmt.Fprintf(out,
		"plasma propose ok\n"+
			"  id:     %s\n"+
			"  agent:  %s\n"+
			"  status: %s（审批只改这个字段，不会改写任何 *.prompt.md）\n"+
			"  json:   %s\n"+
			"  md:     %s\n"+
			"telemetry: %s（%d 条样本，跳过 %d 行）\n",
		result.ID, result.Agent, result.Status, result.JSONPath, result.MDPath,
		result.TelemetryPath, result.Runs, result.Skipped)
	return err
}
