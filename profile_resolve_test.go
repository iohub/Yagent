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
