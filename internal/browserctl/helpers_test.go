package browserctl

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeSpawner 记录每次调用的 argv/Dir/Env 并回放预设输出，
// 让参数构造、env 注入、输出处理都能在没有真实二进制与 Chrome 的环境下验证。
type fakeSpawner struct {
	mu      sync.Mutex
	calls   []SpawnOpts
	handler func(opts SpawnOpts) (SpawnOutput, error)
}

func (f *fakeSpawner) Spawn(ctx context.Context, opts SpawnOpts) (SpawnOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, opts)
	h := f.handler
	f.mu.Unlock()

	if ctx.Err() != nil {
		return SpawnOutput{}, ctx.Err()
	}
	if h != nil {
		return h(opts)
	}
	return SpawnOutput{}, nil
}

func (f *fakeSpawner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSpawner) callAt(i int) SpawnOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func (f *fakeSpawner) lastCall() SpawnOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return SpawnOpts{}
	}
	return f.calls[len(f.calls)-1]
}

// okOutput 构造一次成功调用：exit 0，stdout 为给定文本。
func okOutput(stdout string) SpawnOutput {
	return SpawnOutput{Stdout: stdout, ExitCode: 0}
}

// failOutput 构造一次失败调用。
func failOutput(stderr string) SpawnOutput {
	return SpawnOutput{Stderr: stderr, ExitCode: 1}
}

// installFakeBinary 在临时 HOME 下放一个可执行的假 browser-control，
// 让 ResolveBinaryPath 的 ~/.yagent/bin 分支命中，且不依赖真实安装。
func installFakeBinary(t *testing.T, home string) string {
	t.Helper()
	binDir := filepath.Join(home, ".yagent", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	path := filepath.Join(binDir, BinaryName)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

// newTestManager 构造一个探测必然成功、工作区落在临时目录的 Manager。
func newTestManager(t *testing.T, mutate func(*Config)) (*Manager, *fakeSpawner) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	installFakeBinary(t, home)

	cfg := DefaultConfig()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.Mode = ModeAttach
	cfg.CDPURL = "http://127.0.0.1:9222"
	if mutate != nil {
		mutate(&cfg)
	}

	fake := &fakeSpawner{handler: func(opts SpawnOpts) (SpawnOutput, error) {
		return okOutput("ok\n"), nil
	}}
	return NewManager(cfg, WithSpawner(fake)), fake
}
