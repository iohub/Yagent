package plasma_test

import (
	"testing"

	// 注册表由实现侧的 init() populate：tools 给 37 条，agents 补上 delegate_* 等 13 条。
	// 少 import 一个，测试看到的就是一个生产不存在的注册表（坑 39：探针必须与生产同形）。
	// 外部测试包可以 import 依赖 plasma 的包，所以 P9 之后 agents→plasma 也不会成环。
	_ "yagent/internal/agents"
	_ "yagent/internal/tools"
	"yagent/internal/toolspec"
)

func TestRegistryIsProductionShaped(t *testing.T) {
	if n := len(toolspec.Specs()); n != 50 {
		t.Fatalf("注册表 %d 条，生产基线是 50 —— 测试与生产不同形，did-you-mean 与能力派生都会失真", n)
	}
	if problems := toolspec.Validate(); len(problems) > 0 {
		t.Fatalf("注册表自检不干净，编译阶段的判据建立在坏注册表上: %v", problems)
	}
}
