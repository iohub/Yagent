package memory

// rollout_checkpoint.go — 检查点的两个可计算部分：消息序列的指纹、工作区事实。
//
// 这两件事必须在 memory 里，因为**写入端和重放端要用同一套规则**：指纹不一致就说明
// 这份 rollout 与当时的会话对不上号，而"对不上号"唯一的判据是两边算法逐字节相同。
// 规则放在任何一侧的包里，另一侧就只能抄 —— 抄的东西一定会漂。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"yagent/internal/gitinfo"
	"yagent/internal/llm"
)

// digestPrefix 是指纹输出的长度（sha256 hex 的前 16 个字符 = 64 bit）。
// 用途是"同一份消息序列"的等价判据，不是抗碰撞签名 —— 64 bit 对这个目的足够，
// 而完整 64 字符会让每条检查点白白大上一截。
const digestPrefix = 16

// DigestMessages 按固定规则给一段消息序列算指纹。
//
// 规范化规则（改动它等于改 rollout 的契约，必须同时改 rollout_replay.go 的读侧）：
// 每条消息取 role|content|tool_call_id|reasoning|排序后的 "name(args)" 工具调用列表，
// 消息之间用 \x00 分隔。
//
// 为什么把 Reasoning 也算进去：thinking 是要回灌给 provider 的（AGENTS.md §2.3），
// 丢了它的"同一段对话"其实不是同一段 —— 指纹必须能察觉这件事。
// 为什么不看 IsAnchored / TruncationMarker：那两个是进程内的压缩策略标记，
// 不落盘，重放侧无从复现，算进去只会制造假降级。
func DigestMessages(msgs []llm.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte(0)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s|", m.Role, m.Content, m.ToolCallID, m.Reasoning)
		calls := make([]string, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			calls = append(calls, tc.Function.Name+"("+tc.Function.Arguments+")")
		}
		sort.Strings(calls)
		b.WriteString(strings.Join(calls, ","))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:digestPrefix]
}

// CollectWorkspace 抓取停止那一刻的工作区事实。
//
// ctx 一律经 gitinfo 的 WithoutCancel 派生：这条探测发生在停止/取消路径上，
// 直接拿死掉的 ctx 跑 git 会得到"干净"这个错误答案（§2 R6 —— 命令根本没跑起来，
// 却被记成没改动）。探测失败记进 StatusError，让恢复侧看得见"这里不知道"，
// 而不是把它压成 dirty=false。
func CollectWorkspace(ctx context.Context, dir string) WorkspaceState {
	ws := WorkspaceState{Cwd: dir}
	if dir == "" {
		return ws
	}
	info := gitinfo.Collect(ctx, dir)
	ws.GitSHA, ws.GitBranch, ws.GitOrigin = info.SHA, info.Branch, info.OriginURL

	dirty, files, truncated, err := gitinfo.Status(ctx, dir)
	switch {
	case err != nil:
		ws.StatusError = err.Error()
	default:
		ws.Dirty = dirty
		ws.DirtyFiles = files
		ws.DirtyFilesTruncated = truncated
	}
	return ws
}

// WorkspaceMatchesHead 判断"当前 HEAD 是否就是检查点记下那个提交"。
// 恢复侧的判据 5（工作区变了就不能声称"我改过 X"）用它，而不是让调用点各自比字符串。
//
// 两边都空时返回 true：那不是"匹配"，而是"没有判据"。调用点必须把这种情况记进
// Degraded —— 一次没有 git 身份的会话照样能重放，但用户有权知道这个校验没做。
func WorkspaceMatchesHead(ws WorkspaceState, currentSHA string) bool {
	if ws.GitSHA == "" && currentSHA == "" {
		return true
	}
	return ws.GitSHA != "" && ws.GitSHA == currentSHA
}

// FinalizeCheckpoint 从这份 rollout 文件本身补齐检查点里可派生的两个字段
// （MessageCount / Digest），并把重建过程中的降级事实一并回报。
//
// 指纹必须由**读侧**算，而不是由写侧拿内存里的 messages 算：两边只要规则差一点
// （压缩改写过内存里的历史是常态），恢复时就会"对不上"，把一个本来能重放的会话
// 判成损坏。让写入端调用与重放端同一个函数，指纹不一致就只剩一种可能 ——
// 文件真的被截断、损坏或动过了，那正是这个校验要抓的东西。
//
// 重放起点 Step 不在这里改：它是"已完成步数"，只有循环自己知道。
func FinalizeCheckpoint(path string, base AgentCheckpoint) (AgentCheckpoint, []string) {
	replay, err := ReadRolloutForReplay(path, ReplayOptions{})
	if err != nil {
		base.MessageCount = 0
		base.Digest = ""
		return base, append([]string{"checkpoint digest unavailable: " + err.Error()},
			replayDegraded(replay)...)
	}
	base.MessageCount = len(replay.Messages)
	base.Digest = replay.Digest
	return base, replay.Degraded
}

func replayDegraded(r *Replay) []string {
	if r == nil {
		return nil
	}
	return r.Degraded
}

// CheckpointInput 是写一条检查点所需的最小事实。
type CheckpointInput struct {
	AgentName string
	Model     string
	Tier      string
	StopKind  string
	Steps     int
	MaxSteps  int
	// CollectWorkspace 控制要不要跑 git status。它由调用点从配置里取，
	// 而不是 memory 自己读配置：memory 不认识 config，反过来让它认识配置
	// 就等于给持久化层加了一个策略依赖。
	CollectWorkspace bool
}

// WriteCheckpoint 往这份 rollout 落一条停止坐标。
//
// 之所以是函数而不是每个循环各写一遍：写检查点的两个包（internal/agents 与
// internal/agents/director）不能互相 import，任何"抄一份"的做法都会让父子两份
// 检查点在字段语义上慢慢分叉，而分叉的表现是恢复时其中一边判据永远不成立。
//
// 指纹走 FinalizeCheckpoint ⇒ 由读侧算，理由写在那儿。
func WriteCheckpoint(ctx context.Context, rw *RolloutWriter, in CheckpointInput) {
	if rw == nil || !rw.Enabled() {
		return
	}
	cwd, _ := os.Getwd()
	cp := AgentCheckpoint{
		StopKind:     in.StopKind,
		AgentName:    in.AgentName,
		DelegationID: DelegationIDFrom(ctx),
		Step:         in.Steps,
		MaxSteps:     in.MaxSteps,
		Model:        in.Model,
		Tier:         in.Tier,
	}
	if in.CollectWorkspace {
		// gitinfo 内部用 WithoutCancel 派生预算：这条路径上 ctx 常常已经取消。
		cp.Workspace = CollectWorkspace(ctx, cwd)
	} else {
		cp.Workspace.Cwd = cwd
		cp.Workspace.StatusError = "workspace probe disabled by [resume].checkpoint_workspace"
	}

	filled, degraded := FinalizeCheckpoint(rw.FilePath(), cp)
	if len(degraded) > 0 {
		slog.Debug("checkpoint: rebuilt with losses", "agent", in.AgentName, "degraded", degraded)
	}
	if err := rw.WriteAgentCheckpoint(filled); err != nil {
		// 检查点是"能不能继续"的唯一坐标。写失败必须报错：静默的话恢复侧会把一份
		// 本来可用的会话当成存量文件拒绝重放，而没人知道为什么。
		slog.Error("checkpoint: failed to write agent_checkpoint",
			"agent", in.AgentName, "file", rw.FilePath(), "error", err)
	}
}

// nowRFC3339 给检查点的时间戳用（与 nowISO8601 不同：那一版没有时区后缀，
// 而检查点的创建时间要被跨机器比较，必须有偏移信息）。
func nowRFC3339() string {
	return time.Now().Format(time.RFC3339Nano)
}
