package agents

import (
	"os"
	"testing"
)

// TestMain 把整个测试二进制的 $HOME 指向临时目录，隔离所有按 $HOME 定位的持久化根。
//
// 本包被测代码经 os.UserHomeDir() 定位 ~/.yagent 下的 rollout、blackboard、artifact、
// logging、datamanager 等目录。不隔离时每次 go test 都往生产目录写测试数据：rollout
// 目录实测已积累 2000+ 文件、其中数百个是 fullchain-mock / test-agent 残留，把 TUI
// /resume 的会话列表（按 mtime 倒序取前 200 条）挤满垃圾，真实会话选不到。
//
// 用 os.Setenv 而非 t.Setenv：本包 director_fullchain_test.go 有 7 个 t.Parallel()
// 用例，t.Setenv 与 t.Parallel 互斥会直接 panic；TestMain 在任何用例开始前执行，
// 不存在该冲突，且一处覆盖全包（含后续新增用例）。
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "yagent-agents-test-home-")
	if err != nil {
		os.Exit(1)
	}
	os.Setenv("HOME", home)

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}
