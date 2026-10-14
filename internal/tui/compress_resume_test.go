package tui

import (
	"strings"
	"testing"

	"yagent/internal/memory"
)

// restoreCompressInfo 是 /resume 的压缩半边：token 由既有测试覆盖，这里只带 Compressions。
func restoreCompressInfo() memory.RolloutInfo {
	return memory.RolloutInfo{
		Model: "claude-x",
		Tokens: memory.RolloutTokenTotals{
			Input: 1000, Output: 200, CachedInput: 500, LastContextTokens: 900,
		},
		Compressions: []memory.CompactionRecord{
			{Kind: memory.CompactionKindRule, Saved: 3000, TokensBefore: 9000, TokensAfter: 6000},
			{Kind: memory.CompactionKindEmergency, Saved: 2000, TokensBefore: 6000, TokensAfter: 4000},
		},
	}
}

// TestResumeRestoresCompressBadge 端到端走完 applyRolloutRunState（含便签/统计/tabs 的
// 那套真实装配），确认 ⧉ 段的徽标与节省量在恢复后仍然成立。
func TestResumeRestoresCompressBadge(t *testing.T) {
	initLangManager()
	m := tabTestModel(t, 140, 40)

	applyRolloutRunState(m, restoreCompressInfo(), "director")

	snap := m.compressSnapshot()
	if snap.maxTier != 2 || snap.saved != 5000 {
		t.Errorf("恢复后 maxTier=%d saved=%d, want 2 / 5000", snap.maxTier, snap.saved)
	}
	if snap.any() != true {
		t.Error("恢复后 any() 应为真")
	}
	// ⧉ 计量条用的是 rollout 末值的 ContextTokens
	if got := m.currentRun().ContextTokens; got != 900 {
		t.Errorf("ContextTokens = %d, want 900", got)
	}

	joined := strings.Join(dashboardLines(m, dashboardPanelWidth, 30), "\n")
	if !strings.Contains(joined, "⧉ COMPRESS −5.0k (56%)") {
		t.Errorf("恢复后的侧栏应带节省量与 pct\n----\n%s----", joined)
	}
	// §8.4：cut 榜与采样序列没落盘，但不能留下空壳行
	if strings.Contains(joined, "cut:") || strings.Contains(joined, "ctx ▁") {
		t.Errorf("恢复后不该画出没有数据的两行\n----\n%s----", joined)
	}
}

// TestResumeResetClearsCompress 保证 resetTokenState 把压缩统计一起清掉 ——
// 否则换一个 rollout 恢复时，上一个会话的三档计数会累加进来。
func TestResumeResetClearsCompress(t *testing.T) {
	m := newTestModel()
	m.recordCompression("context_ultimate_compressed", map[string]interface{}{
		"original_tokens": 9000, "compressed_tokens": 1000, "saved_tokens": 8000,
	})
	if !m.compressSnapshot().any() {
		t.Fatal("fixture 没写入压缩统计")
	}

	resetTokenState(m)
	if m.compressSnapshot().any() {
		t.Error("resetTokenState 应清掉压缩统计")
	}
	if m.compress.maxOriginal != 0 {
		t.Errorf("maxOriginal = %d, want 0（pct 分母也会跟着串味）", m.compress.maxOriginal)
	}
}

// TestResumeApplyIsIdempotent 钉住那个真实缺陷：applyRolloutCompressions 被调用两次
// 就会把恢复的统计翻倍。函数自己必须是幂等的。
func TestResumeApplyIsIdempotent(t *testing.T) {
	m := newTestModel()
	info := restoreCompressInfo()

	applyRolloutCompressions(m, info)
	once := m.compressSnapshot()
	applyRolloutCompressions(m, info)
	twice := m.compressSnapshot()

	if once.saved != twice.saved || once.tiers != twice.tiers {
		t.Errorf("重复应用后统计翻倍：saved %d → %d", once.saved, twice.saved)
	}
}
