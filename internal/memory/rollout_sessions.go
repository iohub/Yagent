package memory

// rollout_sessions.go — rollout 会话文件清单与元信息（读侧辅助）。
//
// 职责：
//   - 列举 rollout 根目录下的会话文件（深度≤2：root/<projectID>/<file>.jsonl），
//     按 mtime 倒序返回，并做 head-scan 提取身份与会话预览；
//   - 身份优先取文件头 session_meta，缺失时回退文件名解析（与 writer 命名规则对应）。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ─── 常量 ───

const (
	rolloutFileExt                 = ".jsonl"
	rolloutHeadScanMaxBytes        = 256 * 1024 // head-scan 字节上限
	rolloutHeadScanMaxLines        = 300        // head-scan 行数上限，先到者停
	rolloutPreviewMaxRunes         = 100        // Preview 截断长度（rune）
	rolloutSessionListDefaultLimit = 200        // limit<=0 时的默认返回条数
)

// ─── 会话信息 ───

// RolloutSessionInfo 单个 rollout 会话文件的清单条目。
type RolloutSessionInfo struct {
	Path      string // 文件绝对路径（只读，不修改）
	ProjectID string // 所属 projectID（root 直下文件为空）
	SessionID string // 优先 meta.session_id，回退文件名解析
	AgentName string // 仅文件名带 agent 段时非空（sub-agent 文件）
	CWD       string // 首个 session_meta.cwd / 首个 turn_context.cwd
	Model     string // 首个 turn_context.model
	GitBranch string // 首个 session_meta.git.branch
	Preview   string // 首条 user 消息文本截 100 rune
	StartedAt time.Time
	ModTime   time.Time
	SizeBytes int64
}

// ListRolloutSessions 列举 rollout 会话文件。
//
// root 为 rollout 根目录（生产代码传 RolloutRootDir()）。filepath.WalkDir 收集
// 深度≤2 的 *.jsonl（忽略符号链接/权限错误/非 jsonl），os.Stat 后按 mtime 倒序，
// 取前 limit 条（limit<=0 用默认 200），每文件 head-scan（≤256KB 或前 300 行，
// 先到者停）提取元信息。扫描失败的坏文件不跳过：保留文件名回退身份并返回
//（仅当文件本身打不开导致 Stat 不可得时才从结果中缺席）。
func ListRolloutSessions(root string, limit int) ([]RolloutSessionInfo, error) {
	if limit <= 0 {
		limit = rolloutSessionListDefaultLimit
	}
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("rollout sessions: %w", err)
	}

	type fileMeta struct {
		path      string
		projectID string
		modTime   time.Time
		size      int64
	}
	var files []fileMeta

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 权限/删除竞态等：跳过该子树或文件
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return fs.SkipDir
			}
			if strings.Count(rel, string(filepath.Separator)) >= 2 {
				return fs.SkipDir // 更深目录整树跳过（文件最多取深度 2）
			}
			return nil
		}
		if !d.Type().IsRegular() { // 符号链接/设备等：忽略
			return nil
		}
		if filepath.Ext(path) != rolloutFileExt { // 非 jsonl：忽略
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if depth > 2 {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		projectID := ""
		if depth == 2 {
			projectID = strings.SplitN(rel, string(filepath.Separator), 2)[0]
		}
		files = append(files, fileMeta{
			path:      path,
			projectID: projectID,
			modTime:   info.ModTime(),
			size:      info.Size(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("rollout sessions: walk %s: %w", root, walkErr)
	}

	// mtime 倒序（新在前），截 limit。
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path // 同 mtime 稳定排序保障确定性
		}
		return files[i].modTime.After(files[j].modTime)
	})
	if len(files) > limit {
		files = files[:limit]
	}

	result := make([]RolloutSessionInfo, 0, len(files))
	for _, fm := range files {
		si := headScanRolloutFile(fm.path, fm.projectID, fm.modTime, fm.size)
		if si == nil {
			continue // 打不开（如已被删除）：跳过
		}
		result = append(result, *si)
	}
	return result, nil
}

// ─── head-scan ───

// rolloutHeadScanState head-scan 目标收集状态。
type rolloutHeadScanState struct {
	metaSeen    bool
	turnSeen    bool
	previewDone bool
}

// complete 是否三个提取目标均已拿到（可提前终止扫描）。
func (s *rolloutHeadScanState) complete() bool {
	return s.metaSeen && s.turnSeen && s.previewDone
}

// headScanRolloutFile 对单个会话文件做 head-scan（只读）。
// 文件打不开返回 nil；内容坏行容忍：身份回退文件名解析结果。
func headScanRolloutFile(path, projectID string, modTime time.Time, size int64) *RolloutSessionInfo {
	si := &RolloutSessionInfo{
		Path:      path,
		ProjectID: projectID,
		ModTime:   modTime,
		SizeBytes: size,
	}

	// 身份回退：文件名解析（writer 命名形状：<date>_<time>_<session...>[_<agentName>].jsonl）。
	stem := strings.TrimSuffix(filepath.Base(path), rolloutFileExt)
	si.SessionID, si.AgentName = parseRolloutFilename(stem)

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	var scanned int64
	var state rolloutHeadScanState
	for lineNo := 0; lineNo < rolloutHeadScanMaxLines && scanned < rolloutHeadScanMaxBytes; lineNo++ {
		raw, readErr := reader.ReadBytes('\n')
		scanned += int64(len(raw))
		if len(raw) > 0 {
			si.headScanLine(raw, &state)
		}
		if readErr != nil {
			break // EOF 或 IO 错误
		}
		if state.complete() {
			break
		}
	}
	if si.StartedAt.IsZero() {
		si.StartedAt = modTime
	}
	return si
}

// headScanLine 解析一行，按序提取首个 session_meta / 首个 turn_context 的 model/cwd /
// 首条 user 消息 preview / 首个 envelope 时间戳。坏行静默忽略。
func (si *RolloutSessionInfo) headScanLine(raw []byte, state *rolloutHeadScanState) {
	line := trimJSONLRaw(raw)
	if len(line) == 0 {
		return
	}
	var env rawEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return // 坏行容忍
	}
	if si.StartedAt.IsZero() {
		if ts, ok := parseRolloutTimestamp(env.Timestamp); ok {
			si.StartedAt = ts
		}
	}
	switch env.Type {
	case "session_meta":
		if state.metaSeen {
			return // 首个为基准
		}
		var meta SessionMeta
		if err := json.Unmarshal(env.Payload, &meta); err != nil {
			return
		}
		state.metaSeen = true
		if id := rolloutFirstNonEmpty(meta.SessionID, meta.ID); id != "" {
			si.SessionID = id
		}
		if meta.Cwd != "" {
			si.CWD = meta.Cwd
		}
		if meta.Git.Branch != "" {
			si.GitBranch = meta.Git.Branch
		}
	case "turn_context":
		if state.turnSeen {
			return // 首个为基准（与 reader 的"最新"语义不同：清单追求首回合身份）
		}
		var tc TurnContext
		if err := json.Unmarshal(env.Payload, &tc); err != nil {
			return
		}
		state.turnSeen = true
		if tc.Model != "" {
			si.Model = tc.Model
		}
		if si.CWD == "" && tc.Cwd != "" { // meta 缺 cwd 时回退 turn_context
			si.CWD = tc.Cwd
		}
	case "response_item":
		if state.previewDone {
			return
		}
		var item ResponseItem
		if err := json.Unmarshal(env.Payload, &item); err != nil {
			return
		}
		if item.Type == "message" && item.Role == "user" {
			state.previewDone = true
			si.Preview = truncateRunes(rolloutExtractText(item.Content, "input_text"), rolloutPreviewMaxRunes)
		}
	}
}

// ─── 文件名解析（writer 命名规则的读侧镜像） ───

// parseRolloutFilename 从 rollout 文件名 stem 解析身份（回退路径）。
//
// writer 生成形状：<date>_<time>_<sessionID>[_<agentName>].jsonl，其中
// <date>_<time> 为文件创建时间戳（2 段），<sessionID> 自身为 <date>_<time>_<hex>
//（3 段）。故按 "_" 分段后：
//   - len>=6：前 2 段文件时间戳 + 3 段 sessionID + 剩余段 join 为 agentName；
//   - 3<=len<6：前 2 段为文件时间戳，其余段整体为 sessionID（无 agentName）；
//   - len<3（畸形文件名）：整段当 sessionID。
func parseRolloutFilename(stem string) (sessionID, agentName string) {
	seg := strings.Split(stem, "_")
	switch {
	case len(seg) >= 6:
		return strings.Join(seg[2:5], "_"), strings.Join(seg[5:], "_")
	case len(seg) >= 3:
		return strings.Join(seg[2:], "_"), ""
	default:
		return stem, ""
	}
}

// ─── 工具 ───

// truncateRunes 按 rune 数截断字符串（无标记、超长时截断）。
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// trimJSONLRaw 去除行尾 \r\n 与首尾空白。
func trimJSONLRaw(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}
