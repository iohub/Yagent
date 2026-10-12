package models

import (
	"math/rand"
	"sort"
	"strings"
)

// 高智商基准权重：DeepSWE / SWE-Bench Pro / GPQA Diamond 计 1.5，其余计 1.0。
// ai_score = Σ(benchmark_score × weight)，求和范围为候选模型的基准交集。
var boostedBenchmarks = map[string]float64{
	"DeepSWE":       1.5,
	"SWE-Bench Pro": 1.5,
	"GPQA Diamond":  1.5,
}

const defaultBenchmarkWeight = 1.0

// benchmarkWeight 返回某个基准项的权重。
func benchmarkWeight(name string) float64 {
	if w, ok := boostedBenchmarks[name]; ok {
		return w
	}
	return defaultBenchmarkWeight
}

// TierPicks 是对候选模型池打分后的三档分类结果。
type TierPicks struct {
	High, Mid, Low string             // 三档选中的模型名（原样回传入参名）
	Scores         map[string]float64 // 每个可打分候选模型的 ai_score
	Intersection   []string           // 实际参与求和的基准交集（排序后）
	Reason         string             // ok=false 时的禁用原因
}

// ClassifyTiers 对候选模型池按“严格基准交集”打分并选出三档：
// 最高分 = 高智商模型，中位数 = 性价比模型，最低分 = 快速模型。
//
// 规则：
//  1. 只保留在目录中报告了至少一项基准的候选（无基准数据的模型不参与打分）；
//  2. 交集 = 所有可打分候选的基准名交集；交集为空则禁用（ok=false）；
//  3. ai_score = Σ_{b∈交集} score[b] × weight(b)，权重见 boostedBenchmarks；
//  4. 可打分候选不足 3 个时无法构成三档，禁用（ok=false）；
//  5. 分数并列时随机取一个（rand.Shuffle 后稳定排序）。
//
// 入参可含重复模型名，内部去重。
func ClassifyTiers(candidateModels []string) (TierPicks, bool) {
	// 去重并保持首次出现顺序，随机打乱以满足“并列随机取一个”。
	seen := make(map[string]bool, len(candidateModels))
	unique := make([]string, 0, len(candidateModels))
	for _, m := range candidateModels {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		unique = append(unique, m)
	}
	rand.Shuffle(len(unique), func(i, j int) { unique[i], unique[j] = unique[j], unique[i] })

	// 收集每个候选的基准，剔除无基准数据者。
	benchmarks := make(map[string]map[string]float64, len(unique))
	scorable := make([]string, 0, len(unique))
	for _, m := range unique {
		bm := Benchmarks(m)
		if len(bm) == 0 {
			continue
		}
		benchmarks[m] = bm
		scorable = append(scorable, m)
	}

	if len(scorable) < 3 {
		return TierPicks{
			Scores: map[string]float64{},
			Reason: "insufficient scorable candidates: need at least 3 configured providers whose models report benchmarks in the catalog",
		}, false
	}

	// 严格交集。
	var intersection map[string]bool
	for _, m := range scorable {
		if intersection == nil {
			intersection = make(map[string]bool, len(benchmarks[m]))
			for name := range benchmarks[m] {
				intersection[name] = true
			}
			continue
		}
		for name := range intersection {
			if _, ok := benchmarks[m][name]; !ok {
				delete(intersection, name)
			}
		}
	}
	if len(intersection) == 0 {
		return TierPicks{
			Scores: map[string]float64{},
			Reason: "empty benchmark intersection across candidate models: no common benchmark to compare on",
		}, false
	}

	interNames := make([]string, 0, len(intersection))
	for name := range intersection {
		interNames = append(interNames, name)
	}
	sort.Strings(interNames)

	// 打分。
	scores := make(map[string]float64, len(scorable))
	for _, m := range scorable {
		var sum float64
		for _, name := range interNames {
			sum += benchmarks[m][name] * benchmarkWeight(name)
		}
		scores[m] = sum
	}

	// 按分数降序稳定排序（并列时保持上面 shuffle 的随机顺序）。
	ranked := make([]string, len(scorable))
	copy(ranked, scorable)
	sort.SliceStable(ranked, func(i, j int) bool {
		return scores[ranked[i]] > scores[ranked[j]]
	})

	n := len(ranked)
	picks := TierPicks{
		High:         ranked[0],
		Mid:          ranked[n/2],
		Low:          ranked[n-1],
		Scores:       scores,
		Intersection: interNames,
	}
	return picks, true
}
