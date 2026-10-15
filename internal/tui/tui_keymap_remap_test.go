package tui

import (
	"strings"
	"testing"

	"yagent/internal/config"
)

// 键位配置今天是**追加别名**：editKeyMap[custom] = default，而各 switch 认的是字面默认键，
// 所以自定义键能用、默认键也照常生效、且永远释放不掉。这里钉住改成"替换"后的三条性质。

func TestBuildKeyMapReplacesDefault(t *testing.T) {
	m, conflicts := buildKeyMap([]keyBinding{{def: "alt+s", custom: "ctrl+j"}})
	if len(conflicts) != 0 {
		t.Fatalf("无冲突配置报出冲突：%v", conflicts)
	}
	// 新键映射到规范键，消费点的 switch 才能继续认它。
	if got := translateKey(m, "ctrl+j"); got != "alt+s" {
		t.Errorf("ctrl+j → %q, want alt+s", got)
	}
	// 默认键必须失效 —— 这是"替换"与"追加"的全部区别。
	if got := translateKey(m, "alt+s"); got != keyUnbound {
		t.Errorf("被换走的默认键 alt+s → %q, want 解绑哨兵", got)
	}
	// 没注册过的键原样透传，别把整个键盘吸进翻译表。
	if got := translateKey(m, "x"); got != "x" {
		t.Errorf("未注册键 x → %q, want 原样", got)
	}
}

func TestBuildKeyMapIdentityWhenUnset(t *testing.T) {
	m, _ := buildKeyMap([]keyBinding{{def: "alt+m", custom: ""}, {def: "alt+m2", custom: "alt+m2"}})
	if got := translateKey(m, "alt+m"); got != "alt+m" {
		t.Errorf("没配置时 alt+m → %q, want 保持生效", got)
	}
	if got := translateKey(m, "alt+m2"); got != "alt+m2" {
		t.Errorf("显式写成与默认相同的键被解绑了：%q", got)
	}
}

// 两个动作抢同一个键时保留先声明的，并把冲突报出来（静默丢弃等于用户按了没反应）。
func TestBuildKeyMapReportsConflicts(t *testing.T) {
	m, conflicts := buildKeyMap([]keyBinding{
		{def: "alt+s", custom: "ctrl+x"},
		{def: "alt+d", custom: "ctrl+x"},
	})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want 一条", conflicts)
	}
	if got := translateKey(m, "ctrl+x"); got != "alt+s" {
		t.Errorf("ctrl+x → %q, want 保留先声明的 alt+s", got)
	}
	// 后一个动作的默认键不该被解绑：它没成功迁移，解绑等于凭空少一个快捷键。
	if got := translateKey(m, "alt+d"); got != "alt+d" {
		t.Errorf("冲突失败的 alt+d → %q, want 保持生效", got)
	}
}

// 从真配置构造两张表：自定义生效、默认被释放，且没配的动作全部照旧。
func TestKeymapsBuiltFromConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.TUI.Keybindings.Edit.SubmitTask = "ctrl+j"

	edit, _ := buildKeyMap(editKeyBindings(cfg))
	if got := translateKey(edit, "ctrl+j"); got != "alt+s" {
		t.Errorf("ctrl+j → %q, want alt+s", got)
	}
	if got := translateKey(edit, "alt+s"); got != keyUnbound {
		t.Errorf("alt+s → %q, want 解绑", got)
	}
	// 未配置的 tab 快捷键仍按 alt+N 走（曾经 tab_1..9 在 cfg 为 nil 时先解引用后判空）。
	if got := translateKey(edit, "alt+3"); got != "alt+3" {
		t.Errorf("alt+3 → %q, want 保持默认", got)
	}
}

// nil 配置不能 panic —— 测试与 --prompt 路径都会走到。
func TestKeymapsWithNilConfig(t *testing.T) {
	edit, _ := buildKeyMap(editKeyBindings(nil))
	if len(edit) != 0 {
		t.Errorf("nil 配置应当得到空表，实际 %d 项", len(edit))
	}
	if got := translateKey(edit, "alt+s"); got != "alt+s" {
		t.Errorf("空表下 alt+s → %q, want 原样透传", got)
	}
}

// keyUnbound 绝不能匹配上任何 switch 的字面 case，否则"解绑"变成"触发某个动作"。
func TestUnboundSentinelMatchesNothing(t *testing.T) {
	if strings.HasPrefix(keyUnbound, "alt+") || strings.HasPrefix(keyUnbound, "ctrl+") {
		t.Fatal("哨兵值看起来像一个真实按键，会被 switch 认领")
	}
	if translateKey(map[string]string{"a": ""}, "a") != keyUnbound {
		t.Error("表里的空值没有被翻译成解绑哨兵")
	}
}
