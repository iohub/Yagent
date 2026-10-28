package compression

import "testing"

// TestDefaultOptionsIsAllZero 钉住"DefaultOptions 不许携带任何默认值"。
//
// 存在的理由：阈值与保留量的默认值已经各有一份真相 ——
// ResolveThreshold（threshold.go）与 DefaultToolResultKeepTokens / UltimateKeepPlans=0
// （truncate_results.go、ApplyUltimate）。在这里再写一遍数字，就会有两套默认值：
// 改一处、另一处静默保留旧值，而调用方拿到的是哪个完全取决于它走哪条路径。
// 因此 DefaultOptions 的唯一合法实现就是零值。
func TestDefaultOptionsIsAllZero(t *testing.T) {
	if got := DefaultOptions(); got != (Options{}) {
		t.Errorf("DefaultOptions() = %+v, want 全零（零值 = 走本包既有兜底）", got)
	}
}

// TestOptionsZeroValueFallsBackToPackageDefaults 零值 Options 必须落到本包既有兜底上：
// 阈值由 ResolveThreshold 决定（目录未加载时 = DefaultContextCompressionThreshold），
// 保留量由调用方的 `<= 0` 判定决定。这条把"零值语义"写成了可执行的定义。
func TestOptionsZeroValueFallsBackToPackageDefaults(t *testing.T) {
	if got := ResolveThreshold("", DefaultOptions().Threshold); got != DefaultContextCompressionThreshold {
		t.Errorf("零值 Threshold 解析成 %d, want 兜底 %d", got, DefaultContextCompressionThreshold)
	}
	if keep := DefaultOptions().KeepTokens; keep > 0 {
		t.Errorf("零值 KeepTokens = %d, want 0（调用方据此回落到 DefaultToolResultKeepTokens=%d）", keep, DefaultToolResultKeepTokens)
	}
	if plans := DefaultOptions().UltimateKeepPlans; plans != 0 {
		t.Errorf("零值 UltimateKeepPlans = %d, want 0（= 全部保留）", plans)
	}
}
