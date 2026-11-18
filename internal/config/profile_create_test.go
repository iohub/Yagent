package config

// CreateProfile 的闸门：来源三档、错误分类、以及"绝不覆盖 / 绝不自拷 / 绝不越界"三条硬规则。
//
// 这些用例同时是 CLI 与 TUI 两个入口的共同契约 —— 两个入口都在这一步之后只做文案，
// 所以规则一旦退化成"其中一种来源悄悄变了"，两边会一起错，而肉眼看输出并不明显。
// 全部用例只用 t.TempDir()，不碰 ~/.yagent。

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// profileCreatePath 把 (目录, 档名) 换成期望路径。刻意不复用被测代码的返回值：
// "写到了哪个文件"要和 ProfileConfigPath 的规则独立对上才算数。
func profileCreatePath(t *testing.T, dir, name string) string {
	t.Helper()
	path, err := ProfileConfigPath(dir, name)
	if err != nil {
		t.Fatalf("ProfileConfigPath(%q, %q) unexpected error: %v", dir, name, err)
	}
	return path
}

// seedProfileFile 在 <dir>/<name>.toml 落一份内容确定的档，供"复制来源"用。
func seedProfileFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := profileCreatePath(t, dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// templateBytes 用同一台生成器产出一份内嵌模板作为参照物。
// 口径是"内容等于模板"，不是"非空"—— 后者在模板被换成空文件时照样通过。
func templateBytes(t *testing.T) []byte {
	t.Helper()
	ref := filepath.Join(t.TempDir(), "ref.toml")
	if _, err := EnsureConfigExists(ref); err != nil {
		t.Fatalf("EnsureConfigExists(ref) unexpected error: %v", err)
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		t.Fatalf("read template reference: %v", err)
	}
	return data
}

// TestCreateProfileCopiesDefault 没有 from 时 default 是隐式来源：
// 复制必须**逐字节**一致（一次 TOML 编解码就会蒸发注释），权限 0600，目录自动创建。
func TestCreateProfileCopiesDefault(t *testing.T) {
	// 目录刻意不存在：create 要能沿路建出来（全新机器第一次 create 就是这样）。
	dir := filepath.Join(t.TempDir(), "nested", "config")
	content := "# default\nuse_provider = \"p\"\n"
	defaultPath := seedProfileFile(t, dir, DefaultProfileName, content)

	path, source, err := CreateProfile(dir, "work", "")
	if err != nil {
		t.Fatalf("CreateProfile(work) unexpected error: %v", err)
	}
	if want := profileCreatePath(t, dir, "work"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if source != DefaultProfileName {
		t.Errorf("source = %q, want %q", source, DefaultProfileName)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created profile: %v", err)
	}
	if !bytes.Equal(got, []byte(content)) {
		t.Errorf("created profile = %q, want the bytes of default %q", got, content)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created profile: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("created profile mode = %o, want 600", perm)
	}

	// 来源原样保留：create 是只读源 + 新建目标。
	if data, err := os.ReadFile(defaultPath); err != nil || string(data) != content {
		t.Errorf("default profile changed: %q (err = %v)", data, err)
	}
}

// TestCreateProfileWithoutFromFallsBackToTemplate 全新机器（连 default 都没有）：
// 走内嵌模板，且**不**顺手生成 default 的 config.toml —— create 不是半个 setup。
func TestCreateProfileWithoutFromFallsBackToTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")

	path, source, err := CreateProfile(dir, "work", "")
	if err != nil {
		t.Fatalf("CreateProfile(work) unexpected error: %v", err)
	}
	if source != "" {
		t.Errorf("source = %q, want the empty string (built-in template)", source)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created profile: %v", err)
	}
	if !bytes.Equal(got, templateBytes(t)) {
		t.Errorf("created profile (%d bytes) does not match the embedded template", len(got))
	}

	if _, err := os.Stat(profileCreatePath(t, dir, DefaultProfileName)); !os.IsNotExist(err) {
		t.Errorf("creating a named profile also created config.toml (stat err = %v), want it untouched", err)
	}
}

// TestCreateProfileFromDefaultWhenDefaultMissing 显式 `from=default` 在全新机器上
// 不是"源不存在"：default 是内置档，文件还没生成时回落模板，source 因此是空串。
func TestCreateProfileFromDefaultWhenDefaultMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")

	path, source, err := CreateProfile(dir, "copy1", DefaultProfileName)
	if err != nil {
		t.Fatalf("CreateProfile(copy1, from=default) unexpected error: %v", err)
	}
	if source != "" {
		t.Errorf("source = %q, want the empty string (template fallback)", source)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created profile: %v", err)
	}
	if !bytes.Equal(got, templateBytes(t)) {
		t.Errorf("created profile (%d bytes) does not match the embedded template", len(got))
	}
	if _, err := os.Stat(profileCreatePath(t, dir, DefaultProfileName)); !os.IsNotExist(err) {
		t.Errorf("--from default created config.toml (stat err = %v), want it untouched", err)
	}
}

// TestCreateProfileCopiesNamedSource 显式来源必须是它指向的那份文件，
// 而不是 default —— 内容判据用一份刻意不同的字节。
func TestCreateProfileCopiesNamedSource(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	seedProfileFile(t, dir, DefaultProfileName, "# default\n")
	seedProfileFile(t, dir, "infra", "# from infra\n")

	path, source, err := CreateProfile(dir, "work", "infra")
	if err != nil {
		t.Fatalf("CreateProfile(work, from=infra) unexpected error: %v", err)
	}
	if source != "infra" {
		t.Errorf("source = %q, want infra", source)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created profile: %v", err)
	}
	if string(got) != "# from infra\n" {
		t.Errorf("created profile = %q, want the bytes of infra", got)
	}
}

// TestCreateProfileRejectsExistingTarget 目标已存在时连一个字节都不许改写。
//
// 判据刻意用 errors.Is(ErrProfileExists)：CLI 靠它给退出码 1，TUI 靠它说"已存在"。
func TestCreateProfileRejectsExistingTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	workPath := seedProfileFile(t, dir, "work", "# keep me\n")

	_, _, err := CreateProfile(dir, "work", "")
	if err == nil {
		t.Fatal("CreateProfile(work) = nil, want an error: work already exists")
	}
	if !errors.Is(err, ErrProfileExists) {
		t.Errorf("error = %v, want errors.Is(ErrProfileExists)", err)
	}
	var usage *ProfileUsageError
	if errors.As(err, &usage) {
		t.Errorf("error = %v classified as a usage error, want a plain failure (已存在不是命令行写错)", err)
	}

	data, readErr := os.ReadFile(workPath)
	if readErr != nil {
		t.Fatalf("read existing profile: %v", readErr)
	}
	if string(data) != "# keep me\n" {
		t.Errorf("existing profile was rewritten: %q, want it untouched", data)
	}
}

// TestCreateProfileRejectsMissingNamedSource 命名来源不存在是操作失败（不是用法错），
// 且失败之后磁盘上不能多出任何东西。
func TestCreateProfileRejectsMissingNamedSource(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")

	_, _, err := CreateProfile(dir, "copy2", "ghost")
	if err == nil {
		t.Fatal("CreateProfile(copy2, from=ghost) = nil, want an error")
	}
	if !errors.Is(err, ErrProfileSourceMissing) {
		t.Errorf("error = %v, want errors.Is(ErrProfileSourceMissing)", err)
	}
	var usage *ProfileUsageError
	if errors.As(err, &usage) {
		t.Errorf("error = %v classified as a usage error, want a plain failure (缺文件不是命令行写错)", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		// dir 里若存在，必须是空目录（没有任何 .toml 被建出来）。
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatalf("read config dir: %v", readErr)
		}
		if len(entries) != 0 {
			t.Errorf("a failed create left %d entries in %s, want none", len(entries), dir)
		}
	}
}

// TestCreateProfileRejectsInvalidNames 非法名一律归为用法错误，且目录零新增。
//
// 名字层与路径层两条闸门都要走一遍：`../evil` 死在文法，`config` 死在路径
// （它映射到 config.toml，也就是 default 自己的文件）。
func TestCreateProfileRejectsInvalidNames(t *testing.T) {
	for _, name := range []string{"", " ", "../evil", "a/b", ".hidden", "config"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "config")

			_, _, err := CreateProfile(dir, name, "")
			if err == nil {
				t.Fatalf("CreateProfile(%q) = nil, want an error", name)
			}
			var usage *ProfileUsageError
			if !errors.As(err, &usage) {
				t.Errorf("CreateProfile(%q) error = %v, want *ProfileUsageError (改命令行就能修好)", name, err)
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				entries, readErr := os.ReadDir(dir)
				if readErr != nil {
					t.Fatalf("read config dir: %v", readErr)
				}
				if len(entries) != 0 {
					t.Errorf("rejected name %q created %d entries, want none", name, len(entries))
				}
			}
		})
	}
}

// TestCreateProfileRejectsSelfCopy 同档自拷不静默成功：它只会让人以为"复制了一份备份"，
// 而实际上什么都没发生。只对**显式** from 生效 —— from 为空时的隐式来源是 default，
// `create default` 在全新机器上必须仍然是"把模板落成 config.toml"。
func TestCreateProfileRejectsSelfCopy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")

	_, _, err := CreateProfile(dir, "work", "work")
	if err == nil {
		t.Fatal("CreateProfile(work, from=work) = nil, want a self-copy error")
	}
	var usage *ProfileUsageError
	if !errors.As(err, &usage) {
		t.Errorf("error = %v, want *ProfileUsageError", err)
	}
	if !strings.Contains(err.Error(), "being created") {
		t.Errorf("error = %v, want it to say the source is the target", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatalf("read config dir: %v", readErr)
		}
		if len(entries) != 0 {
			t.Errorf("a rejected self-copy created %d entries, want none", len(entries))
		}
	}

	// 判定顺序也是契约：目标已存在时先报"已存在"（操作失败），而不是"同档自拷"（用法错）。
	seedProfileFile(t, dir, "taken", "# taken\n")
	if _, _, err := CreateProfile(dir, "taken", "taken"); !errors.Is(err, ErrProfileExists) {
		t.Errorf("CreateProfile(taken, from=taken) error = %v, want ErrProfileExists (已存在优先于自拷)", err)
	}

	// 显式 default→default 在全新机器上同样是自拷（不是"抄模板"）。
	freshDir := filepath.Join(t.TempDir(), "fresh")
	if _, _, err := CreateProfile(freshDir, DefaultProfileName, DefaultProfileName); err == nil {
		t.Fatal("CreateProfile(default, from=default) = nil, want a self-copy error on a fresh dir")
	}

	// 隐式来源（from 为空）不受影响：`create default` 仍是"用模板生成 config.toml"。
	path, source, err := CreateProfile(freshDir, DefaultProfileName, "")
	if err != nil {
		t.Fatalf("CreateProfile(default) unexpected error: %v", err)
	}
	if source != "" {
		t.Errorf("source = %q, want the empty string (template)", source)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("CreateProfile(default) did not create %s: %v", path, err)
	}
}
