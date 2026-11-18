package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/config"
)

// profilePath 是测试里的小工具：把档名换成 $HOME 下的真实路径。
func profilePath(t *testing.T, home, name string) string {
	t.Helper()
	path, err := config.ProfileConfigPath(filepath.Join(home, ".yagent", "config"), name)
	if err != nil {
		t.Fatalf("ProfileConfigPath(%q) unexpected error: %v", name, err)
	}
	return path
}

// seedDefault 生成 default 档（内嵌模板），返回其路径。
//
// 刻意不走 resolveConfigPath：那个会跟着 --profile 走，而这里要的正是"不是激活档的那一份"。
func seedDefault(t *testing.T) string {
	t.Helper()
	path, err := config.ProfileConfigPath(profileConfigDir(), config.DefaultProfileName)
	if err != nil {
		t.Fatalf("ProfileConfigPath(default) unexpected error: %v", err)
	}
	if _, err := config.EnsureConfigExists(path); err != nil {
		t.Fatalf("EnsureConfigExists(%s) unexpected error: %v", path, err)
	}
	return path
}

func TestProfileCreateCopiesDefault(t *testing.T) {
	home := isolateConfigEnv(t, "", "")
	defaultPath := seedDefault(t)

	var out bytes.Buffer
	if err := runProfileCreate(&out, "work", ""); err != nil {
		t.Fatalf("runProfileCreate(work) unexpected error: %v", err)
	}

	workPath := profilePath(t, home, "work")
	want, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("read default profile: %v", err)
	}
	got, err := os.ReadFile(workPath)
	if err != nil {
		t.Fatalf("read created profile: %v", err)
	}
	// 逐字节相同：复制不许经过 TOML 编解码（那会蒸发注释）。
	if !bytes.Equal(got, want) {
		t.Errorf("created profile differs from default:\n got %d bytes\nwant %d bytes", len(got), len(want))
	}
	st, err := os.Stat(workPath)
	if err != nil {
		t.Fatalf("stat created profile: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("created profile mode = %o, want 600", perm)
	}
	if !strings.Contains(out.String(), `created profile "work"`) {
		t.Errorf("stdout = %q, want a created-profile line", out.String())
	}
}

func TestProfileCreateFromSource(t *testing.T) {
	home := isolateConfigEnv(t, "", "")
	seedDefault(t)

	var out bytes.Buffer
	if err := runProfileCreate(&out, "base", ""); err != nil {
		t.Fatalf("runProfileCreate(base) unexpected error: %v", err)
	}

	// 改一份内容再当作源：证明 --from 抄的是指定档而不是 default。
	basePath := profilePath(t, home, "base")
	if err := os.WriteFile(basePath, []byte("# from base\n"), 0o600); err != nil {
		t.Fatalf("rewrite base profile: %v", err)
	}

	out.Reset()
	if err := runProfileCreate(&out, "team", "base"); err != nil {
		t.Fatalf("runProfileCreate(team, --from base) unexpected error: %v", err)
	}
	got, err := os.ReadFile(profilePath(t, home, "team"))
	if err != nil {
		t.Fatalf("read team profile: %v", err)
	}
	if string(got) != "# from base\n" {
		t.Errorf("team profile = %q, want the bytes of base", got)
	}
	if !strings.Contains(out.String(), `copied from "base"`) {
		t.Errorf("stdout = %q, want it to name the source profile", out.String())
	}
}

func TestProfileCreateFromTemplateWhenNoDefault(t *testing.T) {
	home := isolateConfigEnv(t, "", "")

	// 全新机器：default 都还没生成，create 要走内嵌模板那条路。
	var out bytes.Buffer
	if err := runProfileCreate(&out, "work", ""); err != nil {
		t.Fatalf("runProfileCreate(work) unexpected error: %v", err)
	}

	path := profilePath(t, home, "work")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created profile: %v", err)
	}
	if len(data) == 0 {
		t.Error("created profile is empty, want the embedded template")
	}
	if !bytes.Contains(data, []byte("use_provider")) {
		t.Errorf("created profile does not look like the template (no use_provider key)")
	}
	if !strings.Contains(out.String(), "built-in template") {
		t.Errorf("stdout = %q, want it to say the template was used", out.String())
	}
	if _, statErr := os.Stat(profilePath(t, home, config.DefaultProfileName)); !os.IsNotExist(statErr) {
		t.Errorf("creating a named profile also created default (stat err = %v), want it untouched", statErr)
	}
}

func TestProfileCreateRejections(t *testing.T) {
	home := isolateConfigEnv(t, "", "")
	seedDefault(t)

	var out bytes.Buffer
	if err := runProfileCreate(&out, "work", ""); err != nil {
		t.Fatalf("first create should succeed: %v", err)
	}

	cases := []struct {
		name    string
		profile string
		from    string
	}{
		{name: "already exists", profile: "work"},
		{name: "empty name", profile: ""},
		{name: "traversal", profile: "../escape"},
		{name: "reserved name collides with default", profile: "config"},
		{name: "copy onto itself", profile: "copy", from: "copy"},
		{name: "source missing", profile: "other", from: "ghost"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out.Reset()
			err := runProfileCreate(&out, c.profile, c.from)
			if err == nil {
				t.Fatalf("runProfileCreate(%q, from=%q) = nil, want error", c.profile, c.from)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want no output on failure", out.String())
			}
		})
	}

	// 拒绝之后磁盘上不该多出任何东西（尤其是 ../escape 这种）。
	if _, err := os.Stat(filepath.Join(home, ".yagent", "escape.toml")); !os.IsNotExist(err) {
		t.Errorf("a rejected name still produced a file: stat err = %v", err)
	}
	// 已存在的档必须原样保留。
	if _, err := os.Stat(profilePath(t, home, "work")); err != nil {
		t.Errorf("existing profile was damaged by a rejected create: %v", err)
	}
}

func TestProfileCreateUsageErrorsAreExit2(t *testing.T) {
	isolateConfigEnv(t, "", "")
	seedDefault(t)

	for _, profile := range []string{"", "a/b", "..", "config"} {
		err := runProfileCreate(&bytes.Buffer{}, profile, "")
		if err == nil {
			t.Fatalf("runProfileCreate(%q) = nil, want usage error", profile)
		}
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("runProfileCreate(%q) error %T = %v, want *usageError (exit 2)", profile, err, err)
		}
	}
}

func TestProfileListMarksActive(t *testing.T) {
	home := isolateConfigEnv(t, "", "work")
	seedDefault(t)

	var out bytes.Buffer
	if err := runProfileCreate(&out, "work", ""); err != nil {
		t.Fatalf("runProfileCreate(work) unexpected error: %v", err)
	}

	out.Reset()
	if err := runProfileList(&out); err != nil {
		t.Fatalf("runProfileList unexpected error: %v", err)
	}
	// 只剪掉行尾换行：TrimSpace 会把 "{marker} {name}" 的前导空格吃掉，
	// 于是"没被标记"和"被标记"就分不出来了。
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("runProfileList printed %d lines, want 2:\n%s", len(lines), out.String())
	}
	// default 排首位且没被标记，work 是激活档。
	if !strings.HasPrefix(lines[0], "  default") {
		t.Errorf("first line = %q, want an unmarked default row", lines[0])
	}
	if !strings.HasPrefix(lines[1], "* work") {
		t.Errorf("second line = %q, want work marked active", lines[1])
	}
	if !strings.Contains(lines[2-1], profilePath(t, home, "work")) {
		t.Errorf("active row = %q, want it to show the file path", lines[1])
	}
}

func TestProfileListEmptyDir(t *testing.T) {
	home := isolateConfigEnv(t, "", "")

	var out bytes.Buffer
	if err := runProfileList(&out); err != nil {
		t.Fatalf("runProfileList on an empty home unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "no profiles in") {
		t.Errorf("stdout = %q, want the empty-list hint", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".yagent")); !os.IsNotExist(err) {
		t.Errorf("profile list created %s (stat err = %v), want it read-only", filepath.Join(home, ".yagent"), err)
	}
}

func TestProfileDeleteRefusesDefault(t *testing.T) {
	isolateConfigEnv(t, "", "")
	defaultPath := seedDefault(t)

	err := runProfileDelete(&bytes.Buffer{}, config.DefaultProfileName)
	if err == nil {
		t.Fatal("runProfileDelete(default) = nil, want refusal")
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Errorf("error %T = %v, want *usageError (exit 2)", err, err)
	}
	if _, statErr := os.Stat(defaultPath); statErr != nil {
		t.Errorf("default config was deleted despite the refusal: %v", statErr)
	}
}

func TestProfileDeleteRemovesFile(t *testing.T) {
	home := isolateConfigEnv(t, "", "")
	seedDefault(t)

	var out bytes.Buffer
	if err := runProfileCreate(&out, "work", ""); err != nil {
		t.Fatalf("runProfileCreate(work) unexpected error: %v", err)
	}

	out.Reset()
	if err := runProfileDelete(&out, "work"); err != nil {
		t.Fatalf("runProfileDelete(work) unexpected error: %v", err)
	}
	if _, err := os.Stat(profilePath(t, home, "work")); !os.IsNotExist(err) {
		t.Errorf("profile file still exists after delete: %v", err)
	}
	if !strings.Contains(out.String(), `deleted profile "work"`) {
		t.Errorf("stdout = %q, want a deleted-profile line", out.String())
	}

	// 再删一次就是"不存在"（exit 1），不是用法错。
	err := runProfileDelete(&bytes.Buffer{}, "work")
	if err == nil {
		t.Fatal("second delete = nil, want an error")
	}
	var ue *usageError
	if errors.As(err, &ue) {
		t.Errorf("second delete error = %v, want a plain error (exit 1), not a usage error", err)
	}
}

func TestProfileDeleteWarnsWhenActive(t *testing.T) {
	home := isolateConfigEnv(t, "", "work")
	seedDefault(t)

	if err := runProfileCreate(&bytes.Buffer{}, "work", ""); err != nil {
		t.Fatalf("runProfileCreate(work) unexpected error: %v", err)
	}

	var out bytes.Buffer
	var deleteErr error
	stderr := captureStderr(t, func() {
		deleteErr = runProfileDelete(&out, "work")
	})
	if deleteErr != nil {
		t.Fatalf("runProfileDelete(active) unexpected error: %v", deleteErr)
	}
	if !strings.Contains(stderr, "is the active profile") {
		t.Errorf("stderr = %q, want a warning that the active profile is being deleted", stderr)
	}
	// 警告归警告，删除照做。
	if _, err := os.Stat(profilePath(t, home, "work")); !os.IsNotExist(err) {
		t.Errorf("active profile was not deleted after the warning: %v", err)
	}
}

func TestProfilePathPrintsAbsolute(t *testing.T) {
	home := isolateConfigEnv(t, "", "work")

	var out bytes.Buffer
	if err := runProfilePath(&out, "work"); err != nil {
		t.Fatalf("runProfilePath(work) unexpected error: %v", err)
	}
	got := strings.TrimSpace(out.String())
	if want := profilePath(t, home, "work"); got != want {
		t.Errorf("runProfilePath(work) = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("runProfilePath(work) = %q, want an absolute path", got)
	}

	// 无参数 = 当前激活档（这里由 --profile 决定）。
	out.Reset()
	if err := runProfilePath(&out, ""); err != nil {
		t.Fatalf("runProfilePath(active) unexpected error: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != profilePath(t, home, "work") {
		t.Errorf("runProfilePath(active) = %q, want the work profile path", got)
	}

	// 路径不存在也照打：它的用途是脚本化，不是探测。
	out.Reset()
	if err := runProfilePath(&out, "ghost"); err != nil {
		t.Fatalf("runProfilePath(ghost) unexpected error: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != profilePath(t, home, "ghost") {
		t.Errorf("runProfilePath(ghost) = %q, want the computed path", got)
	}
}

func TestProfilePathHonorsConfigFlag(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "team.toml")
	isolateConfigEnv(t, explicit, "work")

	var out bytes.Buffer
	var err error
	stderr := captureStderr(t, func() {
		err = runProfilePath(&out, "")
	})
	if err != nil {
		t.Fatalf("runProfilePath with --config unexpected error: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != explicit {
		t.Errorf("runProfilePath = %q, want --config path %q", got, explicit)
	}
	if !strings.Contains(stderr, "ignored because --config") {
		t.Errorf("stderr = %q, want the --profile-ignored warning", stderr)
	}
}
