package tui

// `:profile` / `/profile` 的闸门：面板弹出、直接切换、以及最要紧的那条铁律 ——
// **切换只写一个偏好文件，本次进程的任何状态都不许动**。
//
// 为什么这条铁律值得单独一个用例：profile 切换最容易被"顺手优化"成热切换
// （改 m.com.Config、改 client.ConfigPath、重建 app）。而那一步会立刻踩到
// app.Init 的 sync.Once、supervise.Root() 的进程级单例、以及事件通道没有重放队列
// 这三件既成事实 —— 症状不是报错，是若干个 Agent 拿着旧配置继续跑。
//
// 全部用例靠 t.Setenv("HOME") 隔离，绝不写真实 ~/.yagent。

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/app"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/tui/common"
	"yagent/internal/tui/components"
)

// tuiProfileHome 接管 $HOME 并返回临时 home。
//
// 必须显式清掉 YAGENT_PROFILE：TUI 的 activeProfileName() 不读环境变量，
// 但同一进程里跑着的其它用例（根包的 isolateConfigEnv 已清理）之外，
// 开发机上真实导出的值会穿过子进程边界 —— 清掉它让这条文件只依赖注入的 HOME。
func tuiProfileHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YAGENT_PROFILE", "")
	want := filepath.Join(home, ".yagent")
	if got := config.HomeDir(); got != want {
		t.Fatalf("config.HomeDir() = %q, want %q (HOME 注入失效，后续断言不可信)", got, want)
	}
	return home
}

// tuiActiveProfilePath 返回本次用例的偏好文件路径（$HOME/.yagent/active_profile）。
func tuiActiveProfilePath(t *testing.T, home string) string {
	t.Helper()
	return config.ActiveProfilePath(filepath.Join(home, ".yagent"))
}

// seedProfileToml 在 <configDir>/<name>.toml 造一份档文件，返回其路径。
//
// 内容不重要（applyProfileSelection 只看 os.Stat），刻意写成一行注释而不是完整
// TOML：这条用例不该因为模板改动而变红。
func seedProfileToml(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, ".yagent", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	path, err := config.ProfileConfigPath(dir, name)
	if err != nil {
		t.Fatalf("ProfileConfigPath(%q): %v", name, err)
	}
	if err := os.WriteFile(path, []byte("# profile "+name+"\n"), 0o600); err != nil {
		t.Fatalf("write profile %q: %v", name, err)
	}
	return path
}

// readActiveProfilePreference 读回偏好文件内容，缺失时返回空串（缺失是常态）。
func readActiveProfilePreference(t *testing.T, home string) string {
	t.Helper()
	got, err := config.ReadActiveProfile(filepath.Join(home, ".yagent"))
	if err != nil {
		t.Fatalf("ReadActiveProfile unexpected error: %v", err)
	}
	return got
}

// yagentTreeSnapshot 记录 $HOME/.yagent 下每个文件的"权限位|内容"。
//
// 用来证明 applyProfileSelection 只碰了一个文件：逐文件比对而不是只数个数，
// 因为"改了 config.toml 但文件数没变"正是最危险的退化。
func yagentTreeSnapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	root := filepath.Join(home, ".yagent")
	snap := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 目录根本不存在（全新机器）：空快照，不是错误。
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		st, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		snap[rel] = fmt.Sprintf("%o|%s", st.Mode().Perm(), data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

// TestProfileCommandOpensDialog 无参命令弹面板：default 行必须在，且文件没生成时
// 带"未创建"标注（否则用户会以为 default 不存在，而首启又会凭空造出一份）。
//
// 两种界面语言各跑一遍，并各自断言**完整**标注：标注自成一行、不再与路径共用可省略
// 单元格（9d2e33f 修的正是这一点），所以"完整"是当前实现的硬事实 —— 断言里出现省略号
// 就说明实现退回了与路径同格。只断言尾部片段（如 "yagent setup"）等于拆掉闸门：中间
// 省略**保留首尾**，缺陷形态下尾部照样在，正是这条断言原先失效的原因。
// 顺带钉住语言透传：profileCommand 用 components.Language(m.currentLang) 建面板，
// 面板语言在创建时快照，所以标题同样按语言精确断言。
func TestProfileCommandOpensDialog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lang  Language
		note  string
		title string
	}{
		{name: "chinese", lang: LangChinese, note: "尚未创建 — 运行 yagent setup", title: "选择配置档"},
		{name: "english", lang: LangEnglish, note: "not created yet — run yagent setup", title: "Select Config Profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tuiProfileHome(t)
			m := chromeTestModel(t, 100, 40)
			m.currentLang = tc.lang

			if cmd := m.profileCommand(nil); cmd != nil {
				t.Fatalf("profileCommand(nil) = %v, want nil（它只弹窗，不发起异步动作）", cmd)
			}
			if m.dialogStack.Len() != 1 {
				t.Fatalf("dialogStack.Len() = %d, want 1", m.dialogStack.Len())
			}
			d, ok := m.dialogStack.Top().(*components.ProfileSelectDialog)
			if !ok {
				t.Fatalf("栈顶 = %T, want *components.ProfileSelectDialog", m.dialogStack.Top())
			}
			// 入栈前必须 SetBounds：漏设时 View() 会因 width<40 返回空串（见组件测试）。
			if w, h := d.Bounds(); w != m.termWidth || h != m.termHeight {
				t.Errorf("bounds = (%d,%d), want (%d,%d)", w, h, m.termWidth, m.termHeight)
			}

			out := stripANSI(d.View())
			if !strings.Contains(out, config.DefaultProfileName) {
				t.Errorf("面板没有 default 行：\n%s", out)
			}
			if !strings.Contains(out, tc.note) {
				t.Errorf("全新 home 下 default 行应带完整的未创建标注 %q：\n%s", tc.note, out)
			}
			if !strings.Contains(out, tc.title) {
				t.Errorf("界面语言 %q 下标题应为 %q：\n%s", tc.lang, tc.title, out)
			}
		})
	}
}

// TestProfileCommandWiringFromBothPrefixes 斜杠与冒号两条命令必须都进同一个入口。
//
// 补全列表与分发点是两份注册表（历史坑），漏掉斜杠分支时 `/profile` 会退化成
// "搜索字符串 profile"，界面上什么都不发生。
func TestProfileCommandWiringFromBothPrefixes(t *testing.T) {
	home := tuiProfileHome(t)

	m := chromeTestModel(t, 100, 40)
	if cmd := m.processCommand("/profile"); cmd != nil {
		t.Errorf("/profile 返回了 cmd %v, want nil", cmd)
	}
	if m.dialogStack.Len() != 1 {
		t.Fatalf("/profile 没有打开面板（%d 层）", m.dialogStack.Len())
	}
	m.dialogStack.Pop()

	if cmd := m.processCommand(":profile"); cmd != nil {
		t.Errorf(":profile 返回了 cmd %v, want nil", cmd)
	}
	if m.dialogStack.Len() != 1 {
		t.Fatalf(":profile 没有打开面板（%d 层）", m.dialogStack.Len())
	}
	m.dialogStack.Pop()

	// 带参数的斜杠形态直接切换，不弹面板。
	seedProfileToml(t, home, "work")
	if cmd := m.processCommand("/profile work"); cmd != nil {
		t.Errorf("/profile work 返回了 cmd %v, want nil", cmd)
	}
	if m.dialogStack.Len() != 0 {
		t.Errorf("/profile work 不该弹面板，却有 %d 层", m.dialogStack.Len())
	}
	if got := readActiveProfilePreference(t, home); got != "work" {
		t.Errorf("active_profile = %q, want work", got)
	}
}

// TestProfileCommandSwitchesToExistingProfile 有参直接切：落盘 + 明确说清生效时机。
//
// 只说"已切换"会让人以为配置已经换了 —— 接下来几十轮对话都在用旧 provider。
func TestProfileCommandSwitchesToExistingProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lang    Language
		restart string
	}{
		{name: "chinese", lang: LangChinese, restart: "重启"},
		{name: "english", lang: LangEnglish, restart: "restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := tuiProfileHome(t)
			seedProfileToml(t, home, "work")

			m := chromeTestModel(t, 100, 40)
			m.currentLang = tc.lang
			m.profileCommand([]string{"work"})

			if got := readActiveProfilePreference(t, home); got != "work" {
				t.Errorf("active_profile = %q, want work", got)
			}
			if !strings.Contains(m.infoMsg, "work") {
				t.Errorf("infoMsg = %q, want it to name the selected profile", m.infoMsg)
			}
			if !strings.Contains(m.infoMsg, tc.restart) {
				t.Errorf("infoMsg = %q, want it to say the change applies after a %q", m.infoMsg, tc.restart)
			}
		})
	}
}

// TestProfileCommandRejectsMissingProfile 命名档必须已经存在：
// 允许选中一个空档会让下次启动凭空生成模板，而用户以为切到了自己的配置。
func TestProfileCommandRejectsMissingProfile(t *testing.T) {
	home := tuiProfileHome(t)
	m := chromeTestModel(t, 100, 40)
	m.currentLang = LangChinese

	m.profileCommand([]string{"nosuch"})

	if _, err := os.Stat(tuiActiveProfilePath(t, home)); !os.IsNotExist(err) {
		t.Fatalf("拒绝了不存在的档却写了偏好文件（stat err = %v）", err)
	}
	if !strings.Contains(m.infoMsg, "profile create") {
		t.Errorf("infoMsg = %q, want it to point at `yagent profile create`", m.infoMsg)
	}
	if m.dialogStack.Len() != 0 {
		t.Errorf("有参形态不该弹面板，却有 %d 层", m.dialogStack.Len())
	}
}

// TestProfileCommandRejectsInvalidName 非法名字在写盘之前就被拒，且提示里带上原样输入。
func TestProfileCommandRejectsInvalidName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lang  Language
		input string
		want  string
	}{
		{name: "traversal zh", lang: LangChinese, input: "../evil", want: "非法档名"},
		{name: "traversal en", lang: LangEnglish, input: "../evil", want: "Invalid profile name"},
		{name: "dotfile", lang: LangChinese, input: ".hidden", want: "非法档名"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := tuiProfileHome(t)
			m := chromeTestModel(t, 100, 40)
			m.currentLang = tc.lang

			m.profileCommand([]string{tc.input})

			if _, err := os.Stat(tuiActiveProfilePath(t, home)); !os.IsNotExist(err) {
				t.Fatalf("非法名 %q 却写了偏好文件（stat err = %v）", tc.input, err)
			}
			if !strings.Contains(m.infoMsg, tc.want) {
				t.Errorf("infoMsg = %q, want it to contain %q", m.infoMsg, tc.want)
			}
		})
	}
}

// TestProfileCommandDefaultClearsPreference "切回 default" = 删偏好文件。
//
// default 的 config.toml 不在磁盘上也必须成功：它是内置档，全新机器上那是常态，
// 而这条通道是任何错误配置的逃生出口。
func TestProfileCommandDefaultClearsPreference(t *testing.T) {
	home := tuiProfileHome(t)
	if err := config.WriteActiveProfile(filepath.Join(home, ".yagent"), "work"); err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	// 刻意不造 default 的 config.toml：内置档豁免文件存在性检查。
	m := chromeTestModel(t, 100, 40)
	m.currentLang = LangChinese

	m.profileCommand([]string{config.DefaultProfileName})

	if _, err := os.Stat(tuiActiveProfilePath(t, home)); !os.IsNotExist(err) {
		t.Fatalf("切回 default 之后偏好文件仍在（stat err = %v）", err)
	}
	if got := readActiveProfilePreference(t, home); got != "" {
		t.Errorf("active_profile = %q, want the empty string", got)
	}
	if !strings.Contains(m.infoMsg, config.DefaultProfileName) {
		t.Errorf("infoMsg = %q, want it to name default", m.infoMsg)
	}
}

// TestProfileDialogConfirmAppliesSelection 面板确认那条链路：出栈 + 落盘 + 提示。
//
// 只造一个命名档，于是行序确定：default 恒排首位（内置档），work 第二 ——
// 数字键 2 就是 work，不必依赖游标初始位置。
func TestProfileDialogConfirmAppliesSelection(t *testing.T) {
	home := tuiProfileHome(t)
	seedProfileToml(t, home, "work")

	m := chromeTestModel(t, 100, 40)
	m.currentLang = LangChinese
	m.profileCommand(nil)
	if m.dialogStack.Len() != 1 {
		t.Fatalf("面板没有打开（%d 层）", m.dialogStack.Len())
	}

	if _, _, handled := m.handleDialogStackKey(keyMsgFor("2")); !handled {
		t.Fatal("数字键没有被对话框层消费")
	}

	if m.dialogStack.Len() != 0 {
		t.Errorf("确认后对话框没有收起，剩余 %d 层", m.dialogStack.Len())
	}
	if got := readActiveProfilePreference(t, home); got != "work" {
		t.Errorf("active_profile = %q, want work", got)
	}
	if !strings.Contains(m.infoMsg, "work") || !strings.Contains(m.infoMsg, "重启") {
		t.Errorf("infoMsg = %q, want the name plus the restart hint", m.infoMsg)
	}
}

// TestProfileDialogCancelWritesNothing 取消必须什么都不写：这条是"面板只是选择器"的闸门。
func TestProfileDialogCancelWritesNothing(t *testing.T) {
	home := tuiProfileHome(t)
	seedProfileToml(t, home, "work")

	m := chromeTestModel(t, 100, 40)
	m.currentLang = LangChinese
	m.profileCommand(nil)

	if _, _, handled := m.handleDialogStackKey(keyMsgFor("esc")); !handled {
		t.Fatal("Esc 没有被对话框层消费")
	}
	if m.dialogStack.Len() != 0 {
		t.Errorf("Esc 之后面板还在（%d 层）", m.dialogStack.Len())
	}
	if _, err := os.Stat(tuiActiveProfilePath(t, home)); !os.IsNotExist(err) {
		t.Fatalf("取消却写了偏好文件（stat err = %v）", err)
	}
	if m.infoMsg != "" {
		t.Errorf("infoMsg = %q, want it untouched on cancel", m.infoMsg)
	}
}

// TestApplyProfileSelectionKeepsRuntimeUntouched 方案 A 的三条铁律，逐条显式断言：
//  1. m.com.Config 指针不变；
//  2. client.ConfigPath 字符串不变；
//  3. ~/.yagent 下除 active_profile 外没有新增/修改/删除的文件。
//
// 任何一条破了，就说明有人把"写偏好"偷偷实现成了热切换。
func TestApplyProfileSelectionKeepsRuntimeUntouched(t *testing.T) {
	home := tuiProfileHome(t)
	workPath := seedProfileToml(t, home, "work")
	seedProfileToml(t, home, "infra")

	cfg := &config.Config{}
	client := &llm.Client{Config: cfg, ConfigPath: workPath}
	assistant, err := app.NewYagent(client)
	if err != nil {
		t.Fatalf("NewYagent: %v", err)
	}

	m := chromeTestModel(t, 100, 40)
	m.assistant = assistant
	m.com = &common.Common{Config: cfg}
	m.currentLang = LangChinese

	configPtr := m.com.Config
	configPath := client.ConfigPath
	before := yagentTreeSnapshot(t, home)

	m.applyProfileSelection("infra")

	if m.com.Config != configPtr {
		t.Error("m.com.Config 指针被换掉了：配置被热切换了（工具引擎 / 六个 Agent / MCP 都还持着启动那一刻的旧值）")
	}
	if client.ConfigPath != configPath {
		t.Errorf("client.ConfigPath = %q, want %q unchanged", client.ConfigPath, configPath)
	}
	if got := readActiveProfilePreference(t, home); got != "infra" {
		t.Errorf("active_profile = %q, want infra", got)
	}
	// 本次进程还跑在 work 上：提示必须点明这一点，不能只报"已切换"。
	if !strings.Contains(m.infoMsg, "本次进程继续使用 work") {
		t.Errorf("infoMsg = %q, want the old profile named as still in use", m.infoMsg)
	}

	after := yagentTreeSnapshot(t, home)
	for path, sig := range after {
		if path == "active_profile" {
			continue
		}
		old, existed := before[path]
		if !existed {
			t.Errorf("applyProfileSelection 新建了 %s（只允许写 active_profile）", path)
			continue
		}
		if old != sig {
			t.Errorf("%s 被改写了（只允许写 active_profile）", path)
		}
	}
	for path := range before {
		if path == "active_profile" {
			continue
		}
		if _, stillThere := after[path]; !stillThere {
			t.Errorf("applyProfileSelection 删除了 %s（只允许写 active_profile）", path)
		}
	}
}

// TestActiveProfileNameChain "本次进程在用哪个档"的兜底链：
// client.ConfigPath → 偏好文件 → default。
func TestActiveProfileNameChain(t *testing.T) {
	t.Run("config path decides", func(t *testing.T) {
		home := tuiProfileHome(t)
		workPath := seedProfileToml(t, home, "work")
		// 偏好文件写的是另一个档：ConfigPath 必须压过它（--config 可以指向任意文件名）。
		if err := config.WriteActiveProfile(filepath.Join(home, ".yagent"), "infra"); err != nil {
			t.Fatalf("seed preference: %v", err)
		}
		client := &llm.Client{Config: &config.Config{}, ConfigPath: workPath}
		assistant, err := app.NewYagent(client)
		if err != nil {
			t.Fatalf("NewYagent: %v", err)
		}
		m := chromeTestModel(t, 100, 40)
		m.assistant = assistant

		if got := m.activeProfileName(); got != "work" {
			t.Errorf("activeProfileName() = %q, want work (ConfigPath is the runtime source of truth)", got)
		}
	})

	t.Run("preference when config path is empty", func(t *testing.T) {
		home := tuiProfileHome(t)
		if err := config.WriteActiveProfile(filepath.Join(home, ".yagent"), "infra"); err != nil {
			t.Fatalf("seed preference: %v", err)
		}
		m := chromeTestModel(t, 100, 40)

		if got := m.activeProfileName(); got != "infra" {
			t.Errorf("activeProfileName() = %q, want infra", got)
		}
	})

	t.Run("default when nothing is set", func(t *testing.T) {
		tuiProfileHome(t)
		m := chromeTestModel(t, 100, 40)

		if got := m.activeProfileName(); got != config.DefaultProfileName {
			t.Errorf("activeProfileName() = %q, want default", got)
		}
	})

	t.Run("nil client falls back to preference", func(t *testing.T) {
		home := tuiProfileHome(t)
		if err := config.WriteActiveProfile(filepath.Join(home, ".yagent"), "work"); err != nil {
			t.Fatalf("seed preference: %v", err)
		}
		m := chromeTestModel(t, 100, 40)
		m.assistant = &app.Yagent{} // GetClient() 返回 nil，必须走兜底而不是 panic

		if got := m.activeProfileName(); got != "work" {
			t.Errorf("activeProfileName() = %q, want work", got)
		}
	})
}

// TestApplyProfileSelectionReportsOldProfileBeforeWrite 钉住 current := m.activeProfileName()
// 必须在 WriteActiveProfile **之前**调用。
//
// 顺序写反时提示会变成"本次进程继续使用 infra — 重启后使用 infra"，读起来像没切换，
// 而真正的错是那句话自相矛盾 —— 它不会 panic，也不会让任何既有用例变红。
func TestApplyProfileSelectionReportsOldProfileBeforeWrite(t *testing.T) {
	home := tuiProfileHome(t)
	seedProfileToml(t, home, "infra")
	// 偏好文件里是 work（work.toml 是否存在无关紧要：这条用例只关心"写入前读到什么"）。
	if err := config.WriteActiveProfile(filepath.Join(home, ".yagent"), "work"); err != nil {
		t.Fatalf("seed preference: %v", err)
	}

	// ConfigPath 为空：activeProfileName() 会回落到偏好文件，即 work。
	client := &llm.Client{Config: &config.Config{}}
	assistant, err := app.NewYagent(client)
	if err != nil {
		t.Fatalf("NewYagent: %v", err)
	}
	m := chromeTestModel(t, 100, 40)
	m.assistant = assistant
	m.currentLang = LangChinese

	m.applyProfileSelection("infra")

	if got := readActiveProfilePreference(t, home); got != "infra" {
		t.Fatalf("active_profile = %q, want infra", got)
	}
	if !strings.Contains(m.infoMsg, "本次进程继续使用 work") {
		t.Errorf("infoMsg = %q, want the pre-write profile (work) named as still in use", m.infoMsg)
	}
	if strings.Contains(m.infoMsg, "本次进程继续使用 infra") {
		t.Error("提示里把新档当成\"本次进程在用\"：current 读晚了，顺序写反了")
	}
}

// ── `:profile create` ────────────────────────────────────────────────────────
//
// 这一组用例盯的是"新建"能力的四条不变量：
//  1. 建出来的是**磁盘上的档**（内容来自指定来源），不是内存里的一份配置；
//  2. 建完**不**选中 —— active_profile 不许被写（用户可能只是想先建好再挑）；
//  3. 方案 A 的铁律照旧：m.com.Config 指针与 client.ConfigPath 都不动；
//  4. 参数写错给用法行、非法名/已存在/源缺失各给一句说清后果的话，且磁盘零新增。

// tuiProfileFilePath 返回 <configDir>/<name>.toml 的期望路径。
// 刻意自己算一遍：断言"文件落在哪"不该复用被测代码的返回值。
func tuiProfileFilePath(t *testing.T, name string) string {
	t.Helper()
	path, err := config.ProfileConfigPath(config.ConfigDir(), name)
	if err != nil {
		t.Fatalf("ProfileConfigPath(%q): %v", name, err)
	}
	return path
}

// addedYagentPaths 列出 after 里有、before 里没有的相对路径（$HOME/.yagent 之下）。
//
// 用它而不是"数文件个数"：命令只该新建一个档，而"改了一个文件但个数没变"
// 正是最危险的退化形态。
func addedYagentPaths(before, after map[string]string) []string {
	added := make([]string, 0, len(after))
	for path := range after {
		if _, ok := before[path]; !ok {
			added = append(added, path)
		}
	}
	return added
}

// TestProfileCommandCreateFromTemplate 全新机器上 `:profile create work`：
// 用内置模板建出 work.toml，提示说清"建了什么、从哪来、路径、下一步"。
//
// 同时断言运行期状态原样：这是"create 只碰磁盘"的闸门 —— 它不是隐藏的热切换。
func TestProfileCommandCreateFromTemplate(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang Language
		want []string
	}{
		{name: "chinese", lang: LangChinese, want: []string{"已创建配置档 work", "内置模板", ":profile", "重启"}},
		{name: "english", lang: LangEnglish, want: []string{`created profile "work"`, "built-in template", ":profile", "restart"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := tuiProfileHome(t)

			cfg := &config.Config{}
			activePath := tuiProfileFilePath(t, config.DefaultProfileName)
			client := &llm.Client{Config: cfg, ConfigPath: activePath}
			assistant, err := app.NewYagent(client)
			if err != nil {
				t.Fatalf("NewYagent: %v", err)
			}

			m := chromeTestModel(t, 100, 40)
			m.assistant = assistant
			m.com = &common.Common{Config: cfg}
			m.currentLang = tc.lang

			configPtr := m.com.Config
			before := yagentTreeSnapshot(t, home)

			if cmd := m.profileCommand([]string{"create", "work"}); cmd != nil {
				t.Fatalf("profileCommand(create work) = %v, want nil（建档是同步的磁盘操作，不起异步动作）", cmd)
			}

			created := tuiProfileFilePath(t, "work")
			data, err := os.ReadFile(created)
			if err != nil {
				t.Fatalf("work 档没有被创建（%v）", err)
			}
			// 内容来自内嵌模板：抽样一个模板必有的键，而不是断言非空
			// （非空在"模板被换成半份"时照样通过；字节级比对在 config 包那侧）。
			if !strings.Contains(string(data), "use_provider") {
				t.Errorf("新建的档不像模板内容：%q", string(data))
			}
			if perm := fileMode(t, created); perm != 0o600 {
				t.Errorf("新建档权限 = %o, want 600（里面有明文 api_key）", perm)
			}

			for _, want := range tc.want {
				if !strings.Contains(m.infoMsg, want) {
					t.Errorf("infoMsg = %q, want it to contain %q", m.infoMsg, want)
				}
			}

			// 建完不选中：偏好文件不许出现。
			if got := readActiveProfilePreference(t, home); got != "" {
				t.Errorf("active_profile = %q, want it untouched（create 不自动选中）", got)
			}
			// 方案 A：运行期状态一个字都不许动。
			if m.com.Config != configPtr {
				t.Error("m.com.Config 指针被换掉了：create 热切换了配置")
			}
			if client.ConfigPath != activePath {
				t.Errorf("client.ConfigPath = %q, want %q unchanged", client.ConfigPath, activePath)
			}
			if m.dialogStack.Len() != 0 {
				t.Errorf("create 不该弹面板，却有 %d 层", m.dialogStack.Len())
			}

			added := addedYagentPaths(before, yagentTreeSnapshot(t, home))
			if len(added) != 1 || added[0] != filepath.Join("config", "work.toml") {
				t.Errorf("新增条目 = %v, want exactly [config/work.toml]", added)
			}
		})
	}
}

// fileMode 返回路径的权限位。
func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.Mode().Perm()
}

// TestProfileCommandCreateFromNamedSource `from <src>` 抄的是指定档的**字节**，
// 同时钉住 `:` 前缀也认得带参形态（`:profile create work from infra`）——
// switch 只认完整命令名，带参分支必须走 matchSlashCommand 那一层。
func TestProfileCommandCreateFromNamedSource(t *testing.T) {
	home := tuiProfileHome(t)
	seedProfileToml(t, home, "infra")

	m := chromeTestModel(t, 100, 40)
	m.currentLang = LangChinese

	if cmd := m.processCommand(":profile create work from infra"); cmd != nil {
		t.Fatalf("`:profile create work from infra` 返回了 cmd %v, want nil", cmd)
	}

	data, err := os.ReadFile(tuiProfileFilePath(t, "work"))
	if err != nil {
		t.Fatalf("work 档没有被创建（%v）", err)
	}
	if string(data) != "# profile infra\n" {
		t.Errorf("work 内容 = %q, want infra 的字节", data)
	}
	if !strings.Contains(m.infoMsg, "infra") {
		t.Errorf("infoMsg = %q, want it to name the source profile", m.infoMsg)
	}
	if got := readActiveProfilePreference(t, home); got != "" {
		t.Errorf("active_profile = %q, want it untouched", got)
	}
}

// TestProfileCommandCreateAcceptsDoubleDashFrom `--from` 与 `from` 等价（CLI 肌肉记忆），
// 顺带钉住 `/` 前缀那条注册表。
func TestProfileCommandCreateAcceptsDoubleDashFrom(t *testing.T) {
	home := tuiProfileHome(t)
	seedProfileToml(t, home, "infra")

	m := chromeTestModel(t, 100, 40)
	m.currentLang = LangEnglish

	if cmd := m.processCommand("/profile create work --from infra"); cmd != nil {
		t.Fatalf("`/profile create work --from infra` 返回了 cmd %v, want nil", cmd)
	}
	data, err := os.ReadFile(tuiProfileFilePath(t, "work"))
	if err != nil {
		t.Fatalf("work 档没有被创建（%v）", err)
	}
	if string(data) != "# profile infra\n" {
		t.Errorf("work 内容 = %q, want infra 的字节", data)
	}
	if !strings.Contains(m.infoMsg, `copied from "infra"`) {
		t.Errorf("infoMsg = %q, want it to say what was copied", m.infoMsg)
	}
}

// TestProfileCommandCreateRejectsExisting 已存在时明确说"不会覆盖"，且原文件不被改写。
func TestProfileCommandCreateRejectsExisting(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang Language
		want string
	}{
		{name: "chinese", lang: LangChinese, want: "已存在"},
		{name: "english", lang: LangEnglish, want: "already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := tuiProfileHome(t)
			workPath := seedProfileToml(t, home, "work")

			m := chromeTestModel(t, 100, 40)
			m.currentLang = tc.lang
			before := yagentTreeSnapshot(t, home)
			permBefore := fileMode(t, workPath)

			m.profileCommand([]string{"create", "work"})

			if !strings.Contains(m.infoMsg, tc.want) {
				t.Errorf("infoMsg = %q, want it to contain %q", m.infoMsg, tc.want)
			}
			data, err := os.ReadFile(workPath)
			if err != nil {
				t.Fatalf("read work: %v", err)
			}
			if string(data) != "# profile work\n" {
				t.Errorf("已存在的档被改写了：%q", data)
			}
			if got := fileMode(t, workPath); got != permBefore {
				t.Errorf("已存在的档权限从 %o 变成 %o", permBefore, got)
			}
			if added := addedYagentPaths(before, yagentTreeSnapshot(t, home)); len(added) != 0 {
				t.Errorf("失败的 create 新增了 %v, want none", added)
			}
		})
	}
}

// TestProfileCommandCreateUsageErrors 参数写错要回**用法行**，不是 Unknown command、
// 不 panic、更不许建出文件来。`create work from`（缺源值）是最容易被静默忽略的一例：
// 忽略掉的结果是从 default 抄了一份，而用户以为他指定了来源。
func TestProfileCommandCreateUsageErrors(t *testing.T) {
	cases := [][]string{
		{"create"},
		{"create", "work", "from"},
		{"create", "work", "--from"},
		{"create", "work", "from", "base", "extra"},
		{"create", "work", "--from=base"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := tuiProfileHome(t)
			m := chromeTestModel(t, 100, 40)
			m.currentLang = LangChinese
			before := yagentTreeSnapshot(t, home)

			m.profileCommand(args)

			if !strings.Contains(m.infoMsg, "用法：:profile create") {
				t.Errorf("infoMsg = %q, want the usage line", m.infoMsg)
			}
			if added := addedYagentPaths(before, yagentTreeSnapshot(t, home)); len(added) != 0 {
				t.Errorf("参数错误却建出了 %v", added)
			}
		})
	}
}

// TestProfileCommandCreateRejectsInvalidName 非法名在写盘之前就被拒：提示说非法，
// 且 $HOME 下零新增（`../evil` 这类不能变成"写到别处去"）。
func TestProfileCommandCreateRejectsInvalidName(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang Language
		want string
	}{
		{name: "chinese", lang: LangChinese, want: "非法档名或参数"},
		{name: "english", lang: LangEnglish, want: "invalid profile name or arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := tuiProfileHome(t)
			m := chromeTestModel(t, 100, 40)
			m.currentLang = tc.lang
			before := yagentTreeSnapshot(t, home)

			m.profileCommand([]string{"create", "../evil"})

			if !strings.Contains(m.infoMsg, tc.want) {
				t.Errorf("infoMsg = %q, want it to contain %q", m.infoMsg, tc.want)
			}
			if added := addedYagentPaths(before, yagentTreeSnapshot(t, home)); len(added) != 0 {
				t.Errorf("非法名却新建了 %v", added)
			}
			if _, err := os.Stat(filepath.Join(home, ".yagent", "evil.toml")); !os.IsNotExist(err) {
				t.Errorf("越界路径被写出来了（stat err = %v）", err)
			}
		})
	}
}

// TestProfileCommandCreateRejectsMissingSource 源档不存在与"参数写错"是两类失败：
// 前者的错在磁盘上（缺文件），后者的错在命令行里 —— 这里钉住它给的是源缺失的提示。
func TestProfileCommandCreateRejectsMissingSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang Language
		want string
	}{
		{name: "chinese", lang: LangChinese, want: "源配置档不存在"},
		{name: "english", lang: LangEnglish, want: "source profile does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := tuiProfileHome(t)
			m := chromeTestModel(t, 100, 40)
			m.currentLang = tc.lang
			before := yagentTreeSnapshot(t, home)

			m.profileCommand([]string{"create", "work", "from", "ghost"})

			if !strings.Contains(m.infoMsg, tc.want) {
				t.Errorf("infoMsg = %q, want it to contain %q", m.infoMsg, tc.want)
			}
			if added := addedYagentPaths(before, yagentTreeSnapshot(t, home)); len(added) != 0 {
				t.Errorf("源缺失却新建了 %v", added)
			}
		})
	}
}
