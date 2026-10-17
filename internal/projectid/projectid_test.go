package projectid_test

// projectid 的算法是 rollout 目录、artifact 目录与蓝图目录共同的主键。
// 这里的 golden 表把这些字符串钉死：任何一次"顺手改一下截断长度/哈希位数"
// 都会让既有数据变成孤儿目录，所以它必须是字面量，而不是重新算一遍。

import (
	"strings"
	"testing"

	"yagent/internal/agents/director"
	"yagent/internal/projectid"
)

func TestComputeGoldenTable(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"", "default"},
		{"/", "root_8a5edab2"},
		{".", "root_cdb4ee2a"},
		{"/home/user/myproj", "myproj_f1022b97"},
		{"/home/user/my project.v2", "my_project_v2_55d327da"},
		{"/Users/ydl/iohub/yagent", "yagent_77506fcd"},
		{"/home/a", "a_29ce3852"},
	}
	for _, c := range cases {
		if got := projectid.Compute(c.path); got != c.want {
			t.Errorf("Compute(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestComputeTruncatesBaseTo100(t *testing.T) {
	got := projectid.Compute("/x/" + strings.Repeat("a", 150))
	if len(got) != 109 {
		t.Errorf("len = %d, want 109 (100 base + \"_\" + 8 hex)", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 100)+"_") {
		t.Errorf("前缀不符: %q", got[:110])
	}
}

// director.ComputeProjectID 现在只是转发。这条断言存在的理由是防止两边各自演化：
// 一旦转发改成第二份实现，rollout 与蓝图就会分目录（本仓已经有 `sha[:12]` 那个先例）。
func TestDirectorForwardsToProjectid(t *testing.T) {
	for _, p := range []string{"", "/Users/ydl/iohub/yagent", "/home/user/my project.v2"} {
		if a, b := director.ComputeProjectID(p), projectid.Compute(p); a != b {
			t.Errorf("director=%q 与 projectid=%q 对 %q 不一致", a, b, p)
		}
	}
}
