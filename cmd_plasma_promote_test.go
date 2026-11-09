package main

import (
	"bytes"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/plasma"
)

// promote 的三条分支：runs 不足必须失败且不动 index；--force 升档；已 promoted 只改指针。
func TestRunPlasmaPromoteBranches(t *testing.T) {
	p7Isolate(t)
	store := plasma.NewStore(config.PlasmaStoreDir())
	b, err := plasma.Elaborate([]byte(p7SpecJSON), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	hash, err := store.Put(b, &plasma.Report{Grade: plasma.GradeProvisional})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// ① provisional + runs=0 ⇒ 拒绝，且 index 不被改动（失败的 promote 不能有副作用）。
	var out bytes.Buffer
	if err := runPlasmaPromote(b.Name, hash, 0, false, false, &out); err == nil {
		t.Fatalf("runs=0 的 provisional 被 promote 了：\n%s", out.String())
	} else if !strings.Contains(err.Error(), "未升档") {
		t.Errorf("错误文本没有说明原因: %v", err)
	}
	if _, meta, err := store.Load(b.Name, hash); err != nil {
		t.Fatal(err)
	} else if meta.Grade != plasma.GradeProvisional {
		t.Errorf("失败的 promote 改了 grade: %q", meta.Grade)
	}

	// ② --force ⇒ promoted。
	out.Reset()
	if err := runPlasmaPromote(b.Name, hash, 0, true, false, &out); err != nil {
		t.Fatalf("promote --force: %v\n%s", err, out.String())
	}
	_, meta, err := store.Load(b.Name, hash)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Grade != plasma.GradePromoted {
		t.Errorf("--force 后 grade = %q, want promoted", meta.Grade)
	}

	// ③ 已经 promoted 的版本再 promote（回滚语义）必须成功，而不是被 runs 判据挡住。
	out.Reset()
	if err := runPlasmaPromote(b.Name, hash, 5, false, false, &out); err != nil {
		t.Fatalf("已 promoted 的版本回滚失败: %v\n%s", err, out.String())
	}
}
