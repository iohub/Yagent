package config

// active_profile 的闸门：路径推导、三态读回、写盘（含 default 删文件）、幂等清理。
//
// 这个文件存在的理由：整条 profile 链最要紧的一句是"文件不存在 == 没有偏好 == default"。
// 它没有类型能表达（没有 Optional 包装），只能靠测试钉住 —— 一旦有人把缺失改成错误，
// 老用户升级后在全新机器上会直接启动失败，而所有既有测试都是绿的。
//
// 全部用例都靠 configHome 参数或 t.Setenv("HOME") 注入，绝不碰真实 ~/.yagent。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// activeProfileTestHome 造一个受控的 home 并接管 $HOME，返回 home 与默认落点目录。
//
// 只接管 $HOME（active_profile.go 里唯一的进程级输入就是它）：HomeDir / ConfigDir
// 都从 os.UserHomeDir 推导，所以置空变量即可把整条路径挪进临时目录。
func activeProfileTestHome(t *testing.T) (home, configHome string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	configHome = filepath.Join(home, ".yagent")
	if got := HomeDir(); got != configHome {
		t.Fatalf("HomeDir() = %q, want %q (t.Setenv(\"HOME\") 没有生效，后续断言全不可信)", got, configHome)
	}
	return home, configHome
}

// TestActiveProfileHomeDirFollowsHome 钉住"偏好文件住在 ~/.yagent 顶层，不是 config/ 子目录"。
//
// 住在 config/ 的后果不是报错，而是被 ProfileNamesInDir 当成一个可选档列出来
// （active_profile.go 头注里点名了这个理由）。
func TestActiveProfileHomeDirFollowsHome(t *testing.T) {
	home, configHome := activeProfileTestHome(t)

	if got, want := HomeDir(), filepath.Join(home, ".yagent"); got != want {
		t.Errorf("HomeDir() = %q, want %q", got, want)
	}
	if got, want := ActiveProfilePath(""), filepath.Join(HomeDir(), "active_profile"); got != want {
		t.Errorf("ActiveProfilePath(\"\") = %q, want %q", got, want)
	}
	if got, want := ActiveProfilePath(""), filepath.Join(home, ".yagent", "active_profile"); got != want {
		t.Errorf("ActiveProfilePath(\"\") = %q, want %q", got, want)
	}
	// 不许落在 config/ 子目录里：那里是档文件（*.toml）的扫描域。
	if dir := filepath.Dir(ActiveProfilePath("")); dir != configHome {
		t.Errorf("active_profile 落点目录 = %q, want %q", dir, configHome)
	}
}

// TestActiveProfilePathUsesInjectedConfigHome configHome 非空时必须原样使用 ——
// 这是测试与沙箱唯一的注入口，若被忽略，所有用例会悄悄写进真实 HOME。
func TestActiveProfilePathUsesInjectedConfigHome(t *testing.T) {
	// 刻意不用 t.TempDir()：下面断言的是"目录不存在时也照拼路径"，
	// ActiveProfilePath 不负责创建目录。
	const injected = "/tmp/yagent-profile-injected"
	if got, want := ActiveProfilePath(injected), filepath.Join(injected, "active_profile"); got != want {
		t.Errorf("ActiveProfilePath(%q) = %q, want %q", injected, got, want)
	}
	// 纯空白等同于没传：否则 `ActiveProfilePath(" ")` 会拼出 " /active_profile"。
	activeProfileTestHome(t)
	if got, want := ActiveProfilePath("   "), filepath.Join(HomeDir(), "active_profile"); got != want {
		t.Errorf("ActiveProfilePath(\"   \") = %q, want %q", got, want)
	}
}

// TestReadActiveProfileFallbackStates 钉住"读不到偏好是常态"这一组结果：
// 缺失 / 0 字节 / 纯空白 / 正常 / 带空格，全部返回 ("", nil) 或 trim 后的名字。
func TestReadActiveProfileFallbackStates(t *testing.T) {
	cases := []struct {
		name    string
		write   bool
		content string
		want    string
	}{
		{name: "missing file means no preference", write: false, want: ""},
		{name: "empty file means no preference", write: true, content: "", want: ""},
		{name: "whitespace only means no preference", write: true, content: "  \n\n ", want: ""},
		{name: "plain name", write: true, content: "work\n", want: "work"},
		{name: "surrounding spaces are trimmed", write: true, content: "  work  \n", want: "work"},
		{name: "no trailing newline still parses", write: true, content: "infra", want: "infra"},
		{name: "hyphen and underscore are legal", write: true, content: "team-2_x\n", want: "team-2_x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, configHome := activeProfileTestHome(t)
			if c.write {
				if err := os.MkdirAll(configHome, 0o755); err != nil {
					t.Fatalf("mkdir configHome: %v", err)
				}
				if err := os.WriteFile(ActiveProfilePath(configHome), []byte(c.content), 0o600); err != nil {
					t.Fatalf("seed active_profile: %v", err)
				}
			}

			got, err := ReadActiveProfile(configHome)
			// 缺失必须是 (\"\", nil) 而不是错误：老用户升级后一个文件都没有，
			// 那是向前兼容的主路径，不是异常。
			if err != nil {
				t.Fatalf("ReadActiveProfile() unexpected error: %v（缺失/空白都不是错误）", err)
			}
			if got != c.want {
				t.Errorf("ReadActiveProfile() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestReadActiveProfileRejectsUnusableNames 内容过不了文法时必须返回错误：
// 调用方（根包与 TUI）要靠这个错误决定"降级 default + warn"，静默返回空串
// 会让坏内容看起来像"没有偏好"，两者在日志里无法区分。
func TestReadActiveProfileRejectsUnusableNames(t *testing.T) {
	names := []string{
		"garbage/../x",
		"..",
		"a b",
		"-x",
		".hidden",
		"work.toml",             // 点号不许
		strings.Repeat("a", 65), // 上限 64
		"a/b",                   // 分隔符不许
		// 刻意不放 "config "：ReadActiveProfile 会先 TrimSpace，那一条在下面单独钉住，
		// 混进这张表只会让"trim 生效"与"拒绝非法名"两件事互相矛盾。
	}

	for _, name := range names {
		// 子测试名要扁平：名字里的 "/" 会被 go test 当成层级分隔，-run 就定位不到。
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			_, configHome := activeProfileTestHome(t)
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				t.Fatalf("mkdir configHome: %v", err)
			}
			if err := os.WriteFile(ActiveProfilePath(configHome), []byte(name+"\n"), 0o600); err != nil {
				t.Fatalf("seed active_profile: %v", err)
			}

			got, err := ReadActiveProfile(configHome)
			if err == nil {
				t.Fatalf("ReadActiveProfile() = (%q, nil), want an error for %q", got, name)
			}
			if got != "" {
				t.Errorf("ReadActiveProfile() = %q on error, want the empty string (no half-trusted name)", got)
			}
			// 错误必须点名文件路径：用户手改坏了这份文本，得知道改的是哪个文件。
			if !strings.Contains(err.Error(), ActiveProfilePath(configHome)) {
				t.Errorf("error = %q, want it to name %q", err, ActiveProfilePath(configHome))
			}
		})
	}

	// 对照：`config ` 只因为多了个空格才过不了文法，trim 之后是合法名字 ——
	// 这一条防止有人"顺手"去掉 TrimSpace 而没人发现。
	t.Run("config with trailing space is rejected only because of the space", func(t *testing.T) {
		if err := ValidateProfileName("config"); err != nil {
			t.Fatalf("ValidateProfileName(\"config\") = %v, want nil (文法层放行，路径层才拒绝)", err)
		}
		if err := ValidateProfileName("config "); err == nil {
			t.Error("ValidateProfileName(\"config \") = nil, want rejection (尾随空格不在文法内)")
		}
	})
}

// TestWriteActiveProfileRoundTrip 覆盖写盘的四条事实：内容带换行、权限精确 0600、
// 目录自动创建、以及与 ReadActiveProfile 的往返一致。
func TestWriteActiveProfileRoundTrip(t *testing.T) {
	_, configHome := activeProfileTestHome(t)

	path := ActiveProfilePath(configHome)
	if err := WriteActiveProfile(configHome, "work"); err != nil {
		t.Fatalf("WriteActiveProfile(work) unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read active_profile: %v", err)
	}
	// 内容恒为 "名字\n"：一行纯文本，cat / echo 都能改。
	if string(data) != "work\n" {
		t.Errorf("active_profile content = %q, want %q", data, "work\n")
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat active_profile: %v", err)
	}
	// 精确 0600：util.AtomicWrite 显式 chmod 临时文件后再 rename，不受 umask 影响。
	// 与 config.toml 同口径 —— 偏好文件本身不含密钥，但它与配置文件同源。
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("active_profile mode = %o, want 600", perm)
	}

	got, err := ReadActiveProfile(configHome)
	if err != nil {
		t.Fatalf("ReadActiveProfile() after write unexpected error: %v", err)
	}
	if got != "work" {
		t.Errorf("round trip = %q, want work", got)
	}

	// 名字前后的空格在写入前就被 trim：否则会造出一个带前导空格的偏好，
	// 而下次 `profile use work` 读到的却是另一个名字。
	if err := WriteActiveProfile(configHome, "  infra  "); err != nil {
		t.Fatalf("WriteActiveProfile(\"  infra  \") unexpected error: %v", err)
	}
	got, err = ReadActiveProfile(configHome)
	if err != nil {
		t.Fatalf("ReadActiveProfile() after spaced write unexpected error: %v", err)
	}
	if got != "infra" {
		t.Errorf("trimmed round trip = %q, want infra", got)
	}
}

// TestWriteActiveProfileDefaultDeletesFile 钉住"选中 default 不是写一行 default，而是删文件"。
//
// 两种状态（文件缺失 / 文件内容为 default）在语义上等价，同时存在就一定会漂移：
// 用户手改、备份恢复、脚本 echo 都会让其中一份变成过期事实。
func TestWriteActiveProfileDefaultDeletesFile(t *testing.T) {
	_, configHome := activeProfileTestHome(t)
	path := ActiveProfilePath(configHome)

	if err := WriteActiveProfile(configHome, "work"); err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("seed preference file missing: %v", err)
	}

	if err := WriteActiveProfile(configHome, " default "); err != nil {
		t.Fatalf("WriteActiveProfile(default) unexpected error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("active_profile still exists after selecting default (stat err = %v)", err)
	}

	got, err := ReadActiveProfile(configHome)
	if err != nil {
		t.Fatalf("ReadActiveProfile() after default unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("ReadActiveProfile() = %q after default, want the empty string", got)
	}

	// 全新机器上再选一次 default：仍然成功（幂等）。
	if err := WriteActiveProfile(configHome, DefaultProfileName); err != nil {
		t.Errorf("WriteActiveProfile(default) on a missing file unexpected error: %v", err)
	}
}

// TestWriteActiveProfileRejectsInvalidNameWithoutSideEffects 非法名字必须在**写盘之前**被拒：
// 拒绝之后目录里不许出现任何文件（尤其是 "../evil" 这类穿越名）。
func TestWriteActiveProfileRejectsInvalidNameWithoutSideEffects(t *testing.T) {
	names := []string{
		"infra/../evil",
		"../evil",
		"",
		"a b",
		".hidden",
		"config.toml",
		strings.Repeat("a", 65),
	}

	for _, name := range names {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			_, configHome := activeProfileTestHome(t)
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				t.Fatalf("mkdir configHome: %v", err)
			}

			if err := WriteActiveProfile(configHome, name); err == nil {
				t.Fatalf("WriteActiveProfile(%q) = nil, want an error", name)
			}

			entries, err := os.ReadDir(configHome)
			if err != nil {
				t.Fatalf("read configHome: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("rejected write left %d entries in %s, want none", len(entries), configHome)
			}
			// 穿越名绝不能落到 home 上层去。
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(configHome), "evil")); !os.IsNotExist(statErr) {
				t.Errorf("traversal name created %s (stat err = %v)", filepath.Join(filepath.Dir(configHome), "evil"), statErr)
			}
		})
	}
}

// TestWriteActiveProfileCreatesMissingDir 目录不存在时必须自己建出来：
// `profile use work` 在全新机器上（连 ~/.yagent 都没有）必须能跑通。
func TestWriteActiveProfileCreatesMissingDir(t *testing.T) {
	_, configHome := activeProfileTestHome(t)

	fresh := filepath.Join(configHome, "nested", "deeper")
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("fixture broken: %s already exists (stat err = %v)", fresh, err)
	}

	if err := WriteActiveProfile(fresh, "work"); err != nil {
		t.Fatalf("WriteActiveProfile on a missing dir unexpected error: %v", err)
	}
	if st, err := os.Stat(fresh); err != nil || !st.IsDir() {
		t.Fatalf("dir %s was not created (stat err = %v)", fresh, err)
	}

	got, err := ReadActiveProfile(fresh)
	if err != nil {
		t.Fatalf("ReadActiveProfile() unexpected error: %v", err)
	}
	if got != "work" {
		t.Errorf("round trip = %q, want work", got)
	}
}

// TestClearActiveProfileIsIdempotent 清理必须幂等：`profile use default` 与 TUI 切回 default
// 都会走到它，而全新机器上没有文件可删 —— 那不是错误。
func TestClearActiveProfileIsIdempotent(t *testing.T) {
	_, configHome := activeProfileTestHome(t)
	path := ActiveProfilePath(configHome)

	// 1) 文件不存在：不报错。
	if err := ClearActiveProfile(configHome); err != nil {
		t.Fatalf("ClearActiveProfile on missing file unexpected error: %v", err)
	}

	// 2) 文件存在：删掉。
	if err := WriteActiveProfile(configHome, "work"); err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	if err := ClearActiveProfile(configHome); err != nil {
		t.Fatalf("ClearActiveProfile unexpected error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("active_profile still exists after clear (stat err = %v)", err)
	}

	// 3) 连续两次：都返回 nil。
	if err := ClearActiveProfile(configHome); err != nil {
		t.Errorf("second ClearActiveProfile unexpected error: %v", err)
	}
}
