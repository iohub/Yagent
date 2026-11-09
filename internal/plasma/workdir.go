package plasma

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/util"
)

// 本文件是 trampoline 的**跨语言契约面**（设计稿 §6.3 + ADR-14）。
//
// 两个方向都只有一种实现：
//   - Go 写 Python 读：req.json / manifest.json / llm_resp_NNN.json / diagnostics.json；
//   - Python 写 Go 读：state.json（Python 私有，Go 不碰）/ llm_req_NNN.json / bundle.json。
//
// 文件名与退出码都是契约的一部分：改一个名字必须同时改 Python zipapp（P10），
// 而两侧不同步的表现不是崩溃，是"子进程 exit 0 但 Go 找不到产物"。
//
// 叶子约束：只 import stdlib + util。子进程由 Spawner 注入（接口在这里声明、实现留在 agents）。

// 退出码契约（§6.3）。ExitSampling 是"我需要一次采样"的信号，不是协议错误：
// 它必须与 ExitFailed 分开，否则 Go 侧无法区分"Python 要 LLM"与"Python 挂了"。
const (
	ExitOK       = 0
	ExitFailed   = 1
	ExitUsage    = 2
	ExitTimeout  = 3
	ExitSampling = 4
)

// workdirPrefix 让启动期 GC 只认自己创建的目录（同一 base 下可能有别的临时目录）。
const workdirPrefix = "plasma-"

// 契约文件名。改这些常量要同时改 Python；它们是唯一的跨语言常量来源。
const (
	FileReq      = "req.json"
	FileManifest = "manifest.json"
	FileState    = "state.json"
	FileDiag     = "diagnostics.json"
	FileBundle   = "bundle.json"
)

// SpawnStderrLimit 是 SpawnError 里携带的 stderr 尾部上限。
// 不把 stderr 全量塞进 error：Python 的 traceback 可以有几十 KB，而它最终会进
// 一条 tool 结果/一条日志 —— 截断到 2KB 既保留"最后出错在哪"，又不会撑爆上下文。
const SpawnStderrLimit = 2048

// Request 是 req.json 的内容（Go 写、Python 只读）。
// Context 可空：不是每次委派都有额外上下文，宁可少一个键也不要写一个空串，
// 否则 Python 侧无法区分"没有上下文"与"上下文是空字符串"。
type Request struct {
	Task    string `json:"task"`
	Context string `json:"context,omitempty"`
}

// Workdir 是一次委派的唯一目录。并发两次 delegate_meta 得到两个不同目录（T8）。
type Workdir struct {
	root string
}

// NewWorkdir 在 base 下建一个 0700 的唯一目录。
//
// base 为空时用 os.TempDir()：调用方（装配层）应当显式传配置值，
// 但"没配"不该让整个生成能力消失 —— 与 workdir_ttl 的 0 语义分开（那个 0 = 不清理）。
func NewWorkdir(base string) (*Workdir, error) {
	if strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("plasma: 建 workdir 父目录 %s: %w", base, err)
	}
	root, err := os.MkdirTemp(base, workdirPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("plasma: 建 workdir: %w", err)
	}
	// MkdirTemp 的权限受 umask 影响（0700 &^ umask）；T8 要求 0700 是**事实**而不是
	// "通常如此"，所以显式 chmod 一次。
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("plasma: chmod workdir %s: %w", root, err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("plasma: workdir 绝对路径: %w", err)
	}
	return &Workdir{root: abs}, nil
}

// Path 返回目录绝对路径。nil 接收者返回空串（驱动器出错路径上仍要能安全格式化）。
func (w *Workdir) Path() string {
	if w == nil {
		return ""
	}
	return w.root
}

// File 把契约文件名拼成绝对路径。
func (w *Workdir) File(name string) string {
	if w == nil {
		return ""
	}
	return filepath.Join(w.root, name)
}

// LLMReq / LLMResp 按**序号**对齐，不靠时间戳、不靠扫描目录顺序（§6.3）。
// Python 与 Go 各自持有同一个 calls 计数器，所以两侧不需要共享任何状态。
func (w *Workdir) LLMReq(n int) string  { return w.File(fmt.Sprintf("llm_req_%03d.json", n)) }
func (w *Workdir) LLMResp(n int) string { return w.File(fmt.Sprintf("llm_resp_%03d.json", n)) }

// DiagnosticsPath 是 diagnostics.json 的路径（--fix 轮 Python 读它）。
func (w *Workdir) DiagnosticsPath() string { return w.File(FileDiag) }

// WriteReq 原子写 req.json。全部落盘走 util.AtomicWrite（I21：仓库唯一原子写内核）。
func (w *Workdir) WriteReq(task, contextNote string) error {
	return w.writeJSON(FileReq, &Request{Task: task, Context: contextNote})
}

// WriteManifest 原子写 manifest.json（Python 侧唯一的只读能力/码表来源）。
func (w *Workdir) WriteManifest(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("plasma: manifest 为 nil，拒绝写 %s", FileManifest)
	}
	return w.writeJSON(FileManifest, m)
}

// WriteDiagnostics 原子写编译诊断。Python 的 --fix 轮读它，所以键名必须是
// diag.go 里那套 json tag（小写），不能依赖结构体字段名。
func (w *Workdir) WriteDiagnostics(r *Report) error {
	if r == nil {
		return fmt.Errorf("plasma: report 为 nil，拒绝写 %s", FileDiag)
	}
	return w.writeJSON(FileDiag, r)
}

// ReadDiagnostics 读回诊断。Go 也读回做账：能在 Trace 里说清"上一轮为什么被拒"。
func (w *Workdir) ReadDiagnostics() (*Report, error) {
	raw, err := os.ReadFile(w.File(FileDiag))
	if err != nil {
		return nil, fmt.Errorf("plasma: 读 %s: %w", FileDiag, err)
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("plasma: %s 不是合法诊断 JSON: %w", FileDiag, err)
	}
	return &r, nil
}

// ReadBundleRaw 读 Python 写的产物。返回原始字节而不是 *Bundle：
// 解析是 Elaborate 的职责，plasma 的 Workdir 不该替它决定输入形态（最小 spec 或完整 IR）。
func (w *Workdir) ReadBundleRaw() ([]byte, error) {
	raw, err := os.ReadFile(w.File(FileBundle))
	if err != nil {
		return nil, fmt.Errorf("plasma: 读 %s: %w", FileBundle, err)
	}
	return raw, nil
}

// WriteBundleRaw 原子写产物。生产路径上由 Python 写；Go 侧提供它是为了
// 假 Python（测试）与调试重放，保持"两条路径写同一个契约"。
func (w *Workdir) WriteBundleRaw(raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("plasma: bundle 为空，拒绝写 %s", FileBundle)
	}
	return util.AtomicWrite(w.File(FileBundle), raw, util.AtomicWriteOptions{
		DirPerm:  0o700,
		FilePerm: 0o600,
		Label:    "plasma: write " + FileBundle,
	})
}

// WriteLLMResp 原子写一次采样响应（Go 侧唯一写点，序号由调用方的 calls 计数器给出）。
func (w *Workdir) WriteLLMResp(n int, raw []byte) error {
	if len(raw) == 0 {
		return fmt.Errorf("plasma: 采样响应为空，拒绝写 %s", filepath.Base(w.LLMResp(n)))
	}
	return util.AtomicWrite(w.LLMResp(n), raw, util.AtomicWriteOptions{
		DirPerm:  0o700,
		FilePerm: 0o600,
		Label:    "plasma: write llm_resp",
	})
}

// Cleanup 幂等删除整个目录（不存在不算错：正常路径 defer 与启动期 GC 可能各删一次）。
func (w *Workdir) Cleanup() error {
	if w == nil || w.root == "" {
		return nil
	}
	if err := os.RemoveAll(w.root); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("plasma: 清理 workdir %s: %w", w.root, err)
	}
	return nil
}

func (w *Workdir) writeJSON(name string, v interface{}) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("plasma: 序列化 %s: %w", name, err)
	}
	raw = append(raw, '\n')
	return util.AtomicWrite(w.File(name), raw, util.AtomicWriteOptions{
		DirPerm:  0o700,
		FilePerm: 0o600,
		Label:    "plasma: write " + name,
	})
}

// GCWorkdirs 清理 base 下超过 ttl 的孤儿目录。
//
// 只有 defer 是不够的：进程被 kill -9 时 defer 不跑，而每次 delegate_meta 都会
// 留下一个 0700 目录（T8 的补偿控制里点名要启动期 GC）。ttl <= 0 表示不清理 ——
// 调用方若把"没配"归一成 0，后果必须是"不清理"而不是"全清"。
func GCWorkdirs(base string, ttl time.Duration, now time.Time) (int, error) {
	if ttl <= 0 {
		return 0, nil
	}
	if strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("plasma: 扫 workdir 父目录 %s: %w", base, err)
	}
	cutoff := now.Add(-ttl)
	removed := 0
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), workdirPrefix) {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if rmErr := os.RemoveAll(filepath.Join(base, e.Name())); rmErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("plasma: 清理孤儿 workdir %s: %w", e.Name(), rmErr)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// Spawner 由 agents/app 侧实现（那边才 import os/exec）；plasma 只声明接口，
// 所以叶子约束不被打破（与 FixtureRunner 同一取向）。
//
// 返回 (exitCode, err) 的两条约定：
//   - exit 4（ExitSampling）是正常信号：实现应当返回 (4, nil)；
//   - 被超时/取消杀掉返回 (ExitTimeout, err)，err 非 nil —— 调用方要能区分
//     "业务失败"与"超时"，而 exit code 相同、error 为 nil 的形状做不到这一点。
type Spawner interface {
	Spawn(ctx context.Context, workdir string, args []string) (exitCode int, err error)
}

// SpawnError 是非 0 且非 4 的退出码。带"哪个码 + 哪份 stderr 尾部"，
// 让 CLI/工具输出能点名而不是回一句"子进程失败"。
type SpawnError struct {
	Code    int
	Workdir string
	Stderr  string
}

func (e *SpawnError) Error() string {
	if e == nil {
		return "plasma: 子进程失败"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plasma: 子进程退出码 %d（%s）", e.Code, ExitCodeName(e.Code))
	if e.Workdir != "" {
		fmt.Fprintf(&b, "；workdir %s", e.Workdir)
	}
	if s := strings.TrimSpace(e.Stderr); s != "" {
		fmt.Fprintf(&b, "；stderr 尾部：%s", s)
	}
	return b.String()
}

// NewSpawnError 按上限截断 stderr 后构造。保留**尾部**：编译错误与 traceback
// 的最后几行才是原因所在，头部通常只是命令行回显。
func NewSpawnError(code int, workdir string, stderr []byte) *SpawnError {
	return &SpawnError{Code: code, Workdir: workdir, Stderr: TruncateTail(string(stderr), SpawnStderrLimit)}
}

// ExitCodeName 给退出码一个人话名字，供错误信息与 CLI 输出共用。
func ExitCodeName(code int) string {
	switch code {
	case ExitOK:
		return "ok"
	case ExitFailed:
		return "业务失败"
	case ExitUsage:
		return "用法错"
	case ExitTimeout:
		return "超时被杀"
	case ExitSampling:
		return "需要 LLM 采样"
	default:
		return "未知"
	}
}

// TruncateTail 保留字符串尾部至多 limit 字节（按 rune 边界回退，避免切出半个 UTF-8）。
// 被截断时加一行说明，让读的人知道"上面还有内容"。
func TruncateTail(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := len(s) - limit
	for cut < len(s) && !utf8Start(s[cut]) {
		cut++
	}
	return "...(truncated)\n" + s[cut:]
}

// utf8Start 判断 b 是否是 UTF-8 字符的起始字节。
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
