package config

import (
	"testing"

	"yagent/internal/blueprint"
)

// 本文件钉一条"两份同形实现"的账（§11 坑 18）：
// [blueprint] 的两个预算默认值在 config 里，而渲染器的兜底值在 blueprint 包里 ——
// **两边都必须有人真的用它们的默认值**，否则"配置没写"这条路径与"写了但写错"那条
// 会长出两个不同的数，而没人会注意到。
//
// 为什么不合并成一份：config 是全仓库最靠底的包之一（`go list -deps ./internal/config`
// 今天只有 util 一条内部边），而 blueprint 是 leaf 包、判据必须由调用方传进来（I8）。
// 所以这里用**跨包对账测试**把它钉成一条约束，而不是为了省一个常数把依赖方向反过来。
func TestBlueprintDefaultsAgreeWithRenderer(t *testing.T) {
	got := DefaultBlueprintConfig()

	if got.IndexMaxTokens != blueprint.DefaultIndexMaxTokens {
		t.Errorf("[blueprint] index_max_tokens 默认 = %d, 渲染器兜底 = %d —— 两个数必须同源",
			got.IndexMaxTokens, blueprint.DefaultIndexMaxTokens)
	}
	if got.ProseBudgetTokens != blueprint.DefaultProseBudgetTokens {
		t.Errorf("[blueprint] prose_budget_tokens 默认 = %d, 渲染器兜底 = %d",
			got.ProseBudgetTokens, blueprint.DefaultProseBudgetTokens)
	}

	// 几个配额/成本常数是**设计稿点名的常数**（§10 把键数钉在 5 个）。
	// 这里写死一次：改动必须显式，不能被自比较蒙过去。
	if blueprint.MaxAtomsPerSection != 40 || blueprint.MaxSubmitItems != 8 || blueprint.MaxSupersedes != 4 {
		t.Errorf("蓝图配额常数变了: section=%d items=%d supersedes=%d —— schemas.json 的 maxItems 同步了吗",
			blueprint.MaxAtomsPerSection, blueprint.MaxSubmitItems, blueprint.MaxSupersedes)
	}
	if blueprint.DeriveBudget.String() != "150ms" {
		t.Errorf("派生成本分界 = %v, 设计稿 §5.1 写的是 150ms", blueprint.DeriveBudget)
	}
	if blueprint.DefaultOrphanTTL.String() != "2160h0m0s" { // 90 天
		t.Errorf("孤儿子节老化 = %v, 设计稿 §5.3 写的是 90 天", blueprint.DefaultOrphanTTL)
	}
}
