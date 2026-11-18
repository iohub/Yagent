package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateProfileName 钉住文法：接受什么、拒绝什么。
//
// 拒绝清单的重点是"路径穿越与隐藏文件"这一类：它们不会报错，只会写到别处去。
func TestValidateProfileName(t *testing.T) {
	valid := []string{
		"a",
		"A",
		"0",
		"work",
		"team-1",
		"x_y",
		"MixedCase_99",
		strings.Repeat("a", 64), // 上限：首字符 + 63
	}
	for _, name := range valid {
		if err := ValidateProfileName(name); err != nil {
			t.Errorf("ValidateProfileName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",                      // 空串不是"默认档"，是写错了
		" ",                     // 只有空白
		".",                     //
		"..",                    // 上级目录
		"../etc",                // 穿越
		"a/b",                   // 分隔符
		"a\\b",                  // Windows 分隔符
		"/abs",                  //
		".hidden",               // 以点开头 => 隐藏文件
		"-x",                    // 以连字符开头
		"_x",                    // 以下划线开头
		"a.b",                   // 点号一律不允许（能造扩展名歧义）
		"a b",                   // 空格（不 trim 的话会造出怪文件名）
		"工作",                    // 非 ASCII
		"a<b",                   //
		strings.Repeat("a", 65), // 超长
	}
	for _, name := range invalid {
		if err := ValidateProfileName(name); err == nil {
			t.Errorf("ValidateProfileName(%q) = nil, want error", name)
		}
	}
}

// TestNormalizeProfileName 钉住"没写"与"写了"的分界，以及 trim 的语义。
func TestNormalizeProfileName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", DefaultProfileName},
		{"   ", DefaultProfileName},
		{"work", "work"},
		{"  work  ", "work"}, // shell 里多打的空格不该造出一个带空格的档名
		{"default", DefaultProfileName},
	}
	for _, c := range cases {
		if got := NormalizeProfileName(c.in); got != c.want {
			t.Errorf("NormalizeProfileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestProfileConfigPath 钉住布局：default 走历史路径，命名档走 <name>.toml，
// 且结果必须落在 configDir 里（不越界、绝对路径仍绝对）。
func TestProfileConfigPath(t *testing.T) {
	dir := t.TempDir()

	cases := []struct{ name, want string }{
		{"default", filepath.Join(dir, "config.toml")},
		{"", filepath.Join(dir, "config.toml")}, // 空 == 没指定 == default
		{"work", filepath.Join(dir, "work.toml")},
		{"Team_1", filepath.Join(dir, "Team_1.toml")},
	}
	for _, c := range cases {
		got, err := ProfileConfigPath(dir, c.name)
		if err != nil {
			t.Fatalf("ProfileConfigPath(%q, %q) unexpected error: %v", dir, c.name, err)
		}
		if got != c.want {
			t.Errorf("ProfileConfigPath(%q, %q) = %q, want %q", dir, c.name, got, c.want)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("ProfileConfigPath(%q, %q) = %q, want absolute path", dir, c.name, got)
		}
		if filepath.Dir(got) != filepath.Clean(dir) {
			t.Errorf("ProfileConfigPath(%q, %q) = %q escapes the config dir", dir, c.name, got)
		}
	}

	// 名字不合法 / 与 default 抢同一个文件 → 都必须报错，而不是"猜一个路径"。
	for _, name := range []string{"config", "..", "a/b", "a\\b", ".hidden", "-x"} {
		if _, err := ProfileConfigPath(dir, name); err == nil {
			t.Errorf("ProfileConfigPath(%q, %q) = nil error, want rejection", dir, name)
		}
	}

	// 只有空白 == 没写（NormalizeProfileName 会 trim）：它归一到 default，不是错误。
	blank, err := ProfileConfigPath(dir, "   ")
	if err != nil {
		t.Fatalf("ProfileConfigPath(blank) unexpected error: %v", err)
	}
	if want := filepath.Join(dir, "config.toml"); blank != want {
		t.Errorf("ProfileConfigPath(blank) = %q, want %q", blank, want)
	}

	// 空的 configDir 是调用方 bug，不许退化成 CWD。
	if _, err := ProfileConfigPath("", "work"); err == nil {
		t.Error("ProfileConfigPath(\"\", \"work\") = nil error, want rejection")
	}

	// 相对 configDir（home 不可用时的兜底）保持相对：不要偷偷 Abs 成别的地方。
	got, err := ProfileConfigPath("config", "work")
	if err != nil {
		t.Fatalf("ProfileConfigPath(\"config\", \"work\") unexpected error: %v", err)
	}
	if want := filepath.Join("config", "work.toml"); got != want {
		t.Errorf("ProfileConfigPath(\"config\", \"work\") = %q, want %q", got, want)
	}
}

// TestProfileNameFromPath 钉住反向推导与往返一致：`profile list`/启动日志/失败提示
// 都靠它把一份文件说成"哪个档"。
func TestProfileNameFromPath(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/home/u/.yagent/config/config.toml", DefaultProfileName},
		{"config/config.toml", DefaultProfileName},
		{"config.toml", DefaultProfileName},
		{"/home/u/.yagent/config/work.toml", "work"},
		{"work.toml", "work"},
		{"/tmp/custom.conf", "custom"}, // --config 可以指任意文件：去掉扩展名即可
		{"/tmp/noext", "noext"},
	}
	for _, c := range cases {
		if got := ProfileNameFromPath(c.path); got != c.want {
			t.Errorf("ProfileNameFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}

	dir := t.TempDir()
	for _, name := range []string{DefaultProfileName, "work", "Team_1"} {
		path, err := ProfileConfigPath(dir, name)
		if err != nil {
			t.Fatalf("ProfileConfigPath(%q) unexpected error: %v", name, err)
		}
		if got := ProfileNameFromPath(path); got != name {
			t.Errorf("round trip %q -> %q -> %q, want back to %q", name, path, got, name)
		}
	}
}

// TestProfileNamesInDir 钉住列举规则：目录不存在给空列表、中间态与非 toml 与非法名都不列，
// default 排首位其余按字典序。
func TestProfileNamesInDir(t *testing.T) {
	dir := t.TempDir()

	names, err := ProfileNamesInDir(dir)
	if err != nil {
		t.Fatalf("ProfileNamesInDir(empty dir) unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("ProfileNamesInDir(empty dir) = %v, want empty", names)
	}

	// 目录整个不存在：`yagent profile list` 在全新机器上必须能跑。
	names, err = ProfileNamesInDir(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatalf("ProfileNamesInDir(missing dir) unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("ProfileNamesInDir(missing dir) = %v, want empty", names)
	}

	files := []string{
		"config.toml", // default
		"work.toml",   //
		"Team.toml",   // 大写合法（注意别用 Work/work 这种只差大小写的组合：
		// macOS 的 APFS 默认大小写不敏感，两条夹具会塌成同一个文件）
		"alpha.toml",      //
		"work.toml.1.tmp", // AtomicWrite 的中间态
		"notes.md",        // 不是 toml
		"bad name.toml",   // stem 非法：磁盘上存在但 --profile 永远选不中
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", f, err)
		}
	}
	// 目录名也以 .toml 结尾：不是文件，不该被列。
	if err := os.Mkdir(filepath.Join(dir, "sub.toml"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}

	names, err = ProfileNamesInDir(dir)
	if err != nil {
		t.Fatalf("ProfileNamesInDir unexpected error: %v", err)
	}
	want := []string{DefaultProfileName, "Team", "alpha", "work"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("ProfileNamesInDir = %v, want %v", names, want)
	}
}
