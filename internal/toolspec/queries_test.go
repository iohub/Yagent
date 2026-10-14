package toolspec

import (
	"strings"
	"testing"
)

// queries.go 是"按工具名查派生元数据"的入口，替代原先散在 8 处的名字表。
// 它最重要的性质是**未注册名字的 fail-safe 回落**：并发调度、授权、截断都读它，
// 一个拼错的名字必须退化成"不可并发、不需授权、常规截断"，
// 退化成别的值就是安全属性变化（例如默认变可并发 → 写工具被并行执行）。

func TestQueries_FailSafeForUnregisteredNames(t *testing.T) {
	installRegistry(t, baseSpec("read_file"))

	const ghost = "definitely_not_registered"
	if IsReadOnly(ghost) {
		t.Error("IsReadOnly(未注册) = true, want false（默认可并发会把未知工具塞进并行批）")
	}
	if IsSerial(ghost) {
		t.Error("IsSerial(未注册) = true, want false")
	}
	if IsInteractive(ghost) {
		t.Error("IsInteractive(未注册) = true, want false")
	}
	if NeedsAuth(ghost) {
		t.Error("NeedsAuth(未注册) = true, want false")
	}
	if got := PathParams(ghost); got != nil {
		t.Errorf("PathParams(未注册) = %v, want nil", got)
	}
	if EffectOf(ghost) != 0 {
		t.Errorf("EffectOf(未注册) = %v, want 0", EffectOf(ghost))
	}
	if got := TruncationPriority(ghost); got != 1 {
		t.Errorf("TruncationPriority(未注册) = %d, want 1（与历史 toolTruncationPriority 的 default 分支一致）", got)
	}
	if IsCompactDisplay(ghost) {
		t.Error("IsCompactDisplay(未注册) = true, want false（默认展开正文）")
	}
	if ModelSlot(ghost) != "" {
		t.Errorf("ModelSlot(未注册) = %q, want 空串（沿用所属 Agent 引擎）", ModelSlot(ghost))
	}
	if Brief(ghost) != "" {
		t.Errorf("Brief(未注册) = %q, want 空串", Brief(ghost))
	}
}

// 每个查询都必须与 Spec 上的字段一一对应；中间任何"再查一张表"的回归
// 都会在这里暴露（历史上就是那 8 张名字表各自漂移）。
func TestQueries_ReflectRegisteredSpecs(t *testing.T) {
	writable := baseSpec("create_file")
	writable.Effect = EffectFSWrite
	writable.Mutates = true
	writable.NeedsAuth = true
	writable.PathParams = []string{"target_file", "content"}
	writable.Trunc = TruncFirst
	writable.Display = DisplayCompact
	writable.ModelSlot = "create_file"
	writable.Brief = "新建文件"
	writable.Frequency = Cold

	interactive := baseSpec("ask_user_for_help")
	interactive.Effect = EffectFlow
	interactive.BlocksOnUser = true
	interactive.Serial = true
	interactive.Frequency = Hot

	protected := baseSpec("deepthinking")
	protected.Effect = EffectCogDeep
	protected.Trunc = TruncProtected

	installRegistry(t, writable, interactive, protected)

	if IsReadOnly("create_file") {
		t.Error("IsReadOnly(create_file) = true, want false（Mutates）")
	}
	if got := TruncationPriority("create_file"); got != 0 {
		t.Errorf("TruncationPriority(create_file) = %d, want 0（TruncFirst）", got)
	}
	if !IsCompactDisplay("create_file") {
		t.Error("IsCompactDisplay(create_file) = false, want true")
	}
	if got := ModelSlot("create_file"); got != "create_file" {
		t.Errorf("ModelSlot(create_file) = %q, want create_file", got)
	}
	if got := strings.Join(PathParams("create_file"), ","); got != "target_file,content" {
		t.Errorf("PathParams(create_file) = %v, want [target_file content]", got)
	}
	if EffectOf("create_file") != EffectFSWrite {
		t.Errorf("EffectOf(create_file) = %v, want fs_write", EffectOf("create_file"))
	}
	if Brief("create_file") != "新建文件" {
		t.Errorf("Brief(create_file) = %q, want 新建文件", Brief("create_file"))
	}

	if !IsInteractive("ask_user_for_help") {
		t.Error("IsInteractive(ask_user_for_help) = false, want true（必须豁免工具级超时）")
	}
	if !IsSerial("ask_user_for_help") {
		t.Error("IsSerial(ask_user_for_help) = false, want true")
	}
	if IsReadOnly("ask_user_for_help") {
		t.Error("IsReadOnly(ask_user_for_help) = true, want false（Serial）")
	}

	if got := TruncationPriority("deepthinking"); got != -1 {
		t.Errorf("TruncationPriority(deepthinking) = %d, want -1（受保护，永不截断）", got)
	}
	if IsReadOnly("deepthinking") != true {
		t.Error("IsReadOnly(deepthinking) = false, want true（认知工具可并行）")
	}
}

// 派生恒等式：IsReadOnly() == !Mutates && !Serial。
// 这个"规则单一"是全表并发判定的唯一来源，任何一侧偷偷加条件都会让
// toolbatch 的分批结果与 Spec 脱钩。
func TestDerivedMetadataIsInternallyConsistent(t *testing.T) {
	combos := []struct {
		name    string
		mutates bool
		serial  bool
		want    bool
	}{
		{"neither", false, false, true},
		{"mutates", true, false, false},
		{"serial", false, true, false},
		{"both", true, true, false},
	}
	for _, c := range combos {
		c := c
		t.Run(c.name, func(t *testing.T) {
			s := baseSpec("read_file")
			s.Mutates = c.mutates
			s.Serial = c.serial
			if got := s.IsReadOnly(); got != c.want {
				t.Errorf("IsReadOnly() = %v, want %v (Mutates=%v Serial=%v)", got, c.want, c.mutates, c.serial)
			}
		})
	}
}
