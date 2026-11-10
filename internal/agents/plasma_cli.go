package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"yagent/internal/plasma"
)

// 本文件跑**离线一次性** Python 子命令（`evolve` / `author-fixtures` / `judge`）。
//
// 与 spawner.go 的 ExecSpawner 是两条路，不合并：
//   - ExecSpawner 跑的是 trampoline 的 `generate --workdir W`，契约是"退出码 + workdir 里的文件"，
//     stdout 只当日志（有界丢弃）；
//   - 这里跑的是批处理命令，契约是"退出码 + **stdout 上的一份 JSON**"，没有 workdir。
//
// 合并两者就得给 Spawner 接口加一个"要不要 --workdir""stdout 收不收"的开关，
// 而那两个开关一旦在某个调用点默认错，症状分别是"Python 说参数不认识"与
// "读到空产物"—— 两条都很难从错误信息里看出来。
//
// 纪律与 ExecSpawner 逐条对齐：ctx 先判（被杀的子进程会带一个像业务失败的退出码）、
// stderr 截尾（traceback 可达几十 KB，而它最终会进一条 error/日志）。

// PlasmaCLI 是离线 Python 命令的执行器。
type PlasmaCLI struct {
	// Python 是解释器路径。空 = PATH 上的 python3。
	Python string
	// Pkg 是 plasma.pyz 的路径。空 ⇒ 报错（不猜默认位置：猜错的表现是"每次命令都 exit 1"）。
	Pkg string
	// KillGrace 是 cmd.WaitDelay：kill 之后最多再等它这么久关闭 IO（坑 46）。
	KillGrace time.Duration
	// StdoutLimit 是 stdout 的上限，默认 8MB —— 它承载一份候选/评分 JSON，
	// 但仍是**有界**的：无界 buffer 的症状是把一次异常输出变成一次 OOM。
	StdoutLimit int
	// StderrLimit 默认 plasma.SpawnStderrLimit（2KB）。
	StderrLimit int
}

const defaultCLIStdoutLimit = 8 << 20

// Run 跑一次子进程，返回 stdout 全部字节。
//
// 退出码非 0 一律翻成 *plasma.SpawnError：调用方要靠 Code 区分"Python 说参数不对"（2）
// 与"业务失败"（1），而只给一个 error 字符串的话，那两种的修法完全不同。
func (c *PlasmaCLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	pkg := strings.TrimSpace(c.Pkg)
	if pkg == "" {
		return nil, errors.New("plasma: 未配置 zipapp 路径，离线子命令无法启动")
	}
	python := strings.TrimSpace(c.Python)
	if python == "" {
		found, err := exec.LookPath("python3")
		if err != nil {
			return nil, fmt.Errorf("plasma: PATH 上找不到 python3: %w", err)
		}
		python = found
	}

	cut := defaultCLIStdoutLimit
	if c.StdoutLimit > 0 {
		cut = c.StdoutLimit
	}
	limit := plasma.SpawnStderrLimit
	if c.StderrLimit > 0 {
		limit = c.StderrLimit
	}

	cmd := exec.CommandContext(ctx, python, append([]string{pkg}, args...)...)
	cmd.WaitDelay = c.killGrace()
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	stdout := &cappedBuffer{limit: cut}
	stderr := newTailBuffer(limit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		// 顺序不能换：ctx 先判。被杀的子进程的退出码看起来像业务失败。
		reason := "子进程超时被杀"
		if errors.Is(ctxErr, context.Canceled) {
			reason = "子进程因取消被杀"
		}
		spawnErr := plasma.NewSpawnError(plasma.ExitTimeout, pkg, stderr.Bytes())
		spawnErr.Stderr = strings.TrimSpace(reason + "\n" + spawnErr.Stderr)
		return stdout.Bytes(), spawnErr
	}
	if runErr == nil {
		return stdout.Bytes(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return stdout.Bytes(), plasma.NewSpawnError(exitErr.ExitCode(), pkg, stderr.Bytes())
	}
	return stdout.Bytes(), fmt.Errorf("plasma: 启动子进程失败: %w", runErr)
}

func (c *PlasmaCLI) killGrace() time.Duration {
	if c.KillGrace > 0 {
		return c.KillGrace
	}
	return defaultKillGrace
}

// cappedBuffer 是**有界**的 stdout 缓冲：超过上限后继续接收但不再保存，
// 并把"被截断"这件事记下来（返回时加一行提示）。
//
// 为什么不用 bytes.Buffer：子进程可以无限写，而这段内容会整段进内存。
// 与 tailBuffer 的差别在于保留头部 —— stdout 是数据通道，JSON 的开头才是结构所在。
type cappedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - len(b.buf)
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]byte(nil), b.buf...)
	if b.truncated {
		out = append(out, "\n...(stdout 被截断)"...)
	}
	return out
}
