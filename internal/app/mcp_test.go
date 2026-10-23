package app

import (
	"testing"

	"yagent/internal/toolspec"
)

// curated 名单必须指向真的注册过的静态工具：名单烂掉（改名/删桥）的后果是
// 一个真实能力被永久排除在动态注册之外，而没有任何报错。
func TestCuratedBridgesPointAtRegisteredTools(t *testing.T) {
	if len(codeseekCuratedBridges) != 6 {
		t.Fatalf("curated 桥 = %d 条, want 6（改名单要同时改测试与设计稿）", len(codeseekCuratedBridges))
	}
	for serverTool, localTool := range codeseekCuratedBridges {
		if _, ok := toolspec.Get(localTool); !ok {
			t.Errorf("%s 映射到 %q，但注册表里没有这个工具", serverTool, localTool)
		}
		if !codeseekCuratedTools[serverTool] {
			t.Errorf("%s 没有从对照表派生进 curated 集合", serverTool)
		}
	}
}

// ServerConfig 的翻译点：Manager 不读 config，所以这里必须逐键转对。
func TestMCPServerConfigsTranslateEveryField(t *testing.T) {
	ca := &Yagent{}
	cfgs := ca.mcpServerConfigs("", "")
	if len(cfgs) != 0 {
		t.Fatalf("没有 binary 也没有 [mcp.servers] 时 = %d 条, want 0", len(cfgs))
	}

	cfgs = ca.mcpServerConfigs("/usr/bin/codeseek", "/tmp/proj")
	cs, ok := cfgs["codeseek"]
	if !ok {
		t.Fatal("codeseek 没有成为内建 server")
	}
	if cs.Command != "/usr/bin/codeseek" || !cs.Enabled || len(cs.Curated) != 6 {
		t.Errorf("codeseek config = %+v", cs)
	}
	if cs.WorkingDir != "/tmp/proj" {
		t.Errorf("WorkingDir = %q, want 项目根", cs.WorkingDir)
	}
}
