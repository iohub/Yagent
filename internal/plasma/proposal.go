package plasma

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"yagent/internal/util"
)

// 本文件是"内建 Agent prompt 提案"的落盘契约在 Go 侧的唯一实现。
//
// 写侧是 Python（`plasma/propose.py`），读侧与判决侧是 Go（TUI 的 `/proposals`，
// 以及将来可能的 CLI）。提案目录由调用方传入：本包是 leaf 包
// （`scripts/check_plasma_leaf.sh`），不认识 `[plasma] proposal_dir` 配置。
//
// **本文件永不打开、永不解引用任何 `*.prompt.md`。** 内建 Agent 的 prompt 与 Go 代码是
// "改行为要同时改两处"的耦合（根 AGENTS.md §4、§6.9），自动 apply 会把两者改成不一致；
// 所以审批只改提案 JSON 里那个 `status` 字段，其余字节一律不动 —— 包括 `prompt_path`
// 指向的文件（它只在面板里当文本显示）。

// 提案状态只有三个值，与 plasma/propose.py 的常量逐字同值。
// pending 只会由 Python 产出；approved / rejected 只会由这里的判决写出来。
const (
	ProposalStatusPending  = "pending"
	ProposalStatusApproved = "approved"
	ProposalStatusRejected = "rejected"
)

// ErrProposalNotFound 表示目录里没有这个 id 的提案。
// 单列哨兵的理由同 ErrNoReport：不存在（用户拼错/已被清理）与读不动（权限/坏 JSON）
// 的修法完全不同，混成一句"读不到"会让人反复重试一个永远不会成功的事。
var ErrProposalNotFound = errors.New("plasma: 提案不存在")

// Proposal 是 propose.py 落盘的结构化提案。
//
// 字段与 Python 侧逐字对应（含 `schema_version`）：Go 侧的读者只有这一个结构，
// 少一个字段的症状是"面板上那一栏永远是空的"，而没有任何一处会报错。
type Proposal struct {
	SchemaVersion  int      `json:"schema_version"`
	ID             string   `json:"id"`
	Agent          string   `json:"agent"`
	Status         string   `json:"status"`
	BaseHash       string   `json:"base_hash"`
	Rationale      string   `json:"rationale"`
	Diff           string   `json:"diff"`
	CreatedAt      string   `json:"created_at"`
	RuleIDs        []string `json:"rule_ids"`
	Evidence       []string `json:"evidence"`
	EditableFields []string `json:"editable_fields"`
	PromptPath     string   `json:"prompt_path"`
}

// ProposalEntry 是列出提案时的一行。坏文件**不中断列表**：
// 一个手改坏的 JSON 不该让另外九份待审批的提案在面板里一起消失
// （那正是"审批队列看起来是空的"这种最容易被忽略的静默）。
type ProposalEntry struct {
	// ID 是文件名去扩展名（坏文件也有 id，否则无法点名是哪一份坏了）。
	ID string
	// Proposal 只在解析成功时非 nil。
	Proposal *Proposal
	// Err 非 nil 表示这份提案读不出来（JSON 坏 / 读权限 / 不是普通文件）。
	Err error
}

// ListProposals 扫 dir 里的 `*.json` 提案，按 created_at 降序（新的在前）、id 升序返回。
//
// 目录不存在**不是**错误：还没跑过 `yagent plasma propose` 的机器上"还没有提案"
// 是正确答案（与 store 的空列表同一取向）。读不动的单个文件进 Err，不返回 error。
func ListProposals(dir string) ([]ProposalEntry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读提案目录 %s: %w", dir, err)
	}
	out := make([]ProposalEntry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if item.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		p, loadErr := loadProposalFile(filepath.Join(dir, name))
		if loadErr != nil {
			out = append(out, ProposalEntry{ID: id, Err: loadErr})
			continue
		}
		out = append(out, ProposalEntry{ID: id, Proposal: p})
	}
	// 排序键必须**可传递**：读不动的条目没有 created_at，若在同一个比较函数里按 id 兜底
	// （而不是先按 created_at）就会让比较变成非传递的 —— sort 的结果会随元素初始顺序变，
	// 那种"每次跑顺序都不一样"的列表最难发现。坏的一律排到最后（created="" 最小）。
	key := func(e ProposalEntry) (string, string) {
		created := ""
		if e.Proposal != nil {
			created = e.Proposal.CreatedAt
		}
		return created, e.ID
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, idi := key(out[i])
		cj, idj := key(out[j])
		if ci != cj {
			return ci > cj
		}
		return idi < idj
	})
	return out, nil
}

// LoadProposal 读一份提案。id 先过 validProposalID —— 用户输入直接拼路径是
// 最典型的目录穿越入口（`/proposals show ../../etc/passwd`）。
func LoadProposal(dir, id string) (*Proposal, error) {
	if err := validProposalID(id); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".json")
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", id, ErrProposalNotFound)
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	// 符号链接一律拒：approve 会**写**这个文件，跟随链接等于让一次审批写到别处去。
	// 提案由 propose.py 直接写普通文件，所以拒绝链接不影响任何正常路径。
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: 不是普通文件（mode=%s），拒绝读写", path, info.Mode())
	}
	return loadProposalFile(path)
}

func loadProposalFile(path string) (*Proposal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var p Proposal
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &p, nil
}

// SetProposalStatus 是审批的唯一写入口：把 `status` 从 pending 改成一个判决态。
//
// 三条纪律：
//  1. **只**改 `status`。实现按 `map[string]json.RawMessage` 改写，Python 侧将来新加的
//     字段会原样带过去（按 struct 回写会把它们全部丢掉，而丢掉的字段没有任何报错）；
//  2. 只允许 pending → approved|rejected。已判决的提案再改一次是"同一件事被记了两遍"，
//     允许它等于让终态可以来回翻（审计上无法回答"当初批的是什么"）；
//  3. 落盘走 util.AtomicWrite。半写的提案 JSON 会让列表读到一个坏文件。
//
// 返回判决后的提案（调用方用它回显 diff）。
func SetProposalStatus(dir, id, to string) (*Proposal, error) {
	if to != ProposalStatusApproved && to != ProposalStatusRejected {
		return nil, fmt.Errorf("提案判决只能是 %s / %s，收到 %q",
			ProposalStatusApproved, ProposalStatusRejected, to)
	}
	if err := validProposalID(id); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".json")
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", id, ErrProposalNotFound)
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: 不是普通文件（mode=%s），拒绝读写", path, info.Mode())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// 改写用 map[string]json.RawMessage：未知字段原样保留，只有 status 一个键被换掉。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var current string
	if err := json.Unmarshal(doc["status"], &current); err != nil {
		return nil, fmt.Errorf("%s: 提案缺少合法的 status 字段（%v）", path, err)
	}
	if current != ProposalStatusPending {
		return nil, fmt.Errorf("%s: 当前 status=%s，只有 %s 的提案可以判决（判决是终态，不覆盖）",
			id, current, ProposalStatusPending)
	}
	encoded, err := json.Marshal(to)
	if err != nil {
		return nil, fmt.Errorf("encode status: %w", err)
	}
	doc["status"] = encoded

	// 缩进 2 空格 + 结尾换行：与 propose.py 的 _write_text 同形，人看 diff 时不抖。
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	out = append(out, '\n')
	if err := util.AtomicWrite(path, out, util.AtomicWriteOptions{
		DirPerm: 0o700, FilePerm: 0o600, Label: "plasma/proposal",
	}); err != nil {
		return nil, err
	}
	var updated Proposal
	if err := json.Unmarshal(out, &updated); err != nil {
		return nil, fmt.Errorf("parse %s（刚写完）: %w", path, err)
	}
	return &updated, nil
}

// validProposalID 按 allowlist 收窄 id：propose.py 的 id 形态是 `<agent>-<ts>`，
// 只含字母、数字、`-`、`_`、`.`。空串 / 路径分隔符 / 以 `.` 开头 一律拒 ——
// `..`、`../x`、`/abs` 都是"读/写别的地方"的入口。
func validProposalID(id string) error {
	if id == "" {
		return errors.New("提案 id 不能为空")
	}
	if strings.HasPrefix(id, ".") || strings.Contains(id, "..") {
		return fmt.Errorf("提案 id %q 非法（不允许以 . 开头或包含 ..）", id)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("提案 id %q 含非法字符 %q（只允许字母/数字/-/_/.）", id, r)
		}
	}
	return nil
}
