// Package models 提供内嵌的模型元数据目录（dist/data/models.json）。
//
// 目录由 main 包在启动时通过 Load 注入（embed.FS 只能声明在仓库根包），
// 之后任意包可用 ContextWindow 按模型名查询上下文窗口上限，
// 用于推导上下文压缩阈值与 TUI 上下文用量进度条。
// 未注入或模型未收录时查询返回 0，调用方据此回退到各自的默认值。
package models

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
)

// CatalogPath 是模型目录在注入的 fs.FS 中的路径。
const CatalogPath = "dist/data/models.json"

// fuzzyMinSimilarity 模糊匹配的最低相似度（0~1），低于此值视为目录未收录。
// 0.6 可覆盖 "Qwen3.8-27B-GSQ-RCO" → "alibaba/qwen3.8-27b"（0.600）这类
// 带厂商后缀的私有部署名，同时把无关字符串（实测 ≤0.45）挡在外面。
const fuzzyMinSimilarity = 0.6

// entry 只解析目录中用得到的字段，其余（description/modalities/weights 等）忽略。
type entry struct {
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
}

// fuzzyItem 是模糊匹配索引项，按 id 字典序排列以保证并列时取舍确定。
type fuzzyItem struct {
	id   string // 完整 id（"org/model"，小写），用于日志
	norm string // 归一化裸名（仅保留 a-z0-9）
	ctx  int
}

var (
	mu sync.RWMutex
	// byID 以完整 id（"org/model"，小写）为键。
	byID map[string]entry
	// byBare 以去掉 org 前缀的裸模型名（小写）为键，
	// 供配置里只写模型名（如 "qwen3-coder-plus"）、或 org 与目录不一致
	// （如 "deepseek-ai/DeepSeek-V4-Flash" vs "deepseek/deepseek-v4-flash"）时匹配。
	byBare map[string]entry
	// fuzzyIdx 供前两者都未命中时做相似度匹配。
	fuzzyIdx []fuzzyItem
	// fuzzyLogged 记录已告警过的查询名，避免每步重复输出。
	fuzzyLogged sync.Map
)

// Load 从 fsys 读取 CatalogPath 并重建索引，可重复调用（后一次覆盖前一次）。
func Load(fsys fs.FS) error {
	data, err := fs.ReadFile(fsys, CatalogPath)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", CatalogPath, err)
	}

	var raw map[string]entry
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse embedded %s: %w", CatalogPath, err)
	}

	items := make([]catalogItem, 0, len(raw))
	for id, e := range raw {
		if lower := strings.ToLower(strings.TrimSpace(id)); lower != "" {
			items = append(items, catalogItem{id: lower, e: e})
		}
	}
	// 按 id 排序后再建索引：裸名冲突时字典序最小者胜出，
	// 使查询结果与 map 遍历顺序无关。
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })

	ids := make(map[string]entry, len(items))
	bares := make(map[string]entry, len(items))
	fuzzy := make([]fuzzyItem, 0, len(items))
	for _, it := range items {
		ids[it.id] = it.e
		bare := bareName(it.id)
		if _, dup := bares[bare]; !dup {
			bares[bare] = it.e
		}
		fuzzy = append(fuzzy, fuzzyItem{id: it.id, norm: normalize(bare), ctx: it.e.Limit.Context})
	}

	mu.Lock()
	byID, byBare, fuzzyIdx = ids, bares, fuzzy
	mu.Unlock()
	fuzzyLogged.Clear()
	return nil
}

// ContextWindow 返回模型的上下文窗口上限（token），匹配顺序：
//
//  1. 完整 id（"org/model"）；
//  2. 去掉 org 前缀的裸模型名；
//  3. 归一化（小写、丢弃 a-z0-9 以外字符）后的相似度匹配，取最相似且不低于
//     fuzzyMinSimilarity 的条目。
//
// 三步均大小写不敏感；目录未加载、模型未收录或该条目无上下文数据
// （如 embedding 模型）时返回 0。
func ContextWindow(model string) int {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return 0
	}

	mu.RLock()
	defer mu.RUnlock()

	if e, ok := byID[key]; ok {
		return e.Limit.Context
	}
	bare := bareName(key)
	if e, ok := byBare[bare]; ok {
		return e.Limit.Context
	}
	if item, score, ok := fuzzyLookup(bare); ok {
		// 相似度匹配可能选错模型（如 "gpt-5.4-turbo" 命中 "openai/gpt-4-turbo"），
		// 首次命中时留一条线索便于排查阈值来源。
		warnFuzzyOnce(key, item.id, score)
		return item.ctx
	}
	return 0
}

// fuzzyLookup 在归一化裸名上做 Levenshtein 相似度匹配，返回最优且不低于
// fuzzyMinSimilarity 的条目。索引已按 id 排序且仅在严格更优时替换，
// 因此相似度并列时固定取 id 字典序更小者。
func fuzzyLookup(bare string) (fuzzyItem, float64, bool) {
	query := normalize(bare)
	if query == "" || len(fuzzyIdx) == 0 {
		return fuzzyItem{}, 0, false
	}

	var best fuzzyItem
	bestScore := 0.0
	for _, it := range fuzzyIdx {
		if score := similarity(query, it.norm); score > bestScore {
			best, bestScore = it, score
		}
	}
	if bestScore < fuzzyMinSimilarity {
		return fuzzyItem{}, 0, false
	}
	return best, bestScore, true
}

func warnFuzzyOnce(query, matchedID string, score float64) {
	if _, loaded := fuzzyLogged.LoadOrStore(query, struct{}{}); loaded {
		return
	}
	slog.Warn("Model not in embedded catalog, fuzzy-matched to nearest entry",
		"query", query,
		"matched", matchedID,
		"similarity", math.Round(score*1000)/1000)
}

// similarity 返回两个归一化字符串的相似度：1 - Levenshtein 距离 / 较长串长度。
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	longer := max(len(a), len(b))
	if longer == 0 {
		return 0
	}
	return 1 - float64(levenshtein(a, b))/float64(longer)
}

// levenshtein 计算编辑距离，单行滚动数组实现。
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(prev[j]+1, cur[j-1]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// normalize 丢弃 a-z0-9 以外的字符并转小写，
// 使 "claude-sonnet-4.5" 与目录中的 "claude-sonnet-4-5" 可直接对齐。
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// catalogItem 是建索引时的中间结构，id 已归一化为小写。
type catalogItem struct {
	id string
	e  entry
}

// bareName 去掉 "org/" 前缀，入参需已小写。
func bareName(lowerID string) string {
	if i := strings.LastIndexByte(lowerID, '/'); i >= 0 {
		return lowerID[i+1:]
	}
	return lowerID
}
