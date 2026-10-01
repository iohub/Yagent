package agents

import (
	"context"
	"encoding/json"

	"yagent/internal/globalctx"
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
