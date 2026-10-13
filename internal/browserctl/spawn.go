package browserctl

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// spawnHardCap 是单次命令读取的字节上限，超出即停止累积。
// 业务层的裁剪按 Config.MaxOutputBytes，这里的上限只为兜住失控输出占满内存。
const spawnHardCap = 1 << 20

// Spawner 执行一次外部命令并返回其输出。
// 抽成接口是为了在没有 browser-control 二进制、也没有 Chrome 的环境里，
// 完整测试 argv 构造、env 注入、工作目录与输出处理。
type Spawner interface {
	Spawn(ctx context.Context, opts SpawnOpts) (SpawnOutput, error)
}

type SpawnOpts struct {
	Name    string
	Args    []string
	Dir     string
	Env     []string
	Timeout time.Duration
}

type SpawnOutput struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	TimedOut  bool
}

// NewSpawner 返回默认的进程执行器。
func NewSpawner() Spawner { return execSpawner{} }

// execSpawner 是真实实现：子进程不经过 shell，argv 全部由调用方构造，
// 因此 LLM 提供的字符串不可能被解释成 shell 元字符。
type execSpawner struct{}

func (execSpawner) Spawn(ctx context.Context, opts SpawnOpts) (SpawnOutput, error) {
	runCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, opts.Name, opts.Args...)
	cmd.Dir = opts.Dir
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	}
	stdout := &cappedBuffer{max: spawnHardCap}
	stderr := &cappedBuffer{max: spawnHardCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()

	out := SpawnOutput{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.over || stderr.over,
	}
	if runErr == nil {
		return out, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		out.ExitCode = exitErr.ExitCode()
		// 只有"我们自己设的超时"才算命令超时；父 ctx 取消要向上传播为错误。
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			out.TimedOut = true
		}
		return out, nil
	}

	// 启动失败（二进制不存在、无执行权限）或父 ctx 取消
	return out, runErr
}

// cappedBuffer 累积输出到上限后停止写入，但继续接受调用方的写入以便判断是否被截断。
type cappedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - b.buf.Len()
	if room <= 0 {
		b.over = true
		return len(p), nil
	}
	if len(p) > room {
		b.buf.Write(p[:room])
		b.over = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) String() string { return b.buf.String() }
