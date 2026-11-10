package plasma

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yagent/internal/util"
)

// Store 是内容寻址的 Agent 仓库：
//
//	{root}/{name}/{hash}/bundle.json   不可变（主键 = hash）
//	{root}/{name}/{hash}/report.json   可变（grade 会升降）
//	{root}/{name}/{hash}/meta.json     可变（escalation_approved / promoted 计数 / 出题谱系）
//	{root}/index.json                  name → 当前 hash（唯一可变指针，原子写）
//	{root}/telemetry.jsonl             遥测账本（追加）
//
// 不可变与可变必须分文件：把可变状态写进 bundle.json 会改 hash，而 hash 是主键。
type Store struct {
	Root string
}

func NewStore(root string) *Store { return &Store{Root: root} }

// Entry 是 List 的一行。
type Entry struct {
	Name    string
	Hash    string
	Meta    Meta
	Current bool
}

// Meta 是可变状态。
type Meta struct {
	Version            string     `json:"version,omitempty"`
	Grade              Grade      `json:"grade"`
	Score              float64    `json:"score"`
	Runs               int        `json:"runs"`
	PromotedRuns       int        `json:"promoted_runs,omitempty"`
	EscalationApproved bool       `json:"escalation_approved,omitempty"`
	FixtureProvenance  Provenance `json:"fixture_provenance"`
	Lineage            *Lineage   `json:"lineage,omitempty"`
	CreatedAt          string     `json:"created_at"`
}

// ErrFixtureProvenance 表示出题谱系与变异谱系无法区分 —— 进化器既改 Agent 又改它的夹具，
// 就是自己出题自己考（Goodhart）。store 层直接拒收，不给"先存下来再说"留口子。
var ErrFixtureProvenance = errors.New("fixture provenance lineage must differ from the agent's own lineage")

// ErrImmutable 表示同 hash 目录里已有的 bundle.json 与本次要写的字节不一致。
// 它是 ADR-5 第一条防线的落点：夹具不可变，改夹具必须换 hash。
var ErrImmutable = errors.New("plasma store: bundle is immutable")

// HashBundle 是主键：归一化 IR 的 sha256。encoding/json 对结构体字段与 map 键都排序输出，
// 所以同一份 IR 永远得到同一个 hash（幂等是 RFC 的硬约束，测试钉住）。
func HashBundle(b *Bundle) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("hash bundle: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Put 落盘并返回 hash。同一个 {name,hash} 已存在时**不覆盖**（bundle 与夹具不可变）；
// 于是"改了夹具"必然得到新 hash ⇒ 新目录 ⇒ grade 从 provisional 重新开始，不继承旧的。
func (s *Store) Put(b *Bundle, r *Report) (string, error) {
	if b == nil {
		return "", fmt.Errorf("nil bundle")
	}
	if err := checkFixtureProvenance(b); err != nil {
		return "", err
	}
	// 落盘前把只在内存里存在的 duration 写成文本，否则读回时 LLMTimeout 是 0。
	if b.Runtime.LLMTimeoutText == "" && b.Runtime.LLMTimeout != 0 {
		b.Runtime.LLMTimeoutText = b.Runtime.LLMTimeout.String()
	}
	hash, err := HashBundle(b)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.Root, b.Name, hash)
	bundleRaw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal bundle: %w", err)
	}
	if existing, err := os.ReadFile(filepath.Join(dir, "bundle.json")); err == nil {
		// 同一个 hash 目录里已有 bundle.json：**必须逐字节一致**才允许当作"已存在"返回。
		//
		// 为什么不是简单地 `return hash, nil`：hash 是"归一化 IR 的 sha256"，而落盘的是
		// MarshalIndent 之后的字节。两者在正常路径上同源，但一旦有人在中间做了归一化
		// 的行为变更（或在同目录里手改了内容），"hash 相同"就不再等价于"内容相同"。
		// 静默复用的后果是**夹具不可变性被绕过**：调用方以为写进去的是新夹具，磁盘上
		// 还是旧的，而 List/Current 都指向那一份。ADR-5 的整条防线就建立在这里。
		if !bytes.Equal(existing, append(bundleRaw, '\n')) && !bytes.Equal(existing, bundleRaw) {
			return "", fmt.Errorf("%s: %w — %s 已存在且内容与本次要写的不一致；"+
				"夹具与 contract 一经落盘即不可变（ADR-5），改夹具必须得到新 hash（改完重新 Compile 会自然换 hash）",
				CodeStoreImmutable, ErrImmutable, filepath.Join(dir, "bundle.json"))
		}
		return hash, nil
	}

	if err := util.AtomicWrite(filepath.Join(dir, "bundle.json"), bundleRaw,
		util.AtomicWriteOptions{DirPerm: 0o700, FilePerm: 0o600, Label: "plasma/store"}); err != nil {
		return "", err
	}

	grade := GradeProvisional
	if r != nil && r.Grade != "" {
		grade = r.Grade
	}
	if r != nil && r.EscalationRequired {
		grade = GradeProvisional // 用到了升级就必须等 5b，哪怕报告说更高
	}
	meta := Meta{
		Version:           b.Version,
		Grade:             grade,
		FixtureProvenance: fixtureProvenance(b),
		Lineage:           b.Lineage,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return "", err
	}
	if r != nil {
		if err := s.writeJSON(filepath.Join(dir, "report.json"), newReportRecord(r)); err != nil {
			return "", err
		}
	}
	// 首次落地就把指针指过去；进化/升级由 Promote 显式改指针。
	if err := s.pointIndex(b.Name, hash); err != nil {
		return "", err
	}
	return hash, nil
}

// CheckFixtureProvenance 是"出题谱系必须独立于变异谱系"的**唯一**判据（ADR-5 / T3）。
//
// 两个调用点：Compile 落定夹具之后（`ensureStructuralFixtures` 补完 provenance 那一刻，
// 也就是 5b 之前）与 Store.Put 之前。导出它是因为 agents 侧的进化管线要在**跑 5b 之前**
// 就问一次 —— 让一份注定被拒收的 bundle 先烧掉一轮真 LLM，是把防线做成了事后检查。
//
// 判据刻意比"字符串不相等"更严一点：变异体（Lineage.Mutation 非空）必须由**另一条**
// 谱系出题。没有变异的 bundle（首次生成、内建提案）不受限制，因为不存在"自己考自己"。
func CheckFixtureProvenance(b *Bundle) error {
	if b == nil || b.Fixtures == nil ||
		(len(b.Fixtures.Structural) == 0 && len(b.Fixtures.Behavioral) == 0) {
		return nil // 没有夹具就谈不上"自己出题自己考"
	}
	prov := strings.TrimSpace(b.Fixtures.Provenance.Lineage)
	if prov == "" {
		return fmt.Errorf("%w: %s 夹具存在但 provenance.lineage 为空；"+
			"note: 出题者必须自我标识，否则无法判定它是不是变异体自己出的题",
			ErrFixtureProvenance, CodeFixtureProvenance)
	}
	if b.Lineage == nil {
		return nil
	}
	mutation := strings.TrimSpace(b.Lineage.Mutation)
	if mutation == "" {
		return nil // 不是变异体：没有"自己出题自己考"这回事
	}
	if prov == mutation {
		return fmt.Errorf("%w: %s 变异谱系与出题谱系同为 %q；"+
			"note: 同谱系不得为自己的变异出题（Goodhart 防线二）",
			ErrFixtureProvenance, CodeFixtureProvenance, prov)
	}
	return nil
}

func checkFixtureProvenance(b *Bundle) error { return CheckFixtureProvenance(b) }

func fixtureProvenance(b *Bundle) Provenance {
	if b.Fixtures == nil {
		return Provenance{}
	}
	return b.Fixtures.Provenance
}

// Load 读回一份 bundle 与 meta，并校验 hash：磁盘被改过一个字节就拒载。
func (s *Store) Load(name, hash string) (*Bundle, *Meta, error) {
	dir := filepath.Join(s.Root, name, hash)
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("read bundle: %w", err)
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, nil, fmt.Errorf("parse bundle: %w", err)
	}
	// LLMTimeout 是 json:"-"（落盘用 LLMTimeoutText），读回必须还原，否则阶段 3 判越界。
	if err := b.normalizeRuntime(); err != nil {
		return nil, nil, fmt.Errorf("parse bundle runtime: %w", err)
	}
	actual, err := HashBundle(&b)
	if err != nil {
		return nil, nil, err
	}
	if actual != hash {
		// PLS-701：磁盘字节与主键不再一致。文案带两个 hash —— "被改过"与"读错了目录"
		// 在这句话之前无法区分，而修法完全不同（重装 vs 修 index）。
		return nil, nil, fmt.Errorf("%s: %w — 目录 hash %s 与内容 hash %s 不符；"+
			"修法：用 `yagent plasma list` 找到可用 bundle 后回滚 index，或重新生成",
			CodeLoadTampered, ErrTampered, hash, actual)
	}
	var meta Meta
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err == nil {
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return nil, nil, fmt.Errorf("parse meta: %w", err)
		}
	}
	return &b, &meta, nil
}

// ErrTampered 是加载期 hash 校验失败。
var ErrTampered = errors.New("plasma store: bundle tampered")

// Current 读 index 里 name 当前指向的 hash。
func (s *Store) Current(name string) (string, bool) {
	idx := s.readIndex()
	hash, ok := idx[name]
	return hash, ok
}

// List 按 name 字典序返回所有已落地的 Agent（确定性顺序：prompt 前缀缓存靠它）。
func (s *Store) List() []Entry {
	idx := s.readIndex()
	names := make([]string, 0, len(idx))
	for name := range idx {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Entry, 0, len(names))
	for _, name := range names {
		hash := idx[name]
		var meta Meta
		if raw, err := os.ReadFile(filepath.Join(s.Root, name, hash, "meta.json")); err == nil {
			_ = json.Unmarshal(raw, &meta)
		}
		out = append(out, Entry{Name: name, Hash: hash, Meta: meta, Current: true})
	}
	return out
}

// Promote 把 index 指针指向 name 的另一个 hash（回滚 = 指向旧 hash，不需要"撤销"逻辑）。
func (s *Store) Promote(name, hash string) error {
	if _, err := os.Stat(filepath.Join(s.Root, name, hash, "bundle.json")); err != nil {
		return fmt.Errorf("promote: %s@%s 不存在: %w", name, hash, err)
	}
	return s.pointIndex(name, hash)
}

// SetGrade 覆盖某个 hash 的 grade（promote 用）。bundle.json 不可变，grade 属可变状态，
// 所以只改 meta.json，且走 util.AtomicWrite（index.json 不会被这次写坏）。
//
// hash 目录不存在时**报错**而不是顺手建一个：那意味着调用方拿着一个不该存在的坐标，
// 建目录的后果是 store 里多出一份没有 bundle.json 的"幽灵条目"，而 List 只读 index、
// 看不到它 —— 失败会以"promote 成功但 list 里没有"的形式过很久才暴露。
func (s *Store) SetGrade(name, hash string, g Grade) error {
	switch g {
	case GradeProvisional, GradeVerified, GradePromoted:
	default:
		return fmt.Errorf("set grade: %q 不是合法档位（provisional / verified / promoted）", g)
	}
	dir := filepath.Join(s.Root, name, hash)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("set grade: %s@%s 不存在: %w", name, hash, err)
	}
	path := filepath.Join(dir, "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("set grade: read meta: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("set grade: parse meta: %w", err)
	}
	meta.Grade = g
	return s.writeJSON(path, meta)
}

// ApproveEscalation 按 hash 记批准。新 hash（进化出的新版本）没有这条记录 ⇒ 批准自然失效。
func (s *Store) ApproveEscalation(name, hash string) error {
	path := filepath.Join(s.Root, name, hash, "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("approve: read meta: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("approve: parse meta: %w", err)
	}
	meta.EscalationApproved = true
	return s.writeJSON(path, meta)
}

// RecordRun 累加生产运行计数（promoted 棘轮的数据源）。fixture_run 的调用方不该调它。
func (s *Store) RecordRun(name, hash string, success bool) error {
	path := filepath.Join(s.Root, name, hash, "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	meta.Runs++
	if success {
		meta.PromotedRuns++
	} else if meta.PromotedRuns > 0 {
		meta.PromotedRuns = 0 // 生产失败清零，掉档由 grade 判定读这个数
	}
	return s.writeJSON(path, meta)
}

// GC 删掉"未被 index 指向且超过 ttl"的 hash 目录。返回删除数。
// 没有 GC 的 store 会无限膨胀 —— artifact 包今天就是这个状态，不重犯。
func (s *Store) GC(now time.Time, ttl time.Duration) (int, error) {
	if ttl <= 0 {
		return 0, nil
	}
	idx := s.readIndex()
	referenced := map[string]bool{}
	for name, hash := range idx {
		referenced[name+"/"+hash] = true
	}
	names, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, ne := range names {
		if !ne.IsDir() {
			continue
		}
		name := ne.Name()
		hashes, err := os.ReadDir(filepath.Join(s.Root, name))
		if err != nil {
			continue
		}
		for _, he := range hashes {
			if !he.IsDir() {
				continue
			}
			key := name + "/" + he.Name()
			if referenced[key] {
				continue
			}
			info, err := he.Info()
			if err != nil || now.Sub(info.ModTime()) < ttl {
				continue
			}
			if err := os.RemoveAll(filepath.Join(s.Root, name, he.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

func (s *Store) readIndex() map[string]string {
	idx := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(s.Root, "index.json"))
	if err != nil {
		return idx
	}
	_ = json.Unmarshal(raw, &idx)
	return idx
}

func (s *Store) pointIndex(name, hash string) error {
	idx := s.readIndex()
	if idx[name] == hash {
		return nil
	}
	idx[name] = hash
	return s.writeJSON(filepath.Join(s.Root, "index.json"), idx)
}

// writeJSON 一律走 util.AtomicWrite（全仓唯一的 tmp+rename 内核）。
func (s *Store) writeJSON(path string, v interface{}) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return util.AtomicWrite(path, raw, util.AtomicWriteOptions{
		DirPerm: 0o700, FilePerm: 0o600, Label: "plasma/store",
	})
}
