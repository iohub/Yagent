package agents

// resume_degrade_test.go — §10 G6：降级绝不静默。
//
// 这是整套恢复机制最容易被"测了个寂寞"的部分：只测"生产路径没降级"等于没测 ——
// 删掉任何一条判据都不会有人发现，而后果是拿一个对不上号的上下文去重放，
// 表现成"恢复之后 Agent 行为怪怪的"，比不能恢复难查一个数量级。
// 所以这里给每条判据都配一个**能触发它**的夹具，并要求理由同时出现在
// 返回值与报告正文里。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/blackboard"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/pause"
)

// childFixture 写一份子 Agent 的 rollout：若干消息 + 一条检查点（指纹由读侧算）。
// tamper 在检查点落盘之后改文件，用来造"损坏"的夹具。
func childFixture(t *testing.T, delegationID string, msgs []llm.Message, mutate func(*memory.AgentCheckpoint), tamper func(path string)) string {
	t.Helper()
	w, err := memory.NewRolloutWriter("coding", "task-degrade-"+strings.ReplaceAll(t.Name(), "/", "_"), "degrade-test")
	if err != nil || !w.Enabled() {
		t.Skipf("writer unavailable: %v", err)
	}
	w.SetDelegation(delegationID, "sess-parent")
	cwd, _ := os.Getwd()
	if err := w.WriteSessionMeta(memory.SessionMeta{ID: w.SessionID(), SessionID: w.SessionID(), Cwd: cwd}); err != nil {
		t.Fatalf("meta: %v", err)
	}
	for _, m := range msgs {
		for _, item := range memory.LLMMessageToResponseItems(m, w.NextMessageID()) {
			if err := w.WriteResponseItem(item); err != nil {
				t.Fatalf("item: %v", err)
			}
		}
	}
	cp := memory.AgentCheckpoint{
		StopKind: memory.StopKindCancelled, AgentName: "coding",
		Step: 3, MaxSteps: 10,
	}
	// 先补齐派生字段（消息数、指纹），**再**让夹具改坏它 —— 反过来的话
	// FinalizeCheckpoint 会把 mutate 的改动原样盖回去，这个 fixture 就再也触发不了
	// 判据 3 了（第一次跑就是这么发现的）。
	// 用 writer 版本而不是 FinalizeCheckpoint(w.FilePath(), …)：它会先排干落盘队列，
	// 少那一步算出来的就是"少几条记录"的指纹，判据 2 会抢在目标判据之前响。
	filled, _ := w.FinalizeCheckpoint(cp)
	if mutate != nil {
		mutate(&filled)
	}
	if err := w.WriteAgentCheckpoint(filled); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	path := w.FilePath()
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if tamper != nil {
		tamper(path)
	}
	return path
}

// degradeMessages 是"做完两步、第三步停在半路"的子会话账本。
// 里面特意含一条成功的工具输出、一条错误输出、以及一个没有 output 的悬空调用，
// 让报告的三段各有东西可抠。
func degradeMessages() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: "Autonomous software engineering agent."},
		{Role: llm.RoleUser, Content: "fix the flaky test"},
		{Role: llm.RoleAssistant, Content: "read it first", ToolCalls: []llm.ToolCall{
			{ID: "k1", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"target_file":"a_test.go"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "k1", ToolName: "read_file", Content: "package main\nfunc TestX() {}"},
		{Role: llm.RoleAssistant, Content: "<thought>\nplan:\n- [x] read the test\n- [>] rewrite the assertion\n- [ ] run the suite\n</thought>", ToolCalls: []llm.ToolCall{
			{ID: "k2", Function: llm.FunctionCall{Name: "search_replace_in_file", Arguments: `{"target_file":"a_test.go"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "k2", ToolName: "search_replace_in_file", Content: "Error: match not unique"},
		{Role: llm.RoleAssistant, Content: "retrying", ToolCalls: []llm.ToolCall{
			{ID: "k3", Function: llm.FunctionCall{Name: "run_bash", Arguments: `{"command":"go test ./..."}`}}}},
		// k3 没有 output：这就是"进行到一半"。
	}
}

// 每条判据一个夹具。删掉实现里的任何一条，对应的子用例就会红。
func TestDegradationIsNeverSilent(t *testing.T) {
	healthy := childFixture(t, "call_ok", degradeMessages(), nil, nil)

	cases := []struct {
		name    string
		stop    func(t *testing.T) DelegationStop
		wantTag string
		// wantReplayable 断言"可不可重放"这一半：只有理由没有结论，
		// 判据 5 的 warn 分支就会被写成 block 也测不出来。
		wantReplayable bool
	}{
		{
			name: "criterion1_missing_child_rollout",
			stop: func(*testing.T) DelegationStop {
				return DelegationStop{DelegationID: "call_x", AgentName: "coding", ToolName: "delegate_coding", StopKind: StopKindCancelled}
			},
			wantTag: "1:", wantReplayable: false,
		},
		{
			name: "criterion2_digest_mismatch",
			stop: func(t *testing.T) DelegationStop {
				// 往文件尾部再补一条记录：消息序列变了，检查点里的指纹没变。
				path := childFixture(t, "call_d", degradeMessages(), nil, func(p string) {
					f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
					if err != nil {
						t.Fatalf("append: %v", err)
					}
					defer f.Close()
					item := memory.LLMMessageToResponseItems(llm.Message{Role: llm.RoleUser, Content: "appended after the stop"}, "appended")
					b, _ := json.Marshal(memory.RolloutEnvelope{Type: "response_item", Payload: item[0]})
					fmt.Fprintln(f, string(b))
				})
				return stopForChild(path)
			},
			wantTag: "2:", wantReplayable: false,
		},
		{
			name: "criterion3_message_count_mismatch",
			stop: func(t *testing.T) DelegationStop {
				path := childFixture(t, "call_n", degradeMessages(), func(cp *memory.AgentCheckpoint) {
					cp.MessageCount += 2
				}, nil)
				return stopForChild(path)
			},
			wantTag: "3:", wantReplayable: false,
		},
		{
			name: "criterion4_token_budget",
			stop: func(t *testing.T) DelegationStop {
				t.Cleanup(func() { config.SetGlobalResume(config.DefaultResumeConfig(), config.ResumePresence{}) })
				config.SetGlobalResume(config.ResumeConfig{
					Enable: true, ReplayTokenBudget: 5, MaxReplayMessages: 0,
					DegradeOnWorkspaceChange: config.DegradeWorkspaceWarn, CheckpointWorkspace: true,
				}, config.ResumePresence{Section: true, MaxReplayMessages: true, DegradeOnWorkspace: true})
				return stopForChild(healthy)
			},
			wantTag: "4:", wantReplayable: false,
		},
		{
			name: "criterion5_workspace_block",
			stop: func(t *testing.T) DelegationStop {
				repo := gitRepoWithCommit(t)
				t.Cleanup(func() { config.SetGlobalResume(config.DefaultResumeConfig(), config.ResumePresence{}) })
				config.SetGlobalResume(config.ResumeConfig{
					Enable: true, DegradeOnWorkspaceChange: config.DegradeWorkspaceBlock, CheckpointWorkspace: true,
				}, config.ResumePresence{Section: true, DegradeOnWorkspace: true})
				path := childFixture(t, "call_ws", degradeMessages(), func(cp *memory.AgentCheckpoint) {
					cp.Workspace = memory.WorkspaceState{GitSHA: "deadbeef1234", GitBranch: "main", Cwd: repo}
				}, nil)
				return stopForChild(path)
			},
			wantTag: "5:", wantReplayable: false,
		},
		{
			name: "criterion5_workspace_warn_still_replays",
			stop: func(t *testing.T) DelegationStop {
				repo := gitRepoWithCommit(t)
				path := childFixture(t, "call_ws2", degradeMessages(), func(cp *memory.AgentCheckpoint) {
					cp.Workspace = memory.WorkspaceState{GitSHA: "deadbeef9999", GitBranch: "main", Cwd: repo}
				}, nil)
				return stopForChild(path)
			},
			wantTag: "5:", wantReplayable: true,
		},
		{
			name: "criterion6_browser",
			stop: func(*testing.T) DelegationStop {
				return DelegationStop{DelegationID: "call_b", AgentName: "browser", ToolName: "delegate_browser",
					ChildRolloutPath: healthy, StopKind: StopKindCancelled}
			},
			wantTag: "6:", wantReplayable: false,
		},
		{
			name: "criterion7_max_steps",
			stop: func(*testing.T) DelegationStop {
				return DelegationStop{DelegationID: "call_m", AgentName: "coding", StopKind: StopKindMaxSteps, ChildRolloutPath: healthy}
			},
			wantTag: "7:", wantReplayable: false,
		},
		{
			name: "timeout_is_not_replay_safe",
			stop: func(*testing.T) DelegationStop {
				return DelegationStop{DelegationID: "call_t", AgentName: "coding", StopKind: StopKindIdleTimeout, ChildRolloutPath: healthy}
			},
			wantTag: "extra:", wantReplayable: false,
		},
		{
			name: "no_checkpoint_in_child",
			stop: func(t *testing.T) DelegationStop {
				path := childFixture(t, "call_nc", degradeMessages(), nil, func(p string) {
					dropRecordType(p, memory.RecordAgentCheckpoint)
				})
				return stopForChild(path)
			},
			wantTag: "2:", wantReplayable: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop := tc.stop(t)
			replayable, reasons := assessReplay(stop)
			if replayable != tc.wantReplayable {
				t.Errorf("Replayable = %v, want %v (reasons=%v)", replayable, tc.wantReplayable, reasons)
			}
			if !containsTag(reasons, tc.wantTag) {
				t.Fatalf("reasons = %v, want one carrying %q", reasons, tc.wantTag)
			}
			// 理由必须能出现在交回 Director 的那份报告里，而不只是日志里。
			report := degradedReport(stop, reasons)
			if !strings.Contains(report, tc.wantTag) {
				t.Errorf("报告里没有把降级理由带出来：%s\nreasons=%v", truncRunes(report, 400), reasons)
			}
			if !strings.Contains(report, "精确重放不可用") {
				t.Error("报告没有明说这次不是精确重放")
			}
		})
	}
}

// 健康夹具必须判成可重放，并且一条降级理由都没有。
// 反向的一半同样要测：如果任何会话都会带上 degraded 标记，那这个标记就没有信息量。
func TestHealthyStopReplaysWithoutDegradation(t *testing.T) {
	repo := gitRepoWithCommit(t)
	path := childFixture(t, "call_healthy", degradeMessages(), func(cp *memory.AgentCheckpoint) {
		cp.Workspace = memory.WorkspaceState{GitSHA: headSHA(t, repo), GitBranch: "master", Cwd: repo}
	}, nil)
	stop := stopForChild(path)

	replayable, reasons := assessReplay(stop)
	if !replayable {
		t.Fatalf("健康的落点被判成不可重放：%v", reasons)
	}
	if len(reasons) != 0 {
		t.Errorf("健康路径不该有降级理由：%v", reasons)
	}
}

// 报告的三段必须各自都从账本里抠到东西：已完成只认成功的输出、
// 悬空的那步要单独列出、剩余计划原文照抄。
func TestDegradedReportSectionsComeFromTheLedger(t *testing.T) {
	path := childFixture(t, "call_sections", degradeMessages(), nil, nil)
	stop := stopForChild(path)
	report := degradedReport(stop, []string{"1: fabricated for the assertion"})

	if !strings.Contains(report, "read_file: package main") {
		t.Errorf("已完成段没有从成功的工具输出里抠出事实：\n%s", report)
	}
	if strings.Contains(report, "- search_replace_in_file") {
		t.Errorf("错误输出的工具被当成已完成：\n%s", report)
	}
	if !strings.Contains(report, "run_bash (call_id k3)") {
		t.Errorf("进行到一半那段没有列出悬空调用：\n%s", report)
	}
	if !strings.Contains(report, "- [>] rewrite the assertion") || !strings.Contains(report, "- [ ] run the suite") {
		t.Errorf("剩余计划没有原文照抄 T&P 块：\n%s", report)
	}
	if strings.Contains(report, "- [x] read the test") {
		t.Error("已完成的计划项不该出现在剩余计划里")
	}
	if !strings.Contains(report, "Suggested next delegation task") {
		t.Errorf("报告缺了可直接续委派的那一段：\n%s", report)
	}
}

// 判据 8：重放过程中委派失败 → 交回报告并说清原因，而不是把错误原样抛给 Director
// （那会被它当成"委派失败"从而重做整段 —— §2 R1 就是这个形状）。
func TestReplayFailureDegradesInsteadOfErroring(t *testing.T) {
	a := newResumeTestDirector(t)
	stop := stopForChild(childFixture(t, "call_fail", degradeMessages(), nil, nil))

	// 让子 Agent 的模型直接失败：mockLLM 没配响应就报错。
	a.CodingAgent.LLM = &mockLLM{responses: nil}

	text, degraded := a.resumeDelegation(t.Context(), stop)
	if !strings.Contains(text, "精确重放不可用") {
		t.Errorf("重放失败后应当交回交接报告，实际得到：\n%s", truncRunes(text, 300))
	}
	if !containsTag(degraded, "8:") {
		t.Errorf("degraded = %v, want an 8: reason", degraded)
	}
}

// C 层的落盘一半：降级报告必须同时是一张 status=degraded 的便签，
// 并且 ref 要出现在交回 Director 的文本里 —— 没有 ref，下一次委派的 Agent
// 只能重新调查，那份报告就成了一次性文本。
func TestDegradedHandoffWritesDegradedNote(t *testing.T) {
	a := newResumeTestDirector(t)
	a.SetBoardScope("resume-note-board", "resume-note-task")
	stop := stopForChild(childFixture(t, "call_note", degradeMessages(), nil, nil))

	text, degraded := a.degradedHandoff(stop, []string{"1: no sub-agent rollout carries this delegation_id"})
	if len(degraded) == 0 {
		t.Fatal("降级理由不能丢")
	}
	ref := noteRefFromText(text)
	if ref == "" {
		t.Fatalf("报告里没有 note ref：\n%s", text)
	}

	note, err := blackboard.ReadNote(a.boardID, ref)
	if err != nil {
		t.Fatalf("read note %s: %v", ref, err)
	}
	if note.Status != blackboard.StatusDegraded {
		t.Errorf("note.Status = %q, want %q", note.Status, blackboard.StatusDegraded)
	}
	if !strings.Contains(note.Handoff.Degraded, "1:") {
		t.Errorf("note 的 degraded 字段没有带上判据：%q", note.Handoff.Degraded)
	}
	// 正文是报告全文，不是摘要：便签存在的意义就是让下一个 Agent 不用重查。
	if !strings.Contains(note.Body, "精确重放不可用") {
		t.Errorf("便签正文不是报告全文：\n%s", truncRunes(note.Body, 200))
	}
}

func noteRefFromText(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "note: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// stopForChild 造一个指向某份子文件、其它字段都"正常"的落点。
func stopForChild(path string) DelegationStop {
	return DelegationStop{
		ChildRolloutPath:    path,
		DirectorRolloutPath: "ignored-in-this-fixture",
		DelegationID:        "call_fail",
		ToolName:            "delegate_coding",
		AgentName:           "coding",
		StopKind:            memory.StopKindCancelled,
		OriginalArgs:        `{"task":"fix the flaky test"}`,
		MaxSteps:            10,
	}
}

// containsTag 判断是否有理由带着这个前缀。前缀而不是全文：
// 全文会随措辞变化而红，前缀才是"这条判据有没有生效"的判据。
func containsTag(reasons []string, tag string) bool {
	for _, r := range reasons {
		if strings.Contains(r, tag) {
			return true
		}
	}
	return false
}

// gitRepoWithCommit 造一个有一次提交的小仓库并把工作目录切进去，
// 这样 currentGitSHA() 才有确定答案（判据 5 要的是"当时 vs 现在"的对比）。
func gitRepoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("git %v: %s", args, out)
		}
		return err
	}
	if err := run("init", "-q", "-b", "master"); err != nil {
		t.Skipf("git init unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := run("add", "-A"); err != nil {
		t.Skipf("git add unavailable: %v", err)
	}
	if err := run("commit", "-q", "-m", "seed"); err != nil {
		t.Skipf("git commit unavailable: %v", err)
	}
	t.Chdir(dir)
	return dir
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// dropRecordType 从文件里删掉某种类型的所有记录，用来模拟"这份文件没有检查点"。
func dropRecordType(path, recordType string) {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &env) == nil && env.Type == recordType {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
		panic(err)
	}
}

// newResumeTestDirector 只要一个能跑 resumeDelegation 的门面：
// 委派机器（delegateTo + createRolloutWriter）必须是真实的，所以走全链装配。
func newResumeTestDirector(t *testing.T) *DirectorAgent {
	t.Helper()
	engine := newScripted(map[string][]scriptStep{"coding": {fixed(textResponse("resumed and done"))}})
	director := newFullChainDirector(t, t.TempDir(), engine)
	director.SetBoardScope("degrade-board", "degrade-task")
	director.CodingAgent.LLM = engine
	return director
}

// 停住的委派**什么都不写**，所以 handoff.status 词表里没有 paused（§7.3）。
//
// 闸门 abandon 出来的是零值 result，落上去就是一张空正文的便签，而索引每轮从磁盘重扫，
// 下一次委派的任务前缀里就会多出一条"什么结论都没有"的记录 —— 那正是便签存在目的的反面。
// 反过来，真失败的委派必须留痕，所以两头都要断：只断前者，删掉整段 writeDelegateNote 都能通过。
func TestAbandonedDelegationWritesNoNote(t *testing.T) {
	a := newResumeTestDirector(t)
	a.SetBoardScope("abandon-board-"+strings.ReplaceAll(t.Name(), "/", "_"), "abandon-task")

	abandoned := &pause.AbandonedError{Reason: "/pause", Cause: context.Canceled}
	if ref := a.writeDelegateNote("coding_agent", "delegate_coding", "fix it", AgentResult{}, abandoned); ref != "" {
		t.Errorf("停住的委派写了便签：%s", ref)
	}
	if notes, err := blackboard.ListNotes(a.boardID, "coding_agent", 10); err != nil {
		t.Fatalf("ListNotes: %v", err)
	} else if len(notes) != 0 {
		t.Errorf("停住的委派在板上留下了 %d 张便签", len(notes))
	}

	failRef := a.writeDelegateNote("coding_agent", "delegate_coding", "fix it",
		AgentResult{Text: "boom"}, errors.New("llm exploded"))
	if failRef == "" {
		t.Fatal("真失败的委派没落便签")
	}
	note, err := blackboard.ReadNote(a.boardID, failRef)
	if err != nil {
		t.Fatalf("ReadNote(%s): %v", failRef, err)
	}
	if note.Status != blackboard.StatusError {
		t.Errorf("note.Status = %q, want %q", note.Status, blackboard.StatusError)
	}
}
