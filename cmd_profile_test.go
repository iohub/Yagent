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

// `--from default` 在全新机器上要回退到内嵌模板：default 是内置档，它的文件还没生成
// 不等于"源不存在"。同时**不得**顺手生成 default 的 config.toml —— create 不是 setup。
func TestProfileCreateFromDefaultWhenDefaultMissing(t *testing.T) {
	home := isolateConfigEnv(t, "", "")

	var out bytes.Buffer
	if err := runProfileCreate(&out, "copy1", config.DefaultProfileName); err != nil {
		t.Fatalf("runProfileCreate(copy1, --from default) unexpected error: %v", err)
	}

	got, err := os.ReadFile(profilePath(t, home, "copy1"))
	if err != nil {
		t.Fatalf("read created profile: %v", err)
	}
	// 参照物由同一个生成器产出一份：口径是"内容等于内嵌模板"，不是"非空"。
	refPath := filepath.Join(t.TempDir(), "ref.toml")
	if _, err := config.EnsureConfigExists(refPath); err != nil {
		t.Fatalf("EnsureConfigExists(ref) unexpected error: %v", err)
	}
	want, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatalf("read template reference: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("created profile differs from the embedded template (%d vs %d bytes)", len(got), len(want))
	}
	if !strings.Contains(out.String(), "default has not been created yet") {
		t.Errorf("stdout = %q, want it to explain the fallback", out.String())
	}
	if !strings.Contains(out.String(), "built-in template") {
		t.Errorf("stdout = %q, want it to say the template was used", out.String())
	}
	if _, statErr := os.Stat(profilePath(t, home, config.DefaultProfileName)); !os.IsNotExist(statErr) {
		t.Errorf("--from default created default's config.toml (stat err = %v), want it untouched", statErr)
	}
}

// 回退只对 default 生效：`--from <命名档>` 而源文件不在，仍然是操作失败（exit 1），
// 不能被上面那条回退逻辑误伤成"给你一份模板"。
func TestProfileCreateFromMissingNamedProfileFails(t *testing.T) {
	home := isolateConfigEnv(t, "", "")

	var out bytes.Buffer
	err := runProfileCreate(&out, "copy2", "work")
	if err == nil {
		t.Fatal("runProfileCreate(copy2, --from work) = nil, want an error")
	}
	if isUsageError(err) {
		t.Errorf("error %v is a usage error, want a plain failure (exit 1): a missing source is not a typo", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want no output on failure", out.String())
	}
	if _, statErr := os.Stat(profilePath(t, home, "copy2")); !os.IsNotExist(statErr) {
		t.Errorf("a failed create still produced a file (stat err = %v)", statErr)
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

// executeRootArgs 从 rootCmd 起跑一次命令，返回错误与捕获到 stdout/stderr 的原文。
//
// 必须从 root 起跑而不能 `profileCmd.Execute()`：cobra 的 ExecuteC 见到父命令就转交
// Root()，直接调用子命令会拿测试进程的 os.Args 去跑根命令（那时会真的进 TUI 装配路径）。
func executeRootArgs(t *testing.T, args ...string) (error, string) {
	t.Helper()
	var sink bytes.Buffer
	rootCmd.SetOut(&sink)
	rootCmd.SetErr(&sink)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	err := rootCmd.Execute()
	return err, sink.String()
}

// flag 解析错误必须落到 *usageError（退出码 2），而不是被 cobra 原样抛成 exit 1。
//
// `-x` 在 Run 之前就失败，因此这条用例碰不到 failProfile 里的 os.Exit ——
// 那条路只能靠真实二进制冒烟验证。
func TestProfileFlagErrorBecomesUsageError(t *testing.T) {
	isolateConfigEnv(t, "", "")

	err, sink := executeRootArgs(t, "profile", "create", "-x")
	if err == nil {
		t.Fatal("`profile create -x` returned nil, want the unknown-flag error")
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("error %T = %v, want *usageError so main exits 2", err, err)
	}
	if !isUsageError(err) {
		t.Errorf("isUsageError(%v) = false, want true (that is main's exit-code branch)", err)
	}
	// 包装不许吞掉 cobra 的原文：用户得看懂哪个 flag 写错了。
	if !strings.Contains(err.Error(), "unknown shorthand flag") {
		t.Errorf("error = %v, want cobra's message preserved", err)
	}
	if !strings.Contains(sink, "unknown shorthand flag") {
		t.Errorf("captured output = %q, want the error printed for the user", sink)
	}
}

// 参数个数写错与 flag 写错同类：cobra 的 Args 校验在 Run 之前失败，也得是退出码 2。
func TestProfileArgCountErrorBecomesUsageError(t *testing.T) {
	isolateConfigEnv(t, "", "")

	err, _ := executeRootArgs(t, "profile", "create")
	if err == nil {
		t.Fatal("`profile create` without a name returned nil, want the arg-count error")
	}
	if !isUsageError(err) {
		t.Fatalf("error %T = %v, want *usageError (exit 2)", err, err)
	}
	if !strings.Contains(err.Error(), "accepts 1 arg(s)") {
		t.Errorf("error = %v, want cobra's arg-count message preserved", err)
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

// 全新机器（连 ~/.yagent 都不存在）：default 是内置档，必须出现在列表里，
// 并标注"文件还没生成"。这是"default 在 UI 上消失"那个体验断裂的回归用例。
func TestProfileListFreshHomeShowsDefaultNotCreated(t *testing.T) {
	home := isolateConfigEnv(t, "", "")

	var out bytes.Buffer
	if err := runProfileList(&out); err != nil {
		t.Fatalf("runProfileList on a fresh home unexpected error: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("runProfileList printed %d lines, want exactly the built-in default row:\n%s",
			len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "* default") {
		t.Errorf("default row = %q, want it marked active (default is the default profile)", lines[0])
	}
	if !strings.Contains(lines[0], "not created yet") {
		t.Errorf("default row = %q, want a not-created note", lines[0])
	}
	if !strings.Contains(lines[0], profilePath(t, home, config.DefaultProfileName)) {
		t.Errorf("default row = %q, want the config.toml path", lines[0])
	}

	// list 是只读的：全新 home 下连 ~/.yagent 都不该被创建出来。
	if _, err := os.Stat(filepath.Join(home, ".yagent")); !os.IsNotExist(err) {
		t.Errorf("profile list created %s (stat err = %v), want it read-only",
			filepath.Join(home, ".yagent"), err)
	}
}

// default 的 config.toml 已经存在时，那行不得再带"未创建"标注（否则用户会去跑 setup）。
func TestProfileListDefaultRowHasNoNoteWhenFileExists(t *testing.T) {
	isolateConfigEnv(t, "", "")
	seedDefault(t)

	var out bytes.Buffer
	if err := runProfileList(&out); err != nil {
		t.Fatalf("runProfileList unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "not created yet") {
		t.Errorf("stdout = %q, want no not-created note when config.toml exists", out.String())
	}
	if !strings.HasPrefix(out.String(), "* default") {
		t.Errorf("stdout = %q, want default first and marked active", out.String())
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
