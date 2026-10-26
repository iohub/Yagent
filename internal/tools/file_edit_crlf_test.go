package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// search_replace_in_file 的行尾契约。read_file 现在把 CRLF 文件的 \r 原样交回，
// 模型照抄是正确行为，所以编辑侧必须兜住"抄的时候丢了 \r"这一种情况，
// 同时**不许**把混合行尾的文件整体重写成另一种行尾。

func editResult(t *testing.T, tool *ReplaceBlockTool, params map[string]interface{}) map[string]interface{} {
	t.Helper()
	res, err := tool.ExecuteReplaceBlock(context.Background(), params)
	if err != nil {
		t.Fatalf("ExecuteReplaceBlock 返回了 Go error: %v", err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("返回值是 %T，期望 map", res)
	}
	return m
}

func editSuccess(t *testing.T, m map[string]interface{}) bool {
	t.Helper()
	ok, _ := m["success"].(bool)
	return ok
}

func editError(t *testing.T, m map[string]interface{}) string {
	t.Helper()
	if editSuccess(t, m) {
		t.Fatalf("期望失败，实际回执：%v", m)
	}
	s, _ := m["error"].(string)
	return s
}

// prepare 写一个文件并返回工具与相对路径。
func prepare(t *testing.T, name string, data []byte) (*ReplaceBlockTool, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Fatal(err)
	}
	return NewReplaceBlockTool(dir), name
}

func readBack(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 一致 CRLF 的文件 + 丢了 \r 的多行 old_string：回退必须命中，并把行尾原样还回去。
func TestApplyReplace_CRLFFallbackMatchesAndRestoresEndings(t *testing.T) {
	tool, rel := prepare(t, "crlf.txt", []byte("one\r\ntwo\r\nthree\r\n"))
	dir := tool.workingDir

	got := editResult(t, tool, map[string]interface{}{
		"file_path":  rel,
		"old_string": "one\ntwo",
		"new_string": "start\nend",
	})
	if !editSuccess(t, got) {
		t.Fatalf("CRLF 文件上 LF 的 old_string 必须走回退成功，实际：%s", got["error"])
	}
	if v := got["line_endings"]; v != "crlf-normalized" {
		t.Errorf("回退生效过必须在回执里看得见，实际 line_endings = %v", v)
	}
	want := "start\r\nend\r\nthree\r\n"
	if b := readBack(t, dir, rel); b != want {
		t.Errorf("写回的行尾没还原：\n got %q\nwant %q", b, want)
	}
}

// 精确命中不等于行尾安全：单行 old_string 一定命中，而多行 LF 的 new_string
// 直接插进 CRLF 文件就造出混合行尾。这条钉的是 crlf-applied 那一支。
func TestApplyReplace_ExactMatchStillKeepsFileUniformCRLF(t *testing.T) {
	tool, rel := prepare(t, "crlf.txt", []byte("one\r\ntwo\r\nthree\r\n"))

	got := editResult(t, tool, map[string]interface{}{
		"file_path":  rel,
		"old_string": "two",
		"new_string": "TWO\n2",
	})
	if !editSuccess(t, got) {
		t.Fatalf("编辑失败：%v", got["error"])
	}
	if v := got["line_endings"]; v != "crlf-applied" {
		t.Errorf("new_string 被转过行尾必须记账，实际 line_endings = %v", v)
	}
	b := readBack(t, tool.workingDir, rel)
	if b != "one\r\nTWO\r\n2\r\nthree\r\n" {
		t.Errorf("got %q, want %q", b, "one\r\nTWO\r\n2\r\nthree\r\n")
	}
	if !isUniformCRLF(b) {
		t.Errorf("CRLF 文件被写成了混合行尾：%q", b)
	}
}

// 模型照抄 read_file 输出时 old_string 会带 \r —— 那走的是精确匹配，
// 不该出现 line_endings 字段（回执形状变了会让上游困惑）。
func TestApplyReplace_CRLFWithVerbatimOldStringUsesExactPath(t *testing.T) {
	tool, rel := prepare(t, "crlf.txt", []byte("one\r\ntwo\r\n"))

	got := editResult(t, tool, map[string]interface{}{
		"file_path":  rel,
		"old_string": "one\r\ntwo",
		"new_string": "start\r\nend",
	})
	if !editSuccess(t, got) {
		t.Fatalf("带 \\r 的 old_string 必须精确命中，实际：%v", got["error"])
	}
	if _, ok := got["line_endings"]; ok {
		t.Error("精确匹配不该走回退")
	}
	if b := readBack(t, tool.workingDir, rel); b != "start\r\nend\r\n" {
		t.Errorf("got %q, want %q", b, "start\r\nend\r\n")
	}
}

// 混合行尾：宁可失败，也不许静默把整个文件的行尾改成一种。
// 夹具用 "two\r\nthree" —— 模型以为全篇 CRLF 时就会这么抄，而文件里那一处是 LF。
func TestApplyReplace_MixedEndingsRefuseWithoutRewriting(t *testing.T) {
	original := "one\r\ntwo\nthree\r\n"
	tool, rel := prepare(t, "mixed.txt", []byte(original))

	got := editResult(t, tool, map[string]interface{}{
		"file_path":  rel,
		"old_string": "two\r\nthree",
		"new_string": "TWO",
	})
	msg := editError(t, got)
	if !strings.Contains(msg, "mixed line endings") {
		t.Errorf("混合行尾要点名原因，实际：%q", msg)
	}
	if b := readBack(t, tool.workingDir, rel); b != original {
		t.Errorf("失败不允许动文件：\n got %q\nwant %q", b, original)
	}
}

// LF 文件必须完全走原来的路径：回退一旦无条件生效，
// 它会把整个 LF 文件重写成 CRLF —— 那是一条会静默炸掉全仓 diff 的改动。
func TestApplyReplace_LFFilesUnaffectedByFallback(t *testing.T) {
	tool, rel := prepare(t, "lf.txt", []byte("one\ntwo\nthree\n"))

	got := editResult(t, tool, map[string]interface{}{
		"file_path":  rel,
		"old_string": "two",
		"new_string": "TWO",
	})
	if !editSuccess(t, got) {
		t.Fatalf("LF 文件上的普通编辑失败了：%v", got["error"])
	}
	if _, ok := got["line_endings"]; ok {
		t.Error("LF 文件不该出现 line_endings —— 回退无条件生效了")
	}
	b := readBack(t, tool.workingDir, rel)
	if b != "one\nTWO\nthree\n" {
		t.Errorf("got %q, want %q", b, "one\nTWO\nthree\n")
	}
	if strings.Contains(b, "\r") {
		t.Errorf("LF 文件被写进了 \\r：%q", b)
	}
}

// 回退不放宽唯一性：归一化之后出现两次照样要报。
// 夹具用 "x\ny" —— 它在 CRLF 文件里精确匹配不上（所以一定走回退），
// 归一化后却出现两次（所以必须被唯一性拦下）。
func TestApplyReplace_CRLFFallbackStillRequiresUniqueMatch(t *testing.T) {
	original := "x\r\ny\r\nx\r\ny\r\n"
	tool, rel := prepare(t, "crlf.txt", []byte(original))

	got := editResult(t, tool, map[string]interface{}{
		"file_path":  rel,
		"old_string": "x\ny",
		"new_string": "z",
	})
	msg := editError(t, got)
	if !strings.Contains(msg, "appears 2 times") {
		t.Errorf("归一化后的重复没有被记账，实际：%q", msg)
	}
	if _, ok := got["line_endings"]; ok {
		t.Error("失败的回执不该带 line_endings")
	}
	if b := readBack(t, tool.workingDir, rel); b != original {
		t.Errorf("唯一性失败不允许动文件：got %q", b)
	}
}

// 单元级：纯 LF 文件、没命中 → 只报"没找到"，不许扯上行尾。
func TestApplyReplace_ExactMissOnPlainFileDoesNotMentionEndings(t *testing.T) {
	got, endings, msg := applyReplace("a\nb\n", "zzz", "y")
	if got != "" || endings != "" {
		t.Errorf("没命中却改了内容或走了回退：got=%q endings=%q", got, endings)
	}
	if msg != "old_string not found in file" {
		t.Errorf("纯 LF 文件的失败原因不该扯上行尾：%q", msg)
	}
}
