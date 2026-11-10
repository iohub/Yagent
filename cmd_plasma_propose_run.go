package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/agents"
	"yagent/internal/config"
	"yagent/internal/plasma"
)

// 本文件是 `yagent plasma propose` 的**编排本体**：校验 → 可用性 → 目录 → 材料 →
// 子进程 → 信封校验。一步一道门，顺序即诊断顺序。
//
// 为什么每一步都要"先拒后干"：propose 会往磁盘写两个文件。把可能失败的判定放在
// 写文件之后，失败时留下的就是"半个提案"——而 TUI 的 `/proposals` 会把那个半个
// 文件当成一条真提案列出来。

// plasmaProposeEnvelope 是 propose.py 的 stdout 契约（一份 JSON，不多一行）。
// 缺任何一个必需键都当失败：把"提案没写出来"读成"提案是空的"，是最难查的一种静默。
type plasmaProposeEnvelope struct {
	ID       string   `json:"id"`
	Agent    string   `json:"agent"`
	Status   string   `json:"status"`
	BaseHash string   `json:"base_hash"`
	RuleIDs  []string `json:"rule_ids"`
	JSON     string   `json:"json"`
	MD       string   `json:"md"`
}

// plasmaProposeDefaultAgent 是 --agent 缺省值。选 coding 的理由是遥测里它最常被委派
// （也就最常需要 prompt 调整），而不是"它最特殊"。
const plasmaProposeDefaultAgent = "coding"

// runPlasmaPropose 是编排本体。
func runPlasmaPropose(ctx context.Context, opts plasmaProposeOptions) (plasmaProposeOutput, error) {
	var result plasmaProposeOutput

	// 0) --agent 可选：缺省是最常被改的那个 Agent（coding）。默认值必须**打出来**
	//    （文本输出里有 agent 一行），否则"我明明在给 repo 提建议"会变成一个哑谜。
	if strings.TrimSpace(opts.Agent) == "" {
		opts.Agent = plasmaProposeDefaultAgent
	}

	// 1) 未知 agent 是**用法错**（exit 2），必须在动任何文件之前拒掉。
	info, ok := agents.LookupPrompt(opts.Agent)
	if !ok {
		return result, newUsageError("未知内建 Agent %q；可用：%s",
			opts.Agent, strings.Join(agents.PromptAgents(), " / "))
	}

	// 2) 能力判据（ADR-15）：不可用就明说原因与修法，不假装跑一次再报 exit 1。
	pyz := strings.TrimSpace(opts.Pyz)
	if pyz == "" {
		resolved, err := agents.PlasmaZipappPath()
		if err != nil {
			return result, fmt.Errorf("plasma propose 不可用：定位不到 plasma.pyz（%v）；"+
				"修复：重编译/重装 yagent（内嵌的 dist/bin/plasma.pyz 要落到 ~/.yagent/bin）", err)
		}
		pyz = resolved
	}
	available := opts.Available
	if available == nil {
		// 判据只有一份：plasmaAvailableWith 同时管 [plasma] enable、解释器、zipapp
		// 三件事，且原因文案自带"修复：…"。在这里另写一份检查必然漂。
		available = func() (bool, string) { return agents.PlasmaAvailableWith(opts.Settings, pyz) }
	}
	if usable, why := available(); !usable {
		return result, fmt.Errorf("plasma propose 不可用：%s", why)
	}

	// 3) 提案目录：缺省取配置，不存在时建 0700（已存在的目录不改权限 —— 那是用户的）。
	outDir := strings.TrimSpace(opts.Out)
	if outDir == "" {
		outDir = config.PlasmaProposalDir()
	}
	if err := ensurePlasmaProposeDir(outDir); err != nil {
		return result, err
	}

	// 4) 材料：prompt 全文 + 遥测摘要，全部由 Go 写（Python 不碰工作区/不持密钥）。
	telemetryPath := strings.TrimSpace(opts.TelemetryPath)
	if telemetryPath == "" {
		telemetryPath = filepath.Join(config.PlasmaStoreDir(), "telemetry.jsonl")
	}
	input, err := buildPlasmaProposeInput(info, telemetryPath)
	if err != nil {
		return result, err
	}
	noticePlasmaProposeLedger(opts.Notice, input.Telemetry, telemetryPath)

	// 5) 临时 workdir：0700 目录 + 0600 输入文件，跑完整个目录删除。
	//    prompt 全文会短暂落盘（Python 不读工作区 ⇒ 只能这样递），残余风险靠
	//    "0600 文件 + 0700 目录 + 立即删除"收窄，是显式接受的取舍。
	workdir, err := plasma.NewWorkdir(opts.TempBase)
	if err != nil {
		return result, err
	}
	defer func() { _ = workdir.Cleanup() }()
	inputPath := workdir.File(plasmaProposeInputFileName)
	if err := writeJSONFile(inputPath, input); err != nil {
		return result, fmt.Errorf("写提案输入 %s 失败: %w", inputPath, err)
	}

	runner := opts.Runner
	if runner == nil {
		runner = &agents.PlasmaCLI{Python: opts.Settings.Python, Pkg: pyz}
	}
	raw, err := runner.Run(ctx, "propose", "--in", inputPath, "--out", outDir)
	if err != nil {
		return result, plasmaProposeRunError(err)
	}

	// 6) 信封：stdout 上必须是一份可解析的 JSON，且指向两个真实存在的文件。
	envelope, err := parsePlasmaProposeEnvelope(raw)
	if err != nil {
		return result, err
	}
	if envelope.Agent != "" && envelope.Agent != info.Agent {
		return result, fmt.Errorf("plasma propose 报告了错误的 agent：信封 %q，期望 %q",
			envelope.Agent, info.Agent)
	}
	jsonPath := resolvePlasmaProposalPath(outDir, envelope.JSON)
	mdPath := resolvePlasmaProposalPath(outDir, envelope.MD)
	for _, path := range []string{jsonPath, mdPath} {
		if _, statErr := os.Stat(path); statErr != nil {
			return result, fmt.Errorf("plasma propose 报告的提案文件不存在：%s（%v）；"+
				"修复：检查 --out %s 的写权限，或重跑以确认 Python 侧没有中途失败", path, statErr, outDir)
		}
	}
	return plasmaProposeOutput{
		ID:            envelope.ID,
		Agent:         info.Agent,
		Status:        envelope.Status,
		BaseHash:      envelope.BaseHash,
		RuleIDs:       envelope.RuleIDs,
		JSONPath:      jsonPath,
		MDPath:        mdPath,
		TelemetryPath: telemetryPath,
		Runs:          input.Telemetry.Runs,
		Skipped:       input.Telemetry.skipped,
	}, nil
}

// plasmaProposeRunError 把子进程错误翻成"下一步该做什么"。
// exit 2 是 Python 的用法错：对 propose 来说它几乎只意味着参数契约漂了。
func plasmaProposeRunError(err error) error {
	var spawnErr *plasma.SpawnError
	if errors.As(err, &spawnErr) && spawnErr.Code == plasma.ExitUsage {
		return fmt.Errorf("plasma propose 执行失败（Python 侧 exit 2 = 用法错）：%w；"+
			"修复：确认 plasma.pyz 里的 propose 只接受 --in/--out（参数契约漂移时先重建 pyz）", err)
	}
	return fmt.Errorf("plasma propose 执行失败（Python 侧）：%w", err)
}

// parsePlasmaProposeEnvelope 严格解析 stdout：空输出、多写一行日志、截断的 JSON
// 都不是"提案是空的"，而是失败 —— 并且把开头一小段打出来，让人能直接看出原因。
func parsePlasmaProposeEnvelope(raw []byte) (plasmaProposeEnvelope, error) {
	var envelope plasmaProposeEnvelope
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return envelope, errors.New("plasma propose 的 stdout 是空的：Python 侧没有输出信封；" +
			"修复：用 --out 指定可写目录后重跑，并检查 stderr 里的原因")
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return envelope, fmt.Errorf("plasma propose 输出不是合法 JSON 信封（stdout %d 字节）：%v；开头：%q",
			len(raw), err, plasmaHeadText(trimmed, 240))
	}
	required := []struct {
		name  string
		value string
	}{
		{"id", envelope.ID},
		{"status", envelope.Status},
		{"json", envelope.JSON},
		{"md", envelope.MD},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return envelope, fmt.Errorf("plasma propose 的信封缺少字段 %q；开头：%q",
				field.name, plasmaHeadText(trimmed, 240))
		}
	}
	return envelope, nil
}

// plasmaHeadText 取前 n 字节做诊断片段。
// stdout 可能是一段几十 KB 的 traceback，整段塞进 error 会把上下文吃掉。
func plasmaHeadText(raw []byte, n int) string {
	if n <= 0 || len(raw) <= n {
		return string(raw)
	}
	// ToValidUTF8 把切断处的半个 rune 换成替换字符：error 里出现乱码字节会让
	// 日志管道更难读，而"丢掉半个字"对诊断没有影响。
	return strings.ToValidUTF8(string(raw[:n]), "") + "…"
}

// ensurePlasmaProposeDir 建提案目录（0700）。已存在的目录**不改权限**：
// 用户可能故意把它放在一个共享位置；静默 chmod 是另一种越权。
func ensurePlasmaProposeDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("提案目录 %s 已存在但不是目录；修复：换一个 --out 或先把它移走", dir)
	case !os.IsNotExist(err):
		return fmt.Errorf("检查提案目录 %s 失败: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("建提案目录 %s 失败: %w（修复：检查父目录权限，或用 --out 指定可写位置）", dir, err)
	}
	// MkdirAll 的权限受 umask 影响（0700 &^ umask）；提案里有 prompt 的 diff，
	// 所以 0700 必须是事实而不是"通常如此"。
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod 提案目录 %s: %w", dir, err)
	}
	return nil
}

// noticePlasmaProposeLedger 把"账本缺失/有坏行"打给人看。
// 静默跳过坏行的症状是"提案只依据了部分样本"，而产物里看不出这一点。
func noticePlasmaProposeLedger(w io.Writer, summary plasmaProposeTelemetry, path string) {
	if w == nil {
		w = os.Stderr
	}
	if summary.missing {
		fmt.Fprintf(w, "yagent plasma propose: 没有遥测账本 %s（本次提案只依据 prompt 与兜底规则）\n", path)
		return
	}
	if summary.skipped > 0 {
		fmt.Fprintf(w, "yagent plasma propose: 遥测账本跳过 %d 行不可解析记录（%s）\n", summary.skipped, path)
	}
}

// resolvePlasmaProposalPath 把信封里的路径落到 out 目录：绝对路径原样，相对路径按
// out 解析（Python 侧两种都可能写，按 out 解析不会把"文件名"误判成不存在）。
func resolvePlasmaProposalPath(outDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(outDir, path)
}

// plasmaProposeTimeout 给子进程一个看门狗：propose 是纯离线启发式（不调 provider），
// 但它要读账本与写文件；compile_timeout 被配成 1s 时不该把这个命令误杀。
func plasmaProposeTimeout(settings config.PlasmaConfig) time.Duration {
	timeout := settings.CompileTimeout * 2
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	return timeout
}
