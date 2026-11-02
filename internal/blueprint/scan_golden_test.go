package blueprint

import (
	"reflect"
	"strings"
	"testing"
)

func TestScanSelfGolden(t *testing.T) {
	snap, err := Scan("../..", DefaultOptions())
	if err != nil {
		t.Fatalf("本仓自扫失败: %v", err)
	}
	if snap.ModulePath != "yagent" {
		t.Fatalf("ModulePath = %q, 要 %q", snap.ModulePath, "yagent")
	}
	if len(snap.Leaves) != 45 {
		t.Fatalf("片数 = %d, golden 是 45 —— 形状变了，先确认变化来源再更新数字", len(snap.Leaves))
	}
	out, in := 0, 0
	for i := range snap.Leaves {
		out += snap.Leaves[i].EdgesOutTotal
		in += snap.Leaves[i].EdgesInTotal
	}
	if out != 146 || in != 146 {
		t.Fatalf("模块级边数 out=%d in=%d, golden 是 146/146 —— 形状变了，先确认变化来源", out, in)
	}
	for i := 1; i < len(snap.Leaves); i++ {
		if snap.Leaves[i-1].Dir >= snap.Leaves[i].Dir {
			t.Fatalf("Leaves 未按 Dir 排序: %q >= %q", snap.Leaves[i-1].Dir, snap.Leaves[i].Dir)
		}
	}
	var found bool
	for i := range snap.Leaves {
		l := &snap.Leaves[i]
		if l.Dir == "codeseek" {
			found = true
			if !l.Submodule || len(l.Files) != 0 {
				t.Fatalf("codeseek 应是整片 submodule leaf: sub=%v files=%d", l.Submodule, len(l.Files))
			}
		}
	}
	if !found {
		t.Fatal("codeseek submodule 未成为整片 leaf —— 目录分支跳过失效")
	}
	for i := range snap.Leaves {
		dir := snap.Leaves[i].Dir
		for _, seg := range strings.Split(dir, "/") {
			if DefaultBlacklistSet()[seg] {
				t.Fatalf("片路径 %q 含黑名单段 %q", dir, seg)
			}
		}
		if strings.HasPrefix(dir, "internal/blueprint/testdata") {
			t.Fatalf("fixture 目录 %q 不得进本仓实测（黑名单失效会污染 golden）", dir)
		}
	}
	for i := range snap.Leaves {
		if len(snap.Leaves[i].Fingerprint) != 64 {
			t.Fatalf("片 %q 指纹长度 %d, 要 64", snap.Leaves[i].Dir, len(snap.Leaves[i].Fingerprint))
		}
	}
	again, err := Scan("../..", DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap, again) {
		t.Fatal("本仓两次自扫结果不一致 —— 违反确定性（I15 前置）")
	}
}

func DefaultBlacklistSet() map[string]bool {
	set := make(map[string]bool, len(DefaultBlacklist))
	for _, name := range DefaultBlacklist {
		set[name] = true
	}
	return set
}
