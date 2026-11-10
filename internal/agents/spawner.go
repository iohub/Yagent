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

// 本文件实现 plasma.Spawner（P8）：把一次生成跑成一个**同步**子进程。
//
// 为什么是同步而不是常驻连接：见设计稿 §3.5 —— 常驻连接要动 internal/mcp 的 readLoop
// （已有两条死锁纪律）、要重连逻辑与协议版本协商；trampoline 的全部代价只是一次
// ~300ms 的进程往返，而它落在秒级的 LLM 调用之间。
//
// 三条带牙齿的：
//  1. cmd.WaitDelay 必须有（坑 46）：子进程的**子孙**持有 stdout 管道时，cmd.Wait 会一直
//     等 EOF，只加 context.WithTimeout 的看门狗等于不存在。
//  2. exit 4 是正常信号 ⇒ 返回 (4, nil)；其余非 0 ⇒ (code, *plasma.SpawnError)，
//     stderr 截断到 2KB 随 error 走（不把 traceback 全量塞进 tool 结果）。
//  3. ctx 结束优先于退出码判定：被杀的那一刻 *exec.ExitError 可能带 -1 或 137，
//     把它当"业务失败"会让调用方以为是 bundle 生成错了。

// ExecSpawner 用 os/exec 跑 `python3 plasma.pyz <args> --workdir <W>`。
//
// P8 阶段还没有真 zipapp（P10 才产；现在生产跑 dist/bin/plasma.pyz）：测试把 Pkg 指向
// 自己写的 shell 脚本、Python 指向
// /bin/sh，于是契约（argv 形状、exit code、workdir 标志）能独立于 Python 实现被测死。
type ExecSpawner struct {
	// Python 是解释器路径。空 = PATH 上的 python3（ADR-15：缺解释器 ⇒ 能力不可用，
	// 不保留 Go 兜底）。
	Python string
	// Pkg 是 plasma.pyz 的路径。空 ⇒ 直接报错（不猜默认位置：猜错的表现是
	// "每次委派都 exit 1"，而原因与 package 路径无关）。
	Pkg string
	// WorkdirFlag 默认 "--workdir"。
	WorkdirFlag string
	// KillGrace 是 cmd.WaitDelay：kill 之后最多再等它这么久关闭 IO（坑 46）。
	// 默认 2s；测试可调小以缩短用例耗时。
	KillGrace time.Duration
	// StderrLimit 默认 plasma.SpawnStderrLimit。
	StderrLimit int
	// StdoutLimit 默认 8KB：stdout 只用于日志/诊断的回显，不是数据通道。
	StdoutLimit int
}

// 编译期确认：接口漂移在 build 阶段就红。
var _ plasma.Spawner = (*ExecSpawner)(nil)

const (
	defaultKillGrace   = 2 * time.Second
	defaultStdoutLimit = 8 * 1024
)

// Spawn 同步跑一次子进程并返回退出码。
func (s *ExecSpawner) Spawn(ctx context.Context, workdir string, args []string) (int, error) {
	pkg := strings.TrimSpace(s.Pkg)
	if pkg == "" {
		return 0, errors.New("plasma: 未配置 zipapp 路径，子进程无法启动")
	}
	python := strings.TrimSpace(s.Python)
	if python == "" {
		found, err := exec.LookPath("python3")
		if err != nil {
			// 找不到解释器是**能力缺失**（ADR-15）：返回 (0, err)，调用方据此降级，
			// 不要伪造一个退出码把"没装 python"说成"Python 跑失败了"。
			return 0, fmt.Errorf("plasma: PATH 上找不到 python3: %w", err)
		}
		python = found
	}
	if workdir == "" {
		return 0, errors.New("plasma: 空 workdir，拒绝启动子进程")
	}

	flag := s.WorkdirFlag
	if flag == "" {
		flag = "--workdir"
	}
	argv := make([]string, 0, 2+len(args)+2)
	argv = append(argv, pkg)
	argv = append(argv, args...)
	argv = append(argv, flag, workdir)

	cmd := exec.CommandContext(ctx, python, argv...)
	cmd.WaitDelay = s.killGrace()
	// 显式 Cancel：默认就是 Process.Kill，但写出来是为了让"kill 之后还有
	// WaitDelay 兜住管道"这件事在代码里可见，而不是靠读者知道 os/exec 的默认值。
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	stderr := newTailBuffer(s.stderrLimit())
	stdout := newTailBuffer(s.stdoutLimit())
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	if runErr == nil {
		return plasma.ExitOK, nil
	}

	// 顺序不能换：ctx 先判。被杀的子进程会带一个看起来像业务失败的退出码。
	if ctxErr := ctx.Err(); ctxErr != nil {
		code := plasma.ExitTimeout
		reason := "子进程超时被杀"
		if errors.Is(ctxErr, context.Canceled) {
			reason = "子进程因取消被杀"
		}
		spawnErr := plasma.NewSpawnError(code, workdir, stderr.Bytes())
		spawnErr.Stderr = strings.TrimSpace(reason + "\n" + spawnErr.Stderr)
		return code, spawnErr
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code := exitErr.ExitCode()
		if code == plasma.ExitSampling {
			// exit 4 是"我需要一次采样"，不是失败。返回 nil error 让驱动器的
			// switch 能把它当正常分支处理。
			return code, nil
		}
		return code, plasma.NewSpawnError(code, workdir, stderr.Bytes())
	}
	// 启动失败（解释器不存在、权限不足、pkg 不可执行……）：没有退出码可报。
	return 0, fmt.Errorf("plasma: 启动子进程失败: %w", runErr)
}

func (s *ExecSpawner) killGrace() time.Duration {
	if s.KillGrace > 0 {
		return s.KillGrace
	}
	return defaultKillGrace
}

func (s *ExecSpawner) stderrLimit() int {
	if s.StderrLimit > 0 {
		return s.StderrLimit
	}
	return plasma.SpawnStderrLimit
}

func (s *ExecSpawner) stdoutLimit() int {
	if s.StdoutLimit > 0 {
		return s.StdoutLimit
	}
	return defaultStdoutLimit
}

// tailBuffer 是**有界**的输出缓冲：保留最后 limit 字节。
//
// 为什么需要它：Python 的 traceback 与调试日志可以无限增长，而这段内容最终会进一条
// error/tool 结果。无界 buffer 的症状不是崩溃，是一条 tool 结果把上下文撑爆。
// 保留尾部而不是头部：出错原因在最后几行，头部通常只是命令行回显。
type tailBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func newTailBuffer(limit int) *tailBuffer {
	if limit <= 0 {
		limit = plasma.SpawnStderrLimit
	}
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		b.truncated = true
		return n, nil
	}
	if over := len(b.buf) + len(p) - b.limit; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
		b.truncated = true
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

// Bytes 返回尾部内容。被截断时加一行前缀，让读的人知道上面还有内容。
func (b *tailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.truncated {
		return append([]byte(nil), b.buf...)
	}
	out := make([]byte, 0, len(b.buf)+16)
	out = append(out, "...(truncated)\n"...)
	return append(out, b.buf...)
}
