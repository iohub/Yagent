package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/config"
)

// isolateConfigEnv 接管 $HOME 与两个包级 flag，返回临时 home。
//
// 必须显式清空 YAGENT_PROFILE：开发机上很可能正导着它，而它会穿过 t.Setenv 的隔离
// 把每条用例带到别的档上去 —— 那种失败看起来像逻辑错，实际是环境污染。
func isolateConfigEnv(t *testing.T, configPath, profile string) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(profileEnvVar, "")
	oldConfig, oldProfile := configPathFlag, profileFlag
	configPathFlag, profileFlag = configPath, profile
	t.Cleanup(func() {
		configPathFlag, profileFlag = oldConfig, oldProfile
	})
	return home
}

// captureStderr 把一次调用的 stderr 抓回来：告警文案是行为的一部分（用户必须看到），
// 所以要有断言钉住它。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatalf("close stderr pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	_ = r.Close()
	return buf.String()
}

func TestResolveProfileName(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		env     string
		want    string
		wantErr bool
	}{
		{name: "nothing set falls back to default", want: config.DefaultProfileName},
		{name: "flag wins over env", flag: "work", env: "team", want: "work"},
		{name: "env used when flag absent", env: "team", want: "team"},
		{name: "flag is trimmed", flag: "  work  ", want: "work"},
		{name: "empty env means unset", env: "", want: config.DefaultProfileName},
		{name: "invalid flag rejected", flag: "a/b", wantErr: true},
		{name: "traversal rejected", flag: "..", wantErr: true},
		{name: "hidden rejected", flag: ".hidden", wantErr: true},
		{name: "invalid env rejected", env: "a b", wantErr: true},
		// 文法通过、由 ProfileConfigPath 拒绝（会撞上 default 的 config.toml）。
		{name: "reserved name passes grammar", flag: "config", want: "config"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolateConfigEnv(t, "", c.flag)
			t.Setenv(profileEnvVar, c.env)

			got, err := resolveProfileName()
			if c.wantErr {
				if err == nil {
					t.Fatalf("resolveProfileName() = %q, want error", got)
				}
				var ue *usageError
				if !errors.As(err, &ue) {
					t.Errorf("resolveProfileName() error %T = %v, want *usageError (exit 2)", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProfileName() unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("resolveProfileName() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestResolveConfigPathPrecedence 钉住四级优先级与"要不要创建"的边界。
//
// 最要紧的两条：
//   - 不带参数时路径与旧实现逐字节相同（$HOME/.yagent/config/config.toml）；
//   - --config 永不创建文件（加 profile 之前就是这样，写错的显式路径必须失败）。
func TestResolveConfigPathPrecedence(t *testing.T) {
	t.Run("default profile is the historical path", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "")

		path, created, err := resolveConfigPath(true)
		if err != nil {
			t.Fatalf("resolveConfigPath(true) unexpected error: %v", err)
		}
		want := filepath.Join(home, ".yagent", "config", "config.toml")
		if path != want {
			t.Errorf("resolveConfigPath(true) = %q, want %q", path, want)
		}
		if !created {
			t.Error("created = false, want true (template must be generated on first run)")
		}
		st, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("config file mode = %o, want 600", perm)
		}
	})

	t.Run("profile flag selects a named file", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "work")

		path, created, err := resolveConfigPath(true)
		if err != nil {
			t.Fatalf("resolveConfigPath(true) unexpected error: %v", err)
		}
		want := filepath.Join(home, ".yagent", "config", "work.toml")
		if path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
		if !created {
			t.Error("created = false, want true (named profile generated from the template)")
		}
	})

	t.Run("env var selects a named file", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "")
		t.Setenv(profileEnvVar, "team")

		path, _, err := resolveConfigPath(false)
		if err != nil {
			t.Fatalf("resolveConfigPath(false) unexpected error: %v", err)
		}
		if want := filepath.Join(home, ".yagent", "config", "team.toml"); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "work")
		t.Setenv(profileEnvVar, "team")

		path, _, err := resolveConfigPath(false)
		if err != nil {
			t.Fatalf("resolveConfigPath(false) unexpected error: %v", err)
		}
		if want := filepath.Join(home, ".yagent", "config", "work.toml"); path != want {
			t.Errorf("path = %q, want %q (flag must win)", path, want)
		}
	})

	t.Run("config flag wins verbatim and never creates", func(t *testing.T) {
		explicit := filepath.Join(t.TempDir(), "explicit.toml")
		isolateConfigEnv(t, explicit, "")

		path, created, err := resolveConfigPath(true)
		if err != nil {
			t.Fatalf("resolveConfigPath(true) unexpected error: %v", err)
		}
		if path != explicit {
			t.Errorf("path = %q, want verbatim %q", path, explicit)
		}
		if created {
			t.Error("created = true, want false (an explicit --config path is never generated)")
		}
		if _, statErr := os.Stat(explicit); !os.IsNotExist(statErr) {
			t.Errorf("explicit config file was created (stat err = %v), want it left alone", statErr)
		}
	})

	t.Run("config flag beats profile flag with a warning", func(t *testing.T) {
		explicit := filepath.Join(t.TempDir(), "explicit.toml")
		isolateConfigEnv(t, explicit, "work")

		var path string
		var err error
		stderr := captureStderr(t, func() {
			path, _, err = resolveConfigPath(true)
		})
		if err != nil {
			t.Fatalf("resolveConfigPath(true) unexpected error: %v", err)
		}
		if path != explicit {
			t.Errorf("path = %q, want %q (--config wins)", path, explicit)
		}
		if !strings.Contains(stderr, "ignored because --config") {
			t.Errorf("stderr = %q, want a warning that --profile was ignored", stderr)
		}
	})

	t.Run("createIfMissing false has no side effects", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "work")

		path, created, err := resolveConfigPath(false)
		if err != nil {
			t.Fatalf("resolveConfigPath(false) unexpected error: %v", err)
		}
		if created {
			t.Error("created = true, want false")
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("profile file exists after a side-effect-free resolve (stat err = %v)", statErr)
		}
		if _, statErr := os.Stat(filepath.Join(home, ".yagent", "config")); !os.IsNotExist(statErr) {
			t.Errorf("config dir was created by resolveConfigPath(false), want nothing written")
		}
	})

	t.Run("invalid profile name is a usage error", func(t *testing.T) {
		isolateConfigEnv(t, "", "../etc")

		_, _, err := resolveConfigPath(true)
		if err == nil {
			t.Fatal("resolveConfigPath(true) = nil error, want rejection")
		}
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("error %T = %v, want *usageError (exit 2)", err, err)
		}
	})

	t.Run("reserved name is rejected by the path layer", func(t *testing.T) {
		isolateConfigEnv(t, "", "config")

		_, _, err := resolveConfigPath(true)
		if err == nil {
			t.Fatal("resolveConfigPath(true) = nil error, want rejection for profile \"config\"")
		}
		if !strings.Contains(err.Error(), "config.toml") {
			t.Errorf("error = %q, want it to name the colliding file", err)
		}
	})
}

// TestGetConfigPathMatchesResolve 确认 getConfigPath 就是"要创建"的那一档 ——
// 两处一旦分叉，plasma 与主启动会读不同的文件。
func TestGetConfigPathMatchesResolve(t *testing.T) {
	home := isolateConfigEnv(t, "", "work")

	gotPath, gotCreated, gotErr := getConfigPath()
	wantPath, _, wantErr := resolveConfigPath(false)
	if gotErr != nil || wantErr != nil {
		t.Fatalf("unexpected errors: getConfigPath=%v resolveConfigPath=%v", gotErr, wantErr)
	}
	if gotPath != wantPath {
		t.Errorf("getConfigPath() = %q, resolveConfigPath(false) = %q (two code paths must agree)",
			gotPath, wantPath)
	}
	if !gotCreated {
		t.Error("getConfigPath() created = false, want true for a missing profile")
	}
	// 再问一次：文件已存在，created 必须变 false（首启判据不能被同一次运行里问两遍就翻转成 true）。
	if _, createdAgain, err := resolveConfigPath(true); err != nil || createdAgain {
		t.Errorf("second resolveConfigPath(true) = (created %v, err %v), want (false, nil)", createdAgain, err)
	}
	if want := filepath.Join(home, ".yagent", "config", "work.toml"); gotPath != want {
		t.Errorf("getConfigPath() = %q, want %q", gotPath, want)
	}
}

// writeActivePreferenceRaw 把**原样字节**写进 ~/.yagent/active_profile。
//
// 不走 config.WriteActiveProfile：那个会在写盘前过文法校验，而这条文件专门要造
// "用户手改坏了的"偏好（正常路径写不进去）。这也正是 profile_resolve.go 必须
// 降级而不是报错的场景。
func writeActivePreferenceRaw(t *testing.T, home, content string) string {
	t.Helper()
	dir := filepath.Join(home, ".yagent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "active_profile")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestResolveProfileFallsBackToActiveProfile 钉住第 4 级（~/.yagent/active_profile）
// 以及它与前三级的关系。
//
// 概览里的优先级是 `--config > --profile > $YAGENT_PROFILE > active_profile > default`，
// 而前三级由 flag/env 决定、只有根包看得见 —— 所以这一级只能在这里验证。
func TestResolveProfileFallsBackToActiveProfile(t *testing.T) {
	t.Run("preference is used when flag and env are empty", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "")
		if err := config.WriteActiveProfile(config.HomeDir(), "work"); err != nil {
			t.Fatalf("seed preference: %v", err)
		}

		got, err := resolveProfileName()
		if err != nil {
			t.Fatalf("resolveProfileName() unexpected error: %v", err)
		}
		if got != "work" {
			t.Errorf("resolveProfileName() = %q, want work", got)
		}

		path, created, err := resolveConfigPath(false)
		if err != nil {
			t.Fatalf("resolveConfigPath(false) unexpected error: %v", err)
		}
		if want := filepath.Join(home, ".yagent", "config", "work.toml"); path != want {
			t.Errorf("resolveConfigPath(false) = %q, want %q", path, want)
		}
		if created {
			t.Error("created = true with createIfMissing=false, want false")
		}
	})

	t.Run("env beats the preference", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "")
		if err := config.WriteActiveProfile(config.HomeDir(), "work"); err != nil {
			t.Fatalf("seed preference: %v", err)
		}
		t.Setenv(profileEnvVar, "infra")

		got, err := resolveProfileName()
		if err != nil {
			t.Fatalf("resolveProfileName() unexpected error: %v", err)
		}
		if got != "infra" {
			t.Errorf("resolveProfileName() = %q, want infra (env wins over the preference)", got)
		}
		if path, _, err := resolveConfigPath(false); err != nil {
			t.Fatalf("resolveConfigPath(false) unexpected error: %v", err)
		} else if want := filepath.Join(home, ".yagent", "config", "infra.toml"); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
	})

	t.Run("flag beats everything but --config", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "lab")
		if err := config.WriteActiveProfile(config.HomeDir(), "work"); err != nil {
			t.Fatalf("seed preference: %v", err)
		}
		t.Setenv(profileEnvVar, "infra")

		got, err := resolveProfileName()
		if err != nil {
			t.Fatalf("resolveProfileName() unexpected error: %v", err)
		}
		if got != "lab" {
			t.Errorf("resolveProfileName() = %q, want lab (flag wins)", got)
		}
		if path, _, err := resolveConfigPath(false); err != nil {
			t.Fatalf("resolveConfigPath(false) unexpected error: %v", err)
		} else if want := filepath.Join(home, ".yagent", "config", "lab.toml"); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
	})

	t.Run("config flag ignores the preference entirely", func(t *testing.T) {
		explicit := filepath.Join(t.TempDir(), "explicit.toml")
		isolateConfigEnv(t, explicit, "")
		if err := config.WriteActiveProfile(config.HomeDir(), "work"); err != nil {
			t.Fatalf("seed preference: %v", err)
		}

		var path string
		var err error
		// 偏好文件存在也不该被读：--config 命中时连文件都不碰，
		// 所以一份写坏的偏好不可能让 --config 起不来。这里的 stderr 只有 --profile 警告，
		// 没有警告时为空 —— 不因此断言 stderr。
		stderr := captureStderr(t, func() {
			path, _, err = resolveConfigPath(true)
		})
		if err != nil {
			t.Fatalf("resolveConfigPath(true) with --config unexpected error: %v", err)
		}
		if path != explicit {
			t.Errorf("path = %q, want verbatim --config path %q", path, explicit)
		}
		if strings.Contains(stderr, "active_profile") {
			t.Errorf("stderr = %q, want nothing about the preference (it must not be read)", stderr)
		}
	})

	// 用户没在命令行写错任何东西：一份手改坏的偏好不该把启动卡住，
	// 也不该让退出码变成 2（那是"命令行写错了"的语义）。
	t.Run("unusable preference degrades to default without error", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "")
		writeActivePreferenceRaw(t, home, "garbage/../x\n")

		var got string
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("resolveProfileName panicked on an unusable preference: %v", r)
				}
			}()
			got, err = resolveProfileName()
		}()

		if err != nil {
			t.Fatalf("resolveProfileName() = (%q, %v), want the default with no error", got, err)
		}
		if got != config.DefaultProfileName {
			t.Errorf("resolveProfileName() = %q, want %q", got, config.DefaultProfileName)
		}
		var ue *usageError
		if errors.As(err, &ue) {
			t.Error("error is a usage error (exit 2), want a silent fallback")
		}

		path, _, pathErr := resolveConfigPath(false)
		if pathErr != nil {
			t.Fatalf("resolveConfigPath(false) unexpected error: %v", pathErr)
		}
		if want := filepath.Join(home, ".yagent", "config", "config.toml"); path != want {
			t.Errorf("path = %q, want the default config path %q", path, want)
		}
	})

	// 向前兼容基线：老用户升级后根本没有这份文件。
	t.Run("missing preference is the default baseline", func(t *testing.T) {
		home := isolateConfigEnv(t, "", "")

		got, err := resolveProfileName()
		if err != nil {
			t.Fatalf("resolveProfileName() unexpected error: %v", err)
		}
		if got != config.DefaultProfileName {
			t.Errorf("resolveProfileName() = %q, want %q", got, config.DefaultProfileName)
		}
		// 缺失不算"读写失败"：连 ~/.yagent 都不该被创建出来。
		if _, statErr := os.Stat(filepath.Join(home, ".yagent")); !os.IsNotExist(statErr) {
			t.Errorf("%s was created by a read-only resolve (stat err = %v)",
				filepath.Join(home, ".yagent"), statErr)
		}
	})
}
