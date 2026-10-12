package models

import (
	"math"
	"testing"
	"testing/fstest"
)

// scoreFixture 构造带基准成绩的候选：
//   - strong / mid / weak 共享基准集 {A, DeepSWE}，可打分；
//   - nobench 无基准数据（应被剔除）；
//   - disjoint 只报告 B，导致若纳入则交集为空。
const scoreFixture = `{
  "acme/strong": {"limit":{"context":1000},"benchmarks":[{"name":"A","score":80},{"name":"DeepSWE","score":60}]},
  "acme/mid":    {"limit":{"context":1000},"benchmarks":[{"name":"A","score":50},{"name":"DeepSWE","score":40}]},
  "acme/weak":   {"limit":{"context":1000},"benchmarks":[{"name":"A","score":20},{"name":"DeepSWE","score":10}]},
  "acme/nobench":{"limit":{"context":1000}},
  "acme/disjoint":{"limit":{"context":1000},"benchmarks":[{"name":"B","score":99}]}
}`

func loadScoreFixture(t *testing.T) {
	t.Helper()
	loadFixture(t, scoreFixture)
}

func TestClassifyTiers_HappyPath(t *testing.T) {
	loadScoreFixture(t)

	// 交集 = {A, DeepSWE}；DeepSWE 权重 1.5，A 权重 1.0。
	// strong = 80*1 + 60*1.5 = 170；mid = 50 + 40*1.5 = 110；weak = 20 + 10*1.5 = 35。
	picks, ok := ClassifyTiers([]string{"acme/strong", "acme/mid", "acme/weak", "acme/nobench"})
	if !ok {
		t.Fatalf("ClassifyTiers() ok=false, reason=%q", picks.Reason)
	}
	if picks.High != "acme/strong" || picks.Mid != "acme/mid" || picks.Low != "acme/weak" {
		t.Errorf("tiers = %s/%s/%s, want strong/mid/weak", picks.High, picks.Mid, picks.Low)
	}
	if got := picks.Scores["acme/strong"]; math.Abs(got-170) > 1e-9 {
		t.Errorf("strong score = %v, want 170", got)
	}
	if got := picks.Scores["acme/mid"]; math.Abs(got-110) > 1e-9 {
		t.Errorf("mid score = %v, want 110", got)
	}
	if len(picks.Intersection) != 2 {
		t.Errorf("intersection = %v, want [A DeepSWE]", picks.Intersection)
	}
	if _, present := picks.Scores["acme/nobench"]; present {
		t.Error("nobench should be excluded from scoring")
	}
}

func TestClassifyTiers_InsufficientCandidates(t *testing.T) {
	loadScoreFixture(t)

	// 只有 2 个可打分候选 → 不足三档。
	picks, ok := ClassifyTiers([]string{"acme/strong", "acme/mid"})
	if ok {
		t.Fatalf("ClassifyTiers() ok=true with only 2 scorable candidates")
	}
	if picks.Reason == "" {
		t.Error("expected a non-empty disable reason")
	}
}

func TestClassifyTiers_EmptyIntersection(t *testing.T) {
	loadScoreFixture(t)

	// disjoint 只报告 B，与其余的 {A,DeepSWE} 无交集 → 禁用。
	picks, ok := ClassifyTiers([]string{"acme/strong", "acme/mid", "acme/disjoint"})
	if ok {
		t.Fatalf("ClassifyTiers() ok=true despite empty intersection")
	}
	if picks.Reason == "" {
		t.Error("expected a non-empty disable reason")
	}
}

func TestClassifyTiers_TiesProduceDistinctModels(t *testing.T) {
	catalog := `{
	  "acme/a": {"benchmarks":[{"name":"A","score":50}]},
	  "acme/b": {"benchmarks":[{"name":"A","score":50}]},
	  "acme/c": {"benchmarks":[{"name":"A","score":50}]}
	}`
	if err := Load(fstest.MapFS{CatalogPath: &fstest.MapFile{Data: []byte(catalog)}}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(resetCatalog)

	picks, ok := ClassifyTiers([]string{"acme/a", "acme/b", "acme/c"})
	if !ok {
		t.Fatalf("ClassifyTiers() ok=false, reason=%q", picks.Reason)
	}
	seen := map[string]bool{picks.High: true, picks.Mid: true, picks.Low: true}
	if len(seen) != 3 {
		t.Errorf("tied tiers not distinct: high=%s mid=%s low=%s", picks.High, picks.Mid, picks.Low)
	}
}

func TestBenchmarks_DuplicateTakesMax(t *testing.T) {
	catalog := `{
	  "acme/dup": {"benchmarks":[{"name":"A","score":10},{"name":"A","score":42}]}
	}`
	if err := Load(fstest.MapFS{CatalogPath: &fstest.MapFile{Data: []byte(catalog)}}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(resetCatalog)

	bm := Benchmarks("acme/dup")
	if bm["A"] != 42 {
		t.Errorf("Benchmarks(A) = %v, want 42 (max of duplicates)", bm["A"])
	}
}

func TestBenchmarks_NotFound(t *testing.T) {
	loadScoreFixture(t)
	if bm := Benchmarks("acme/does-not-exist"); len(bm) != 0 {
		t.Errorf("Benchmarks(unknown) = %v, want empty", bm)
	}
}
