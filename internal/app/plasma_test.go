package app

import (
	"errors"
	"strings"
	"testing"

	"yagent/internal/config"
)

// TestBuildPlasmaGenerator_ExtractErrorIsVisible 钉住 P10 修掉的那个静默坑：
// 内嵌二进制提取失败时，原因必须进**同一个** Skipped 语句，而不是只留一行
// slog.Debug（P9a 的先例，app.go:167-168 的旧写法）。
//
// 判据本身由 agents.plasmaAvailableWith 给出（缺 pyz，或本机没有 python3），
// 这里不复制那套逻辑，只断言"提取失败被接上了"——它才是这个函数新加的行为。
func TestBuildPlasmaGenerator_ExtractErrorIsVisible(t *testing.T) {
	// BinPath 走 os.UserHomeDir：把 HOME 指到空目录，等价于"机器上还没解过 pyz"。
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{}
	cfg.Plasma.Enable = true // 关掉 [plasma] 时函数在判据之前就返回，测不到这一条

	gen, reason := buildPlasmaGenerator(cfg, nil, nil, errors.New("disk full"))
	if gen != nil {
		t.Fatalf("判据失败时不该装配生成器（会得到\"判定通过但每次 exit 1\"的半装配态）")
	}
	for _, want := range []string{"提取失败", "disk full", "修复"} {
		if !strings.Contains(reason, want) {
			t.Errorf("降级原因 %q 缺 %q —— Surface.Skipped 只显示这一句，缺了读的人会以为包没做出来", reason, want)
		}
	}
}
