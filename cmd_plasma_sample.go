package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"yagent/internal/llm"
)

// 本文件是 `plasma sample`：借生产引擎发一次调用，把 llm.Response 原样打出来。
// 它是"离线专用"的（§7.2）：不装 Agent、不跑循环，只验引擎与 tier 解析这条路。

const unknownTier = llm.Tier(-1)

// runPlasmaSample 用配置里的引擎发一次调用，把 llm.Response 原样打出来。
//
// tier 的落点走 llm/router.go 既有机制（Router.EngineFor / ModelFor），不新写一层
// tier→model 映射：映射的单一来源是 BuildRouter 的分类结果。
func runPlasmaSample(messagesPath, tierName string, asJSON bool, out io.Writer) error {
	if strings.TrimSpace(messagesPath) == "" {
		return newUsageError("sample 需要 --messages <path>")
	}
	settings, cfg := plasmaRuntime()
	if cfg == nil {
		return fmt.Errorf("sample 需要一份可用的 config.toml（引擎要 provider）")
	}
	raw, err := os.ReadFile(messagesPath)
	if err != nil {
		return newUsageError("读不到 messages %s: %v", messagesPath, err)
	}
	var messages []llm.Message
	if err := json.Unmarshal(raw, &messages); err != nil {
		return newUsageError("messages 必须是 []llm.Message 的 JSON: %v", err)
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("构造客户端: %w", err)
	}
	defer client.CloseIdleConnections()

	engine, model, err := sampleEngine(client, tierName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), settings.SampleTimeout)
	defer cancel()
	resp, err := engine.GenerateContent(ctx, messages, nil, nil)
	if err != nil {
		return fmt.Errorf("调用 %s: %w", model, err)
	}
	return renderPlasmaSample(out, model, resp, asJSON)
}

// sampleEngine 解析 tier 并返回该档位的引擎。
//
// "既有 API 无法按调用选 tier"这条注解在设计稿里是留了余地的：这里用的是
// Client.GetRouter() + Router.EngineFor/ModelFor（BuildRouter 的产物），
// 也就是**按 tier 直接取引擎**，不经过 RoutingEngine 的 agent 基线。
func sampleEngine(c *llm.Client, tierName string) (llm.Engine, string, error) {
	tier := llm.TierBalanced
	if v := strings.TrimSpace(tierName); v != "" {
		tier = llm.ParseTier(v, unknownTier)
		if tier == unknownTier {
			return nil, "", newUsageError("--tier %q 不认识（fast | balanced | high）", v)
		}
	}
	if router := c.GetRouter(); router.Enabled() {
		if engine := router.EngineFor(tier); engine != nil {
			model := router.ModelFor(tier)
			fmt.Fprintf(os.Stderr, "yagent plasma sample: tier=%s 解析为 model=%s\n", tier, model)
			return engine, model, nil
		}
	}
	// 固定模式（routing.mode=fixed 或候选池不足以分档）：如实说明，不假装 tier 生效了。
	fmt.Fprintf(os.Stderr,
		"yagent plasma sample: 自动路由未启用，tier=%s 没有对应模型，改用固定 provider\n", tier)
	if c.Engine == nil {
		return nil, "", fmt.Errorf("配置里没有可用引擎")
	}
	return c.Engine, c.Engine.Model(), nil
}

// runPlasmaManifest 打印 Manifest。它不读配置也不碰 store：
