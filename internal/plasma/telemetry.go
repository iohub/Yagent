package plasma

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TelemetryRecord 是遥测账本的一行（ADR-6）。一行而不是整个会话：
// rollout 是重放账本，拿它做分析会把进化器耦合到重放格式变更上，且要反复解析大文件。
type TelemetryRecord struct {
	Ts           string `json:"ts"`
	Agent        string `json:"agent"`
	BundleHash   string `json:"bundle_hash,omitempty"`
	Grade        Grade  `json:"grade,omitempty"`
	Session      string `json:"session,omitempty"`
	DelegationID string `json:"delegation_id,omitempty"`
	StopReason   string `json:"stop_reason"`
	Steps        int    `json:"steps"`
	TokensIn     int64  `json:"tokens_in"`
	TokensOut    int64  `json:"tokens_out"`
	Degraded     bool   `json:"degraded"`
	Escalated    bool   `json:"escalated"`
	NoteRef      string `json:"note_ref,omitempty"`
	// FixtureRun 标记这次是 5a/5b 的夹具运行；评分只统计 fixture_run=false 的记录。
	FixtureRun bool `json:"fixture_run"`
}

// AppendTelemetry 追加一行。失败返回 error，但**调用方不得因此阻断委派**
// （照 artifact.Save 失败的取向）；同时必须把失败留成一条可见账目，否则就是静默丢失。
func AppendTelemetry(root string, rec TelemetryRecord) error {
	if rec.Ts == "" {
		rec.Ts = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal telemetry: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("telemetry dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(root, "telemetry.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open telemetry: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write telemetry: %w", err)
	}
	return nil
}
