package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"yagent/internal/globalctx"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// registerToolFactories 把内置工具的执行绑定注册为工厂（name → FactoryFunc），
// 消灭 DirectorAgent 的 12 case 执行绑定 switch 与构造函数的 8 case 手工 switch。
// 工厂闭包体 = 原执行绑定分支逻辑（P0 Step 9a：逐字等价迁移）；
// schema 由 Build/BuildFromFactory 时从 def.Parameters 注入
// （等价旧 NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)）。
//
// 禁用语义：ask_user_for_help 在 FullYoloMode 下返回 (nil, nil)，Build/BuildFromFactory
// 调用方跳过——等价旧构造函数 switch 的 continue 与旧执行绑定方法返回 nil。
func registerToolFactories(reg *tools.Registry, gctx *globalctx.GlobalCtx) {
	reg.MustRegisterFactory("read_file", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.FileOps.ExecuteReadFile).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("search_replace_in_file", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.ReplaceTool.ExecuteReplaceBlock).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("create_file", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.FileOps.ExecuteCreateFile).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("run_bash", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.SysOps.ExecuteRunBash).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("search_by_regex", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.SearchOps.ExecuteGrepSearch).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("list_dir", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.FileOps.ExecuteListDir).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("print_dir_tree", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.FileOps.ExecutePrintDirTree).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("thinking", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		fn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			inputBytes, _ := json.Marshal(params)
			return gctx.ThinkingTool.Call(ctx, string(inputBytes))
		}
		return tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("micro_agent", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.MicroAgentTool.Execute).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("deepthinking", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.DeepThinkingTool.Execute).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("agent_exit", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		return tools.NewAdapter(def.Name, def.Description, gctx.FlowOps.ExecuteAgentExit).WithSchema(def.Parameters), nil
	})
	reg.MustRegisterFactory("ask_user_for_help", func(def tools.ToolDefinition) (*tools.Adapter, error) {
		if gctx.FullYoloMode {
			return nil, nil // 禁用：Build 跳过（等价旧 continue），BuildFromFactory 返回 (nil, nil)
		}
		return tools.NewAdapter(def.Name, def.Description, gctx.FlowOps.ExecuteAskUserForHelp).WithSchema(def.Parameters), nil
	})
}

// delegateSpec 统一模式 delegate 工具的规格（5 个标准子 Agent 共用）。
type delegateSpec struct {
	toolName    string // 工具名 "delegate_repo"
	agentName   string // 短名 "repo"（createRolloutWriter/applyEnhancedCommander 用）
	disabledKey string // disabledAgents 的 key（"repo"）
	description string // 工具 description（从原闭包逐字提取）
	taskDesc    string // task 参数的 description（从原闭包逐字提取）
	agent       Agent  // types.go Agent 接口
}

// newDelegateAdapter 统一 delegate 工厂：解析 task → createRolloutWriter →
// WithRolloutWriter → agent.Run → applyEnhancedCommander（等价原 5 个闭包的统一模式）。
// selfFn 延迟获取 DirectorAgent：构造函数在 self 赋值前创建 adapter，
// 闭包调用时 self 已就绪（等价原闭包变量捕获语义——直接传 *DirectorAgent
// 会固化 nil 指针导致 panic）。
func newDelegateAdapter(spec delegateSpec, selfFn func() *DirectorAgent) *tools.Adapter {
	return tools.NewAdapter(spec.toolName, spec.description,
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			self := selfFn()
			task, ok := params["task"].(string)
			if !ok {
				return nil, fmt.Errorf("task parameter required")
			}
			// 创建 Rollout Writer 并注入到 context
			if rolloutWriter := self.createRolloutWriter(spec.agentName, task); rolloutWriter != nil {
				defer rolloutWriter.Close()
				ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
			}
			result, err := spec.agent.Run(ctx, task)
			// 使用增强型 Commander 处理结果（压缩 + 注册）
			return self.applyEnhancedCommander(spec.agentName, task, result, err)
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": spec.taskDesc},
		},
		"required": []string{"task"},
	})
}
