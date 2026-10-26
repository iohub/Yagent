package util

import (
	"bytes"
	"strings"
	"testing"
)

func TestCappedBufferStopsAccumulatingAtMax(t *testing.T) {
	var b CappedBuffer
	b.Max = 10
	// io.Copy 的写入块大小不由调用方决定，所以跨块的写入也要恰好停在 Max。
	for i := 0; i < 3; i++ {
		if _, err := b.Write([]byte("abcdefghij")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if got := b.String(); len(got) != 10 {
		t.Errorf("累积 %d 字节，期望封顶在 10：%q", len(got), got)
	}
	if !b.Over() {
		t.Error("写了 30 字节而 Over() 仍为 false —— 截断没有被记账")
	}
}

// 到顶之后 Write 必须继续成功返回：这是"排空管道"的保证，报错会让孩子收 SIGPIPE。
func TestCappedBufferKeepsAcceptingWritesAfterCap(t *testing.T) {
	var b CappedBuffer
	b.Max = 4
	b.Write(bytes.Repeat([]byte("x"), 100))

	n, err := b.Write([]byte("more output after the cap"))
	if err != nil {
		t.Errorf("封顶后 Write 返回错误 %v —— 调用方的 io.Copy 会就此收手", err)
	}
	if n != len("more output after the cap") {
		t.Errorf("封顶后 Write 报告写了 %d 字节，期望 %d", n, len("more output after the cap"))
	}
	if len(b.String()) != 4 {
		t.Errorf("封顶后内容仍在增长：%q", b.String())
	}
}

func TestCappedBufferExactFitIsNotOver(t *testing.T) {
	var b CappedBuffer
	b.Max = 5
	b.Write([]byte(strings.Repeat("y", 5)))
	if b.Over() {
		t.Error("恰好写满 Max 不该算截断")
	}
	if b.String() != "yyyyy" {
		t.Errorf("内容 = %q，期望 %q", b.String(), "yyyyy")
	}
}
