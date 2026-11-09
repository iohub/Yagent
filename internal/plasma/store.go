package plasma

import (
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
	if _, err := os.Stat(filepath.Join(dir, "bundle.json")); err == nil {
		return hash, nil // 已存在：不可变，直接返回
	}

	bundleRaw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal bundle: %w", err)
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

func checkFixtureProvenance(b *Bundle) error {
	if b.Fixtures == nil || (len(b.Fixtures.Structural) == 0 && len(b.Fixtures.Behavioral) == 0) {
		return nil // 没有夹具就谈不上"自己出题自己考"
	}
	prov := strings.TrimSpace(b.Fixtures.Provenance.Lineage)
	if prov == "" {
		return fmt.Errorf("%w: fixtures 存在但 provenance.lineage 为空", ErrFixtureProvenance)
	}
	if b.Lineage != nil && prov == strings.TrimSpace(b.Lineage.Mutation) {
		return fmt.Errorf("%w: %q", ErrFixtureProvenance, prov)
	}
	return nil
}

func fixtureProvenance(b *Bundle) Provenance {
	if b.Fixtures == nil {
		return Provenance{}
	}
	return b.Fixtures.Provenance
}

// reportRecord 是落盘的诊断快照（不是 Report 全量：Report 里有非导出字段）。
type reportRecord struct {
	Grade              Grade        `json:"grade"`
	EscalationRequired bool         `json:"escalation_required"`
	Diagnostics        []Diagnostic `json:"diagnostics,omitempty"`
}

func newReportRecord(r *Report) reportRecord {
	return reportRecord{Grade: r.Grade, EscalationRequired: r.EscalationRequired, Diagnostics: r.Diagnostics}
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
		return nil, nil, fmt.Errorf("%w: 磁盘 hash %s 与内容 %s 不符", ErrTampered, hash, actual)
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
