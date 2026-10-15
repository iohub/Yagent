package config

// resume_test.go — [resume] 的缺省归一与 kill-switch（docs/pause-resume-design.md §8.4）。
//
// 每条用例都对应一个"错了不报错"的形状：整段缺省会把重放预算归零
// （表现是"恢复永远超预算"），显式 0 被当成没写会让用户"放开上限"的意图失效。

import (
	"testing"
)

func loadResumeCfg(t *testing.T, section string) *Config {
	t.Helper()
	t.Cleanup(func() {
		SetGlobalResume(DefaultResumeConfig(), ResumePresence{})
	})
	cfg, err := LoadFromFile(minimalConfig(t, section))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	return cfg
}

func TestResumeDefaultsWhenSectionAbsent(t *testing.T) {
	cfg := loadResumeCfg(t, "")
	d := DefaultResumeConfig()

	if cfg.resumeSectionDefined {
		t.Error("resumeSectionDefined = true, want false for a config without the section")
	}
	if cfg.Resume != (ResumeConfig{}) {
		t.Errorf("raw decode = %+v, want all zero (TOML filled nothing)", cfg.Resume)
	}
	active := GlobalResume()
	if active != d {
		t.Errorf("effective config = %+v, want defaults %+v", active, d)
	}
	// 最危险的一条：缺段落不等于"关掉这套机制"。
	if !active.Enable {
		t.Error("Enable = false 时整段缺省会把检查点与重放静默关掉")
	}
	if active.MaxReplayMessages != 2000 {
		t.Errorf("MaxReplayMessages = %d, want 2000", active.MaxReplayMessages)
	}
}

// 显式写 0 = 不限条数。把它当"没写"补成 2000，用户放宽上限的意图就消失了；
// 反过来把"没写"当成 0（不限），一次几万条的存量 rollout 会被整个塞进上下文。
func TestResumeExplicitZeroMessageCap(t *testing.T) {
	loadResumeCfg(t, `
[resume]
max_replay_messages = 0
`)
	if got := GlobalResume().MaxReplayMessages; got != 0 {
		t.Errorf("MaxReplayMessages = %d, want 0 (显式 0 = 不限)", got)
	}
}

func TestResumeNegativeValuesFallBack(t *testing.T) {
	loadResumeCfg(t, `
[resume]
max_replay_messages = -5
replay_token_budget = -1
`)
	active := GlobalResume()
	if active.MaxReplayMessages != 2000 {
		t.Errorf("MaxReplayMessages = %d, want 2000 for a negative value", active.MaxReplayMessages)
	}
	if active.ReplayTokenBudget != 0 {
		t.Errorf("ReplayTokenBudget = %d, want 0 for a negative value", active.ReplayTokenBudget)
	}
}

// 认不出来的策略必须回落到默认并播报，不能"看起来配上了"。
// 回落成 block 比回落成 warn 更糟：用户会以为危险的重放被挡住了。
func TestResumeUnknownWorkspacePolicyFallsBackToWarn(t *testing.T) {
	loadResumeCfg(t, `
[resume]
degrade_on_workspace_change = "blockk"
`)
	if got := GlobalResume().DegradeOnWorkspaceChange; got != DegradeWorkspaceWarn {
		t.Errorf("DegradeOnWorkspaceChange = %q, want %q", got, DegradeWorkspaceWarn)
	}
}

func TestResumeExplicitBlockPolicyKept(t *testing.T) {
	loadResumeCfg(t, `
[resume]
degrade_on_workspace_change = "block"
`)
	if got := GlobalResume().DegradeOnWorkspaceChange; got != DegradeWorkspaceBlock {
		t.Errorf("DegradeOnWorkspaceChange = %q, want %q", got, DegradeWorkspaceBlock)
	}
}

func TestResumeEnableFalseIsHonoured(t *testing.T) {
	loadResumeCfg(t, `
[resume]
enable = false
checkpoint_workspace = false
`)
	active := GlobalResume()
	if active.Enable {
		t.Error("Enable = false 被归一化改回了 true")
	}
	if ResumeEnabled() {
		t.Error("ResumeEnabled() 在 enable=false 时必须为假")
	}
	if active.CheckpointWorkspace {
		t.Error("CheckpointWorkspace = false 被改回 true")
	}
}

// kill-switch 优先于配置：出问题时先关再用环境变量重启排查，不该还要改文件。
func TestResumeKillSwitchBeatsConfig(t *testing.T) {
	loadResumeCfg(t, "")
	if !ResumeEnabled() {
		t.Fatal("整段缺省应当启用")
	}
	t.Setenv("YAGENT_RESUME", "0")
	if ResumeEnabled() {
		t.Error("YAGENT_RESUME=0 没有关掉这套机制")
	}
	t.Setenv("YAGENT_RESUME", "1")
	if !ResumeEnabled() {
		t.Error("只有取值恰好是 0 才算关闭，其它值必须忽略")
	}
}

// 只剩失效键的段落 = 与没写过同样处理（[blackboard] 已确立的口径）：
// 如果按"表头在就尊重段内取值"处理，一段全是拼错键的配置会把默认值全丢掉。
// 用户的正确性靠 stale 键告警兜住，而不是靠把这段当成"配置过"。
func TestResumeStaleOnlySectionFallsBackToDefaults(t *testing.T) {
	cfg := loadResumeCfg(t, `
[resume]
enble = false
`)
	if cfg.resumeSectionDefined {
		t.Error("resumeSectionDefined = true, want false: 只剩失效键的段落等于没写过")
	}
	if !GlobalResume().Enable {
		t.Error("enble 不是合法键，不该被当成 enable=false 生效")
	}
	if GlobalResume().MaxReplayMessages != 2000 {
		t.Errorf("MaxReplayMessages = %d, want 2000（缺段必须回到整表默认）", GlobalResume().MaxReplayMessages)
	}
}

// 失效键与显式取值混写：段仍算"被写过"，那个能解析的键必须被尊重。
func TestResumeStaleKeyAlongsideExplicitValve(t *testing.T) {
	cfg := loadResumeCfg(t, `
[resume]
enable = false
enble = true
`)
	if !cfg.resumeSectionDefined {
		t.Error("resumeSectionDefined = false, want true：段里有一个能解析的键")
	}
	if GlobalResume().Enable {
		t.Error("显式的 enable=false 被拼错的键带偏了")
	}
}
