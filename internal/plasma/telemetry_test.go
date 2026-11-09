package plasma_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"yagent/internal/plasma"
)

func TestAppendTelemetry_WritesParseableLine(t *testing.T) {
	root := t.TempDir()
	rec := plasma.TelemetryRecord{
		Agent: "security_auditor", BundleHash: "abc", Grade: plasma.GradeVerified,
		StopReason: "agent_exit", Steps: 7, TokensIn: 18400, TokensOut: 2100,
		FixtureRun: false,
	}
	if err := plasma.AppendTelemetry(root, rec); err != nil {
		t.Fatalf("AppendTelemetry: %v", err)
	}
	if err := plasma.AppendTelemetry(root, rec); err != nil {
		t.Fatalf("second append: %v", err)
	}

	f, err := os.Open(filepath.Join(root, "telemetry.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var got plasma.TelemetryRecord
		if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v", n+1, err)
		}
		if got.Agent != rec.Agent || got.Steps != rec.Steps || got.StopReason != rec.StopReason {
			t.Errorf("第 %d 行内容漂移: %+v", n+1, got)
		}
		if got.Ts == "" {
			t.Errorf("第 %d 行缺 ts（追加时该自动补）", n+1)
		}
		n++
	}
	if n != 2 {
		t.Errorf("行数 = %d, want 2", n)
	}
}

// 追加失败必须返回 error 让调用方留账目，而不是 panic 或静默吞掉。
func TestAppendTelemetry_ReportsWriteFailure(t *testing.T) {
	// 用一个"父路径是文件"的目录制造不可创建。
	base := t.TempDir()
	blocker := filepath.Join(base, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := plasma.AppendTelemetry(filepath.Join(blocker, "sub"), plasma.TelemetryRecord{Agent: "a"})
	if err == nil {
		t.Fatal("不可写路径应返回 error")
	}
}
