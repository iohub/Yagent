package browserctl

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const traceExcerptLimit = 800

// Result 是一次 CLI 调用的结果。非零退出码不是 error：
// 上游失败往往携带可自诊的信息（stderr 提示、trace 目录），交给工具层整形后再交给 LLM。
type Result struct {
	Command   string
	Args      []string
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	TimedOut  bool
	Trace     string
	Notes     []string
}

func (r *Result) OK() bool { return r != nil && r.ExitCode == 0 }

// Combined 返回给 LLM 的完整文本：stdout 优先，stderr 与 note 追加其后。
func (r *Result) Combined() string {
	if r == nil {
		return ""
	}
	var sb strings.Builder
	if s := strings.TrimRight(r.Stdout, "\n"); s != "" {
		sb.WriteString(s)
	}
	if s := strings.TrimRight(r.Stderr, "\n"); s != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(s)
	}
	if r.Trace != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "trace:\n%s", r.Trace)
	}
	for _, n := range r.Notes {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(n)
	}
	return sb.String()
}

// Session 是一次浏览器委派独占的工作区。
// 所有命令都在同一个目录里执行：上游把状态（current-tab、chrome 记录、traces）
// 写在该目录的 .browser-control/ 下，因此工作区目录就是隔离边界。
type Session struct {
	mgr *Manager
	dir string
	env []string
	mu  sync.Mutex
}

// Dir 返回工作区目录（也是 CLI 的执行目录）。
func (s *Session) Dir() string { return s.dir }

// Run 执行一个子命令。同一 Session 内串行：current-tab 与快照 ref 是磁盘共享状态，
// 并行观察会读到错页，串行的代价远小于错页的代价。
func (s *Session) Run(ctx context.Context, command string, args ...string) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.run(ctx, command, args)
	if err != nil {
		return nil, err
	}
	return s.handleDialog(ctx, res, command, args), nil
}

func (s *Session) run(ctx context.Context, command string, args []string) (*Result, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建浏览器工作区失败: %w", err)
	}

	out, err := s.mgr.spawn.Spawn(ctx, s.mgr.spawnOpts(command, args, s.dir, s.env))
	if err != nil {
		return nil, fmt.Errorf("browser-control %s 启动失败: %w", command, err)
	}
	s.mgr.touch()

	res := &Result{
		Command:   command,
		Args:      args,
		Stdout:    out.Stdout,
		Stderr:    out.Stderr,
		ExitCode:  out.ExitCode,
		Truncated: out.Truncated,
		TimedOut:  out.TimedOut,
	}
	if len(res.Stdout) > s.mgr.cfg.MaxOutputBytes {
		res.Stdout = Clip(res.Stdout, s.mgr.cfg.MaxOutputBytes)
		res.Truncated = true
	}
	if len(res.Stderr) > s.mgr.cfg.MaxOutputBytes {
		res.Stderr = Clip(res.Stderr, s.mgr.cfg.MaxOutputBytes)
		res.Truncated = true
	}
	if out.TimedOut {
		res.ExitCode = -1
		res.Notes = append(res.Notes, fmt.Sprintf("命令超时（上限 %s）", s.mgr.commandTimeout()))
	}
	if res.Truncated {
		res.Notes = append(res.Notes, "输出被截断")
	}
	if !res.OK() {
		res.Trace = s.latestTrace()
	}
	return res, nil
}

// handleDialog 处理原生 alert/confirm/prompt：这类对话框会冻结页面 JS 线程，
// 让后续 eval / page-info 一直挂到超时。默认按 [browser] dialog_policy 自动处理并重试一次。
func (s *Session) handleDialog(ctx context.Context, res *Result, command string, args []string) *Result {
	policy := s.mgr.cfg.DialogPolicy
	if policy == DialogAsk || command == "dialog" {
		return res
	}
	// 处理对话框依赖上游 daemon，而 daemon 的 IPC 只有 Unix socket
	if runtime.GOOS == "windows" {
		return res
	}
	action := DialogAccept
	if policy == DialogDismiss {
		action = DialogDismiss
	} else if policy != DialogAccept {
		action = DialogAccept
	}

	if !s.dialogLikely(ctx, res) {
		return res
	}

	handled, err := s.run(ctx, "dialog", []string{action})
	if err != nil || !handled.OK() {
		res.Notes = append(res.Notes, "检测到原生对话框，但自动处理失败，请人工确认页面状态")
		return res
	}

	retry, err := s.run(ctx, command, args)
	if err != nil {
		return res
	}
	retry.Notes = append([]string{fmt.Sprintf("[dialog auto-%sed]", action)}, retry.Notes...)
	return retry
}

func (s *Session) dialogLikely(ctx context.Context, res *Result) bool {
	if hasDialogKey(res.Stdout) {
		return true
	}
	if res.OK() {
		return false
	}
	// 命令失败：可能正是对话框冻结了页面线程，用 page-info 确认一次
	// （上游 page-info 会直接报 pending dialog，不会挂住）。
	info, err := s.run(ctx, "page-info", nil)
	if err != nil {
		return false
	}
	return info.OK() && hasDialogKey(info.Stdout)
}

// hasDialogKey 精确判断 stdout 是否为携带 dialog 字段的 JSON，避免误伤讨论对话框的页面文本。
func hasDialogKey(stdout string) bool {
	trimmed := strings.TrimSpace(stdout)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return false
	}
	_, ok := payload["dialog"]
	return ok
}

// latestTrace 读取上游为最近一次失败写的 trace（error.txt 摘要），让 LLM 能自诊。
func (s *Session) latestTrace() string {
	base := filepath.Join(s.dir, ".browser-control", "traces")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	// 目录名是毫秒时间戳，字典序即时间序
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	data, err := os.ReadFile(filepath.Join(base, names[len(names)-1], "error.txt"))
	if err != nil {
		return ""
	}
	return Clip(strings.TrimSpace(string(data)), traceExcerptLimit)
}

// Release 归还工作区：只标记空闲时间，不关浏览器（下一个委派还要复用）。
func (s *Session) Release() {
	s.mgr.touch()
	slog.Debug("browser session released", "dir", s.dir)
}
