package agents

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"yagent/internal/blueprint"
	"yagent/internal/toolspec"
)

// 本文件是 I5 与 I12 的闸门：**模型能写的只有散文行**，而段落/字段的词表
// 在 schema 与代码里必须是一件事而不是两件。
//
// I12 的双向对账不是修辞：只测"schema 里的每个值代码都认识"，
// 代码里新增一个段落时测试照样绿，而模型永远提交不出它 ——
// 那正好是"配置项存在≠生效"的同一种病，只不过这次是 schema 侧。

func agentExitBlueprintItem(t *testing.T) map[string]interface{} {
	t.Helper()
	s, ok := toolspec.GetSchema("agent_exit")
	if !ok {
		t.Fatalf("agent_exit 没有 schema 声明")
	}
	bp, ok := s.Parameters["properties"].(map[string]interface{})["blueprint"].(map[string]interface{})
	if !ok {
		t.Fatalf("agent_exit 的 schema 里没有 blueprint 数组")
	}
	if got := bp["type"]; got != "array" {
		t.Errorf("blueprint 的类型 = %v, 要 array", got)
	}
	// 有界数组是这套 schema 的既有语法（§7.2），也必须是**代码里的那个常数**。
	if got := bp["maxItems"]; got != float64(blueprint.MaxSubmitItems) {
		t.Errorf("blueprint 的 maxItems = %v, 要 %d（与代码常数同源）", got, blueprint.MaxSubmitItems)
	}
	item, ok := bp["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("blueprint.items 不是对象")
	}
	return item
}

// I5：item 只有六个键，而且 required 只有两个。
// 多一个键就是给模型开一条新的写入通道 —— 结构层（模块、边、计数）必须写不进去。
func TestAgentExitBlueprintItemSchemaShape(t *testing.T) {
	item := agentExitBlueprintItem(t)

	props, ok := item["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("item.properties 不是对象")
	}
	var names []string
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	want := []string{"claim", "field", "module", "section", "supersedes", "where"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("blueprint item 的键集变了: %v, 要恰好 %v（多一个键 = 多一条写入通道）", names, want)
	}

	req := stringSet(item["required"])
	if len(req) != 2 || !req["section"] || !req["claim"] {
		t.Errorf("required = %v, 要恰好 section 与 claim", item["required"])
	}
	// supersedes 是"指认旧条目"的通道，宽度也是常数（§7.4 第 3 条：认错了不报错，
	// 但一条提交不能借它把整段清空）。
	sub, ok := props["supersedes"].(map[string]interface{})
	if !ok {
		t.Fatalf("supersedes 没声明")
	}
	if got := sub["maxItems"]; got != float64(blueprint.MaxSupersedes) {
		t.Errorf("supersedes 的 maxItems = %v, 要 %d", got, blueprint.MaxSupersedes)
	}
	// claim/where 的长度上限由代码常数说了算（§7.5 第 4 条）。
	for _, k := range []string{"claim", "where"} {
		if _, ok := props[k].(map[string]interface{}); !ok {
			t.Errorf("%s 没声明", k)
		}
	}
	if blueprint.MaxClaimRunes != 400 || blueprint.MaxWhereRunes != 120 {
		t.Errorf("长度常数与 schema 描述脱钩了: claim=%d where=%d", blueprint.MaxClaimRunes, blueprint.MaxWhereRunes)
	}
}

// I12：schema 的 section enum 与代码枚举**双向**对账。
func TestSectionEnumMatchesSchema(t *testing.T) {
	item := agentExitBlueprintItem(t)
	props := item["properties"].(map[string]interface{})
	schemaEnum := stringList(props["section"].(map[string]interface{})["enum"])

	// schema → 代码：每个值都必须能被 NormaliseSection 认出来。
	for _, v := range schemaEnum {
		got, ok := blueprint.NormaliseSection(v)
		if !ok {
			t.Errorf("schema 里的 section %q 代码不认（模型提交得出、程序消化不了）", v)
			continue
		}
		if got == blueprint.SecModules && v != "modules" {
			t.Errorf("modules 段的名字漂了: %q", v)
		}
	}
	// 代码 → schema：代码里可提交的段落（去掉前缀段自身）必须在 enum 里。
	for _, sec := range blueprint.AssertedSections {
		if sec == blueprint.SecModules {
			if !containsString(schemaEnum, "modules") {
				t.Errorf("代码里有 Modules 前缀段，schema enum 里却没有")
			}
			continue
		}
		want := strings.ToLower(sec)
		if !containsString(schemaEnum, want) {
			t.Errorf("代码里有段落 %q，schema enum 里却没有（模型永远提交不出它）", sec)
		}
	}
	if len(schemaEnum) != len(blueprint.AssertedSections) {
		t.Errorf("schema enum %d 个值, 代码段落 %d 个 —— 两边必须同宽", len(schemaEnum), len(blueprint.AssertedSections))
	}
}

// I12 的同一条对账用在三个模块字段上。
func TestModuleFieldEnumMatchesSchema(t *testing.T) {
	item := agentExitBlueprintItem(t)
	props := item["properties"].(map[string]interface{})
	enum := stringList(props["field"].(map[string]interface{})["enum"])

	sort.Strings(enum)
	sorted := append([]string(nil), blueprint.ModuleFields...)
	sort.Strings(sorted)
	if strings.Join(enum, ",") != strings.Join(sorted, ",") {
		t.Errorf("schema 的 field enum = %v, 代码 ModuleFields = %v", enum, sorted)
	}
	for _, f := range enum {
		if !blueprint.IsModuleField(f) {
			t.Errorf("schema 放行了代码不认的字段 %q", f)
		}
	}
}

// read_blueprint 的 schema 必须与渲染器认识的参数一致 ——
// 声明了却读不到的参数会静默按默认渲染，那是"写了没人读"的另一种形态。
func TestReadBlueprintSchemaPropertiesExist(t *testing.T) {
	s, ok := toolspec.GetSchema("read_blueprint")
	if !ok {
		t.Fatalf("read_blueprint 没有 schema 声明")
	}
	for _, p := range []string{"section", "module", "field", "state", "offset", "limit"} {
		if !s.HasProperty(p) {
			t.Errorf("read_blueprint 的 schema 少了参数 %q", p)
		}
	}
	// 要有真快照：没有它，执行器会先回答"还在扫描"，参数校验那条分支根本走不到
	//（夹具小到没触发被测分支，§8.3 的那类假绿）。
	_, snap := blueprintProject(t)
	view := NewBlueprintView(&testBlueprintSource{root: "/nonexistent", snap: snap}, "repo", true, 4000, 1000, nil)
	fn := blueprintExecutor(view)
	// 每个声明过的参数都要能被 proseSpecFromParams 处理，且非法值必须**说出来**。
	out, err := fn(context.Background(), map[string]interface{}{"section": "nonsense"})
	if err != nil {
		t.Fatalf("非法段落名不该报错: %v", err)
	}
	if text, _ := out.(string); !strings.Contains(text, "unknown section") {
		t.Errorf("非法段落名被静默按默认渲染了: %q", text)
	}
	out, _ = fn(context.Background(), map[string]interface{}{"module": "internal/nope"})
	if text, _ := out.(string); strings.TrimSpace(text) == "" {
		t.Errorf("未知模块返回了空回执（空会被读成\"这里没东西\"）")
	}
}

// 七个 Agent 都有 agent_exit，但只有 Repo 的完成接缝会读 blueprint[]（§7.2 三条边界之一）。
// 这条断言钉住"Director 不需要知道这个参数存在"：它的委派文案里没有 blueprint 字样。
func TestBlueprintWriteChannelIsRepoOnly(t *testing.T) {
	if !strings.Contains(repoPrompt, "blueprint") {
		t.Errorf("repo.prompt.md 没写蓝图契约 —— 模型不知道该提交什么")
	}
	if strings.Contains(directorPrompt, "agent_exit 的新参数 blueprint") {
		t.Errorf("Director 的 prompt 里出现了写入通道说明（§7.2：Director 侧不需要知道）")
	}
	// 采集点只有一个：落盘钩子挂在 Repo 上，其他 Agent 的提交不会变成文件。
	if !strings.Contains(codingPrompt, "You do not write to the blueprint") {
		t.Errorf("coding.prompt.md 少了\"你不写蓝图\"这一句，Coding 会以为自己也能沉淀")
	}
}

func stringList(v interface{}) []string {
	raw, _ := json.Marshal(v)
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func stringSet(v interface{}) map[string]bool {
	out := map[string]bool{}
	for _, s := range stringList(v) {
		out[s] = true
	}
	return out
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
