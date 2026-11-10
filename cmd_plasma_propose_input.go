package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"yagent/internal/agents"
	"yagent/internal/plasma"
)

// 本文件是 `yagent plasma propose` 的**材料装配**：把 propose.py 需要的输入文档
// （prompt 全文 + 遥测摘要 + 允许改动的字段）在 Go 侧拼好。
//
// 为什么材料必须由 Go 写（§1.2 / §8.1 T5）：Python 不碰工作区、不直连 provider、
// 不持密钥。它只能读一个由 Go 递过去的 JSON 文件 —— 于是"提案看的是哪份 prompt"
// 这件事在 Go 侧可审计，而不是靠 Python 去猜仓库布局。
//
// 跨语言契约：下面的 json tag 与 plasma/propose.py 的 `payload.get(...)` 逐键对齐。
// 改名不会报错，症状是"Python 按默认值跑"（runs=0、lint 为空）—— 所以对齐靠
// cmd_plasma_propose_input_test.go 里对已序列化 JSON 的逐键断言钉住。

const (
	// plasmaProposeSchemaVersion 是输入文档的协议版本。两侧对不上时 Python 直接拒收
	// （`schema_version = %r，本版本只认 %d`），不做"尽力解析"。
	plasmaProposeSchemaVersion = 1
	// plasmaProposeTelemetryWindow 是遥测窗口：只看**最近**这么多条匹配样本。
	// 与 propose.py 的 DEFAULT_TELEMETRY_WINDOW 同值；Go 侧显式写出来，Python 侧
	// 只是兜底默认。
	plasmaProposeTelemetryWindow = 200
	// plasmaProposeInputFileName 是临时 workdir 里的输入文件名（workdir 0700、文件 0600，
	// 跑完整个目录删除）。
	plasmaProposeInputFileName = "propose-input.json"
)

// plasmaProposeEditableFields 是提案允许触碰的字段清单（§6.9）。
//
// 只有两项，且都不是"点了就生效"：prompt.system 的改动必须同时改 Go 代码，
// runtime.max_steps 的改动必须同时改装配点。把这份清单写进输入是让 Python 的产物
// 自带边界说明，而不是让它自由发挥。
var plasmaProposeEditableFields = []string{"prompt.system", "runtime.max_steps"}

// plasmaProposeInput 是写给 propose.py 的输入文档。
type plasmaProposeInput struct {
	SchemaVersion  int                    `json:"schema_version"`
	Agent          string                 `json:"agent"`
	Prompt         string                 `json:"prompt"`
	PromptHash     string                 `json:"prompt_hash"`
	PromptPath     string                 `json:"prompt_path"`
	EditableFields []string               `json:"editable_fields"`
	Telemetry      plasmaProposeTelemetry `json:"telemetry"`
	Lint           []string               `json:"lint"`
}

// plasmaProposeTelemetry 是遥测摘要。键名与 propose.py 的 `_evaluate` 一致。
//
// skipped / missing 刻意是**未导出**字段：它们是给 Go 侧的账目（缺账本、跳过坏行
// 必须可见），不属于跨语言契约 —— 写进 JSON 只会让 Python 以为多了一个字段。
type plasmaProposeTelemetry struct {
	Window      int            `json:"window"`
	Runs        int            `json:"runs"`
	Failures    int            `json:"failures"`
	Escalations int            `json:"escalations"`
	StopReasons map[string]int `json:"stop_reasons"`
	AvgSteps    float64        `json:"avg_steps"`
	TopErrors   []string       `json:"top_errors"`

	skipped int
	missing bool
}

// buildPlasmaProposeInput 把一次提案所需的全部材料装配成输入文档。
//
// telemetryPath 为空 ⇒ 直接记"账本缺失"（不猜路径：猜错会把别的 store 的样本算进来）。
func buildPlasmaProposeInput(info agents.PromptInfo, telemetryPath string) (plasmaProposeInput, error) {
	telemetry, err := aggregatePlasmaTelemetry(telemetryPath, info.Agent)
	if err != nil {
		return plasmaProposeInput{}, err
	}
	return plasmaProposeInput{
		SchemaVersion: plasmaProposeSchemaVersion,
		Agent:         info.Agent,
		Prompt:        info.Text,
		PromptHash:    plasmaPromptHash(info.Text),
		PromptPath:    info.Path,
		// 非 nil 空切片会被编码成 []，nil 会被编码成 null；Python 侧对 null 会回落，
		// 但"我们明确说了没有 lint"与"我们忘了填"必须能区分。
		EditableFields: append([]string{}, plasmaProposeEditableFields...),
		Telemetry:      telemetry,
		Lint:           []string{},
	}, nil
}

// plasmaPromptHash 是 base_hash 的算法：对 prompt 原文做 sha256，小写 hex。
// 必须与 propose.py 的 `hashlib.sha256(prompt.encode("utf-8"))` 逐字节同源
// （Python 侧会重算并比对，对不上直接 fail）。
func plasmaPromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// aggregatePlasmaTelemetry 读 telemetry.jsonl 并折成 propose.py 要的形状。
//
// 三条口径（都对得上"提案依据是最近的生产遥测"）：
//  1. **按 agent 过滤**：账本里混着全部委派的记录。接受 `agent` 与 `delegate_agent`
//     两种写法（`PlasmaTelemetrySink` 写的是工具名后缀，custom agent 写的是自己的名字）。
//  2. **取尾部窗口**：只保留最近 200 条**匹配**记录（按文件追加序，不按 ts 排序 ——
//     账本本来就是追加写的）。窗口先于派生统计，否则"前 50 条老样本"会把均值拖走。
//  3. **坏行只计数不中断**：一行解析失败说明那一行坏了，不代表整个账本不可用；
//     但计数必须回到调用方并**打出来**（静默跳过等于账本缺样本而没人知道）。
//
// 缺账本是正常状态（还没跑过生产委派）：返回全零摘要 + missing=true，由调用方决定
// 怎么说；打不开（权限/IO）才是错误。
func aggregatePlasmaTelemetry(path, agent string) (plasmaProposeTelemetry, error) {
	summary := plasmaProposeTelemetry{
		Window:      plasmaProposeTelemetryWindow,
		StopReasons: map[string]int{},
		TopErrors:   []string{},
	}
	if strings.TrimSpace(path) == "" {
		summary.missing = true
		return summary, nil
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			summary.missing = true
			return summary, nil
		}
		return summary, fmt.Errorf("读遥测账本 %s 失败: %w（修复：检查文件权限；或先跑几次生产委派）", path, err)
	}
	defer file.Close()

	window := make([]plasma.TelemetryRecord, 0, plasmaProposeTelemetryWindow)
	scanner := bufio.NewScanner(file)
	// 1 MiB 足够一行遥测（它是定长字段的对象）；超长行会以 scanner.Err 报出来，
	// 而不是被静默截断成一条"坏行"。
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec plasma.TelemetryRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			summary.skipped++
			continue
		}
		if !plasmaTelemetryMatchesAgent(rec, agent) {
			continue
		}
		window = append(window, rec)
		if len(window) > plasmaProposeTelemetryWindow {
			window = window[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return summary, fmt.Errorf("读遥测账本 %s 中断: %w", path, err)
	}
	summarizePlasmaTelemetry(&summary, window)
	return summary, nil
}

// summarizePlasmaTelemetry 把窗口内的记录折成计数与均值。
//
// 拆成独立函数是为了让"窗口语义"与"派生口径"能分别被测：窗口错了 avg 会跟着错，
// 两者混在一起时红的那条测试说不清是哪一半坏的。
func summarizePlasmaTelemetry(summary *plasmaProposeTelemetry, window []plasma.TelemetryRecord) {
	summary.Runs = len(window)
	if summary.Runs == 0 {
		return
	}
	steps := 0
	counts := make(map[string]int, 4)
	firstSeen := make(map[string]int, 4)
	for i, rec := range window {
		steps += rec.Steps
		if rec.Escalated {
			summary.Escalations++
		}
		if reason := strings.TrimSpace(rec.StopReason); reason != "" {
			summary.StopReasons[reason]++
		}
		errText := strings.TrimSpace(rec.Err)
		if errText == "" {
			continue
		}
		summary.Failures++
		counts[errText]++
		if _, seen := firstSeen[errText]; !seen {
			firstSeen[errText] = i
		}
	}
	summary.AvgSteps = float64(steps) / float64(summary.Runs)
	summary.TopErrors = topPlasmaErrors(counts, firstSeen, 3)
}

// topPlasmaErrors 按"出现次数降序、同频按首次出现序"取前 limit 条。
//
// 同频次序固定是确定性的一部分：同一份账本反复跑提案，`top_errors` 不能换顺序，
// 否则 rationale 会跟着换（提案的启发式承诺了同一输入同一输出）。
func topPlasmaErrors(counts, firstSeen map[string]int, limit int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return firstSeen[keys[i]] < firstSeen[keys[j]]
	})
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

// plasmaTelemetryMatchesAgent 是 agent 过滤的唯一判据。
// 两种写法都认：`PlasmaTelemetrySink` 对内建委派写 `delegate_` 后缀，对 custom agent
// 写自定义名字。
func plasmaTelemetryMatchesAgent(rec plasma.TelemetryRecord, agent string) bool {
	name := strings.TrimSpace(rec.Agent)
	if name == "" || agent == "" {
		return false
	}
	return name == agent || name == "delegate_"+agent
}
