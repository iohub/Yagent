package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/llm"
	"yagent/internal/plasma"
)

// 本文件是 trampoline 驱动器（设计稿 §6.2 / ADR-14）：把"生成策略在 Python、
// LLM 与流式在 Go"这条分工落成一个同步循环。
//
// 为什么实现在 agents 而不在 plasma：驱动器要调 LLM（llm.Engine）、要发 TUI 事件
// （EventBus）、要 spawn 子进程（os/exec）。三者都在 plasma 的 import 白名单之外
// （叶子约束），所以 plasma 只声明 Spawner 与退出码契约，循环留在这一侧。
//
// 纪律（AGENTS.md 硬约束 19 的配套）：
//   - 序号按 calls 计数器对齐，不扫目录：Python 与 Go 各自从 0 数（llm_req_000.json）；
//   - StreamHandler 在 Go 侧构造，所以 ai_chunk 进的是**父进程 dispatcher**；
//   - workdir 的 defer 清理覆盖正常与异常路径，启动期 GC 由装配层调 plasma.GCWorkdirs。

// llmRequestFile 是 Python 写的 llm_req_NNN.json 的形状。
//
// 类型留在 agents 侧：plasma 不能 import llm（叶子约束），而"一次采样请求长什么样"
// 正是 llm 的类型。刻意**没有** tier 字段 —— 引擎本身绑定档位，Go 侧拿一个 tier
// 字符串也无处施加；多一个没人使用的字段就是坑 35 的装饰性配置。
type llmRequestFile struct {
	Messages        []llm.Message `json:"messages"`
	Tools           []llm.ToolDef `json:"tools,omitempty"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	Temperature     float64       `json:"temperature,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

// PlasmaTrampoline 驱动一次在线生成：spawn → (exit 4 → Go 采样 → respawn)* → 编译 →
// (诊断 → --fix)* → 产出 Bundle。
//
// 本阶段（P8）**不注册、不执行、不写 store**：那是 P9 的接线。驱动器只回答
// "能不能产出一份编译通过的 bundle"，这样 trampoline 的契约可以独立于装配被测死。
type PlasmaTrampoline struct {
	Spawner plasma.Spawner
	LLM     llm.Engine
	// Publisher 为 nil 时不发流式事件（CLI/离线路径与测试），但 LLM 调用照旧。
	Publisher EventBus
	// AgentName 是事件归属（生产填 "meta"），空则回落 metaTrampolineAgent。
	AgentName   string
	Manifest    *plasma.Manifest
	Compiler    *plasma.Compiler
	Elaborate   func(raw []byte) (*plasma.Bundle, error)
	MaxLLMCalls int
	RetryCount  int
	// SpawnTimeout 是单次 spawn 的墙钟上限（compile_timeout）。<=0 表示只受 ctx 约束。
	SpawnTimeout time.Duration
	// GCBase 是 workdir 的父目录。空则用 os.TempDir()（plasma.NewWorkdir 的约定）。
	GCBase string
}

// TrampolineResult 是一次成功生成的观测。Workdir 在返回前已被清理，只留给错误信息与日志。
type TrampolineResult struct {
	Bundle   *plasma.Bundle
	Report   *plasma.Report
	Workdir  string
	LLMCalls int
	Attempts int
}

const (
	metaTrampolineAgent = "meta"
	// maxReportLines 是"重试用尽"错误里带的诊断条数上限：够看清主要问题，
	// 又不会把一整份报告塞进一条错误（错误会进 tool 结果，撑爆的是上下文）。
	maxReportLines = 5
)

// Generate 跑完整条 trampoline。ctx 结束（取消/超时）优先于任何业务判定。
func (t *PlasmaTrampoline) Generate(ctx context.Context, task, contextNote string) (*TrampolineResult, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	w, err := plasma.NewWorkdir(t.GCBase)
	if err != nil {
		return nil, err
	}
	// defer 覆盖正常与异常路径：workdir 里有 LLM 请求/响应正文（可能含用户输入），
	// 泄漏一次就是把一次委派的上下文留在磁盘上（T8）。
	defer func() { _ = w.Cleanup() }()

	if err := w.WriteReq(task, contextNote); err != nil {
		return nil, err
	}
	if err := w.WriteManifest(t.Manifest); err != nil {
		return nil, err
	}

	res := &TrampolineResult{Workdir: w.Path()}
	var lastReport *plasma.Report

	for attempt := 0; attempt < t.RetryCount; attempt++ {
		res.Attempts = attempt + 1
		args := []string{"generate"}
		if attempt > 0 {
			// 模式作为 argv 传给 Python，是契约的一部分（§7.3）：
			// generate [--resume] [--fix] --workdir W。
			args = []string{"generate", "--resume", "--fix"}
		}

		generated := false
		for res.LLMCalls < t.MaxLLMCalls {
			code, spawnErr := t.spawn(ctx, w.Path(), args)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("plasma: 驱动器被取消（第 %d 轮，已采样 %d 次）: %w",
					res.Attempts, res.LLMCalls, ctxErr)
			}
			if spawnErr != nil {
				return nil, spawnErr
			}
			switch code {
			case plasma.ExitSampling:
				if err := t.serveSample(ctx, w, res.LLMCalls); err != nil {
					return nil, err
				}
				res.LLMCalls++
				args = []string{"generate", "--resume"}
			case plasma.ExitOK:
				generated = true
			default:
				return nil, plasma.NewSpawnError(code, w.Path(), nil)
			}
			if generated {
				break
			}
		}
		if !generated {
			// T9：Python 死循环刷采样必须在这里停住。文案带次数，让调用方能说
			// "这条委派烧了 N 次采样"而不是只说"失败了"。
			return nil, fmt.Errorf("plasma: LLM 调用次数超上限（max_llm_calls=%d，已采样 %d 次，第 %d 轮仍未产出 bundle）",
				t.MaxLLMCalls, res.LLMCalls, res.Attempts)
		}

		bundle, report, ok, err := t.compile(ctx, w)
		if err != nil {
			return nil, err
		}
		if !ok {
			lastReport = report
			continue
		}
		res.Bundle, res.Report = bundle, report
		return res, nil
	}

	return nil, fmt.Errorf("plasma: 编译在 %d 次重生成后仍失败%s", t.RetryCount, summarizeReport(lastReport))
}

// validate 在开 workdir 之前点名缺什么。缺失的依赖不报错会变成一次"exit 0 但没人编译"。
func (t *PlasmaTrampoline) validate() error {
	switch {
	case t.Spawner == nil:
		return errors.New("plasma: 未注入 Spawner（子进程执行层）")
	case t.LLM == nil:
		return errors.New("plasma: 未注入 LLM 引擎（采样无法进行）")
	case t.Manifest == nil:
		return errors.New("plasma: 未注入 Manifest（Python 侧的能力清单是只读契约）")
	case t.Compiler == nil:
		return errors.New("plasma: 未注入 Compiler（产物无人裁决）")
	case t.Elaborate == nil:
		return errors.New("plasma: 未注入 Elaborate（bundle 形态归一化）")
	case t.MaxLLMCalls <= 0:
		return fmt.Errorf("plasma: max_llm_calls=%d 非法（<=0 会让内层循环一次都不跑）", t.MaxLLMCalls)
	case t.RetryCount <= 0:
		return fmt.Errorf("plasma: meta_retry_count=%d 非法（<=0 会让外层循环一次都不跑）", t.RetryCount)
	}
	return nil
}

// spawn 给单次 spawn 套墙钟上限。坑 46：真正让看门狗生效的是 Spawner 实现里的
// cmd.WaitDelay —— 只加 context.WithTimeout 时，子进程的**子孙**持有 stdout 管道
// 会让 Wait 一直等 EOF，看门狗于是形同虚设。
func (t *PlasmaTrampoline) spawn(ctx context.Context, workdir string, args []string) (int, error) {
	if t.SpawnTimeout <= 0 {
		return t.Spawner.Spawn(ctx, workdir, args)
	}
	sctx, cancel := context.WithTimeout(ctx, t.SpawnTimeout)
	defer cancel()
	return t.Spawner.Spawn(sctx, workdir, args)
}

// serveSample 服务一次 exit 4：读请求 → 调 LLM（流式投递到父进程） → 写响应。
//
// 事件顺序与 executor.go:560 生产路径一致：start → chunk* → end（带 usage）。
// end 无论成败都发：TUI 的流式态靠它收尾，失败时不发会让面板永远停在"生成中"。
func (t *PlasmaTrampoline) serveSample(ctx context.Context, w *plasma.Workdir, seq int) error {
	path := w.LLMReq(seq)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("plasma: 读取采样请求 %s: %w", filepath.Base(path), err)
	}
	var req llmRequestFile
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("plasma: 采样请求 %s 不是合法 JSON: %w", filepath.Base(path), err)
	}
	if len(req.Messages) == 0 {
		// 空 messages 会被 provider 拒成一次 400，而错误信息里看不出是 Python 写错了文件。
		return fmt.Errorf("plasma: 采样请求 %s 的 messages 为空", filepath.Base(path))
	}

	name := t.agentName()
	opts := &llm.CallOptions{
		AgentName:       name,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: req.ReasoningEffort,
	}
	if t.Publisher != nil {
		opts.StreamHandler = t.chunkPublisher(name)
		_ = t.Publisher.Publish("ai_stream_start", map[string]interface{}{"agent": name}, name)
	}

	resp, genErr := t.LLM.GenerateContent(ctx, req.Messages, req.Tools, opts)

	if t.Publisher != nil {
		metadata := map[string]interface{}{"agent": name}
		if genErr == nil && resp != nil && resp.Usage != nil {
			metadata["usage"] = usageMetadata(resp.Usage)
		}
		_ = t.Publisher.PublishWithMetadata("ai_stream_end", "", name, metadata)
	}
	if genErr != nil {
		return fmt.Errorf("plasma: 第 %d 次采样失败: %w", seq, genErr)
	}
	if resp == nil {
		return fmt.Errorf("plasma: 第 %d 次采样返回 nil Response", seq)
	}

	body, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return fmt.Errorf("plasma: 序列化采样响应: %w", err)
	}
	body = append(body, '\n')
	if err := w.WriteLLMResp(seq, body); err != nil {
		return err
	}
	return nil
}

// chunkPublisher 是**Go 侧**的流式构造点（ADR-14 断言④）。
// 闭包捕获 Publisher，所以 ai_chunk 发往父进程 dispatcher；Python 全程不接触 provider。
func (t *PlasmaTrampoline) chunkPublisher(name string) llm.StreamHandler {
	return func(_ context.Context, chunk []byte) error {
		if len(chunk) == 0 {
			return nil
		}
		return t.Publisher.Publish("ai_chunk", map[string]interface{}{
			"content": string(chunk),
			"agent":   name,
		}, name)
	}
}

func (t *PlasmaTrampoline) agentName() string {
	if strings.TrimSpace(t.AgentName) != "" {
		return t.AgentName
	}
	return metaTrampolineAgent
}

// compile 读产物并跑编译。ok=false 表示"这一轮被拒，写诊断后去 --fix"。
// 形态错误（Elaborate 失败）也走同一条路：它同样是一份可读的诊断。
func (t *PlasmaTrampoline) compile(ctx context.Context, w *plasma.Workdir) (*plasma.Bundle, *plasma.Report, bool, error) {
	raw, err := w.ReadBundleRaw()
	if err != nil {
		return nil, nil, false, fmt.Errorf("plasma: 子进程 exit 0 但没有产出 %s: %w", plasma.FileBundle, err)
	}
	bundle, err := t.Elaborate(raw)
	if err != nil {
		var diagErr *plasma.DiagError
		if errors.As(err, &diagErr) {
			report := &plasma.Report{Diagnostics: []plasma.Diagnostic{diagErr.Diag}}
			if writeErr := w.WriteDiagnostics(report); writeErr != nil {
				return nil, nil, false, writeErr
			}
			return nil, report, false, nil
		}
		return nil, nil, false, fmt.Errorf("plasma: 解析 bundle 失败: %w", err)
	}
	report := t.Compiler.Compile(ctx, bundle)
	if report.HasErrors() {
		if writeErr := w.WriteDiagnostics(report); writeErr != nil {
			return nil, nil, false, writeErr
		}
		return nil, report, false, nil
	}
	return bundle, report, true, nil
}

// summarizeReport 把最后一轮诊断压成一句可读的尾巴。
// 只取前 maxReportLines 条 error：warning 不阻断，放进"为什么没出包"里会误导。
func summarizeReport(r *plasma.Report) string {
	if r == nil {
		return ""
	}
	errs := r.Errors()
	if len(errs) == 0 {
		return ""
	}
	lines := make([]string, 0, maxReportLines)
	for i, d := range errs {
		if i == maxReportLines {
			lines = append(lines, fmt.Sprintf("…（共 %d 条 error）", len(errs)))
			break
		}
		lines = append(lines, fmt.Sprintf("%s: %s", d.Path, d.Code))
	}
	return "；最后一次诊断：" + strings.Join(lines, "；")
}

// usageMetadata 与 executor.go:586 的 ai_stream_end 元数据同形。
// 六个字段是 usage 面板与成本统计的既有口径，少一个就会让那一栏归零。
func usageMetadata(u *llm.TokenUsage) map[string]interface{} {
	return map[string]interface{}{
		"prompt_tokens":               u.PromptTokens,
		"completion_tokens":           u.CompletionTokens,
		"total_tokens":                u.TotalTokens,
		"cache_creation_input_tokens": u.CacheCreationInputTokens,
		"cache_read_input_tokens":     u.CacheReadInputTokens,
		"total_input_tokens":          u.TotalInputTokens,
	}
}
