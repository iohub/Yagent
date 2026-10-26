package tools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 这一组用例钉同一条不变量：前台 run_bash 的等待必须有界，而且回执要说清这次是怎么结束的。
// 背景见 docs/run-bash-hang-fix.md 与 internal/tools/AGENTS.md 硬约束 16。

func restoreRunBashWaitDelay(t *testing.T, d time.Duration) {
	t.Helper()
	old := runBashWaitDelay
	runBashWaitDelay = d
	t.Cleanup(func() { runBashWaitDelay = old })
}

func restoreShellOutputCap(t *testing.T, n int) {
	t.Helper()
	old := maxShellOutputBytes
	maxShellOutputBytes = n
	t.Cleanup(func() { maxShellOutputBytes = old })
}

// callRunBash 把结果取成 map；Go error 这条路对本工具不存在（失败也是 success=false 的回执）。
func callRunBash(t *testing.T, ctx context.Context, dir, command string) map[string]interface{} {
	t.Helper()
	res, err := NewSystemOperationsTool(dir).ExecuteRunBash(ctx, map[string]interface{}{
		"command":     command,
		"explanation": "test",
	})
	if err != nil {
		t.Fatalf("ExecuteRunBash(%q) 返回 Go error: %v", command, err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("结果不是 map：%T", res)
	}
	return m
}

// TestExecuteRunBash_GrandchildHoldingPipeReturns 钉的就是那次事故：
// `sleep … &` 让后台子 shell 继续握着输出管道，直接子进程却早就退了，
// 于是 ctx 超时与取消都触发不了 exec 的看门狗。只有 WaitDelay 解得开。
func TestExecuteRunBash_GrandchildHoldingPipeReturns(t *testing.T) {
	restoreRunBashWaitDelay(t, 150*time.Millisecond)

	// deadline 远大于 WaitDelay：证明收回来的是 WaitDelay，不是超时。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type outcome struct {
		res map[string]interface{}
		err error
	}
	// t.TempDir 必须在测试 goroutine 里取（它内部会 t.Fatalf）。
	dir := t.TempDir()
	done := make(chan outcome, 1)
	go func() {
		res, err := NewSystemOperationsTool(dir).ExecuteRunBash(ctx, map[string]interface{}{
			"command":     `sleep 30 & echo "PID=$!"`,
			"explanation": "test",
		})
		m, _ := res.(map[string]interface{})
		done <- outcome{res: m, err: err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒还没有返回：等待又变成无界的了，这正是永久卡死的形状")
	}
	if got.err != nil {
		t.Fatalf("ExecuteRunBash 返回 Go error: %v", got.err)
	}
	res := got.res
	if res == nil {
		t.Fatal("结果不是 map")
	}

	output, _ := res["output"].(string)
	if !strings.HasPrefix(output, "PID=") {
		t.Fatalf("输出里没有后台进程的 PID：%q —— 命令明明执行成功了", output)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(output, "PID=")))
	if err != nil {
		t.Fatalf("PID 解析失败：%q", output)
	}
	// 那个 sleep 不归生产代码管（命令已经"成功"了），所以由本测试收尾。
	t.Cleanup(func() { terminateProcess(t, pid) })

	if res["success"] != true {
		t.Errorf("success = %v，期望 true（exit code 0 的 `&` 调用不该算失败）：%q", res["success"], output)
	}
	if code, ok := res["exit_code"].(int); !ok || code != 0 {
		t.Errorf("exit_code = %v，期望真实的 0（ProcessState 在这条路上是有效的）", res["exit_code"])
	}
	if note, ok := res["note"].(string); !ok || !strings.Contains(note, "is_background=true") {
		t.Errorf("note = %v —— 没有告诉模型这是 `&` 的形状、下次该用 is_background", res["note"])
	}
}

func TestExecuteRunBash_TimeoutReceiptNamesTheReason(t *testing.T) {
	restoreRunBashWaitDelay(t, 100*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	res := callRunBash(t, ctx, t.TempDir(), "sleep 30")
	if res["success"] == true {
		t.Errorf("超时的调用回报 success=true：%v", res)
	}
	e, _ := res["error"].(string)
	if !strings.Contains(e, "timed out") {
		t.Errorf("回执没有点名超时：%q", e)
	}
	// 这条才是真正的闸门：把裸 err.Error() 交回模型的话，它看到的是一句 Go 内部话术。
	if strings.Contains(e, "signal: killed") {
		t.Errorf("回执把\"我们把它杀了\"原样交给模型：%q", e)
	}
}

func TestExecuteRunBash_CancelReceiptSaysCancelled(t *testing.T) {
	restoreRunBashWaitDelay(t, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	// 取消必须在命令跑起来之后发生，否则测的是"没开始就被取消"。
	stop := time.AfterFunc(80*time.Millisecond, cancel)
	defer stop.Stop()

	res := callRunBash(t, ctx, t.TempDir(), "sleep 30")
	if res["success"] == true {
		t.Errorf("被取消的调用回报 success=true：%v", res)
	}
	e, _ := res["error"].(string)
	if !strings.Contains(e, "cancelled") {
		t.Errorf("回执没有点名取消：%q", e)
	}
	if strings.Contains(e, "timed out") {
		t.Errorf("把取消说成超时：模型对这两件事的下一步动作不同 —— %q", e)
	}
}

// TestExecuteRunBash_OutputCapKeepsDraining 钉 capped writer 的第二半：
// 到顶之后必须继续把管道排空。返回错误会让 io.Copy 收手，孩子下一次写就收 SIGPIPE，
// 于是"输出太大"变成"命令失败"。
func TestExecuteRunBash_OutputCapKeepsDraining(t *testing.T) {
	restoreRunBashWaitDelay(t, 100*time.Millisecond)
	restoreShellOutputCap(t, 4096)

	res := callRunBash(t, context.Background(), t.TempDir(), `head -c 9000 /dev/zero | tr '\0' 'x'`)
	output, _ := res["output"].(string)
	if len(output) > 4096 {
		t.Errorf("累积了 %d 字节，上限是 4096", len(output))
	}
	if res["truncated"] != true {
		t.Errorf("截断没有被记账：truncated = %v", res["truncated"])
	}
	if res["success"] != true {
		t.Errorf("success = %v（管道停止排空时孩子会收 SIGPIPE，退出码就不再是 0）：%v",
			res["success"], res["error"])
	}
}

// pidsFromDir 读出命令自己写的 pid 清单，并给每一个挂上兜底清理。
func pidsFromDir(t *testing.T, dir string) []int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "pids"))
	if err != nil {
		t.Fatalf("读不到孩子进程的 pid 清单：%v —— 命令没跑到写文件那一步，本用例没测到杀进程组", err)
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("pid 清单里有非数字项 %q: %v", field, err)
		}
		pids = append(pids, pid)
		// 断言失败时不许把子孙留在机器上（kill 对已退出的进程是无害的 ESRCH）。
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	}
	if len(pids) == 0 {
		t.Fatal("pid 清单是空的 —— 本用例没有被测进程")
	}
	return pids
}

// TestExecuteRunBash_TimeoutKillsWholeProcessGroup 钉杀组：exec 默认的取消只打得到
// 直接子进程 bash，`make`/`node` 这些子孙会留下占端口的孤儿。
func TestExecuteRunBash_TimeoutKillsWholeProcessGroup(t *testing.T) {
	restoreRunBashWaitDelay(t, 200*time.Millisecond)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	// 直接子进程卡在 wait 上（它自己不会退），所以结束只能来自超时之后的杀组。
	res := callRunBash(t, ctx, dir, `sleep 30 & echo $! > pids; sleep 30 & echo $! >> pids; wait`)
	if res["success"] == true {
		t.Errorf("被超时的调用回报 success=true：%v", res)
	}

	for _, pid := range pidsFromDir(t, dir) {
		deadline := time.Now().Add(2 * time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond) // 等它从进程表里消失（回收是 init 的事，不是我们的）
		}
		if syscall.Kill(pid, 0) == nil {
			t.Errorf("pid %d 还活着：取消只杀到了 bash，子孙留在机器上占着端口", pid)
		}
	}
}

// TestExecuteRunBash_ReturnedCallSurvivesLaterCancel 是上一条方向相反的闸门：
// 调用**已经返回**之后 executor 会立刻取消那个 toolCtx（agents/executor.go:154-155），
// 这一次取消不许把刚放行的后台进程杀掉 —— 否则 `nohup node server &` 就永远留不下服务。
func TestExecuteRunBash_ReturnedCallSurvivesLaterCancel(t *testing.T) {
	restoreRunBashWaitDelay(t, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 兜底；真正的断言在下面这一次显式 cancel

	dir := t.TempDir()
	res := callRunBash(t, ctx, dir, `sleep 30 & echo $! > pids; echo "PID=$(cat pids)"`)
	output, _ := res["output"].(string)
	if !strings.HasPrefix(output, "PID=") {
		t.Fatalf("输出里没有后台进程的 PID：%q", output)
	}

	pid := pidsFromDir(t, dir)[0]
	cancel()

	// 这里没有事件可等，所以是一段有界的"必须仍然活着"窗口：
	// 把杀组挪到返回之后（比如塞进 defer）就会在窗口里把它打死。
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			t.Fatal("取消之后后台进程没了：杀组跑到了调用返回之后，故意 & 出去的服务永远留不下来")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func liveGroupCount(t *testing.T) int {
	t.Helper()
	liveGroupsMu.Lock()
	defer liveGroupsMu.Unlock()
	return len(liveGroups)
}

// TestKillLiveProcessGroupsEndsInFlightCall 钉硬退出那条路：os.Exit 跳过所有 defer，
// 登记表是杀掉在飞子进程组的唯一机会。表空了、或者登记只在某一条分支上，
// 表现都是"用户按了两次 Ctrl+C，机器上留下一堆占端口的进程"。
func TestKillLiveProcessGroupsEndsInFlightCall(t *testing.T) {
	restoreRunBashWaitDelay(t, 200*time.Millisecond)
	dir := t.TempDir()

	// deadline 故意放到 30 秒：本用例要证明结束调用的是杀组，不是超时。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan map[string]interface{}, 1)
	go func() {
		res, err := NewSystemOperationsTool(dir).ExecuteRunBash(ctx, map[string]interface{}{
			"command":     `sleep 30 & echo $! > pids; wait`,
			"explanation": "test",
		})
		if err != nil {
			done <- nil
			return
		}
		m, _ := res.(map[string]interface{})
		done <- m
	}()

	deadline := time.Now().Add(2 * time.Second)
	for liveGroupCount(t) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if liveGroupCount(t) == 0 {
		t.Fatal("在飞的前台调用没登记进进程组表：硬退出时无组可杀")
	}

	KillLiveProcessGroups()

	var res map[string]interface{}
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("杀组之后调用没有返回")
	}
	if res["success"] == true {
		t.Errorf("被硬退出杀掉的调用回报 success=true：%v", res)
	}
	for _, pid := range pidsFromDir(t, dir) {
		if syscall.Kill(pid, 0) == nil {
			t.Errorf("pid %d 还活着：KillLiveProcessGroups 没打到整个组", pid)
		}
	}
	if n := liveGroupCount(t); n != 0 {
		t.Errorf("返回之后表里还剩 %d 项：注销漏了", n)
	}
}
