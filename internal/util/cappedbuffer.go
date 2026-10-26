package util

import "bytes"

// CappedBuffer 累积写入到 Max 为止，之后丢弃字节但仍然接受写入。
// Write 永不返回错误：报错会让 io.Copy 提前收手，子进程下一次写管道就收 SIGPIPE——
// 那会把"输出太大"变成"命令失败"，而失控输出本来是不该改变退出码的。
type CappedBuffer struct {
	buf  bytes.Buffer
	Max  int
	over bool
}

func (b *CappedBuffer) Write(p []byte) (int, error) {
	room := b.Max - b.buf.Len()
	if room <= 0 {
		b.over = true
		return len(p), nil
	}
	if len(p) > room {
		b.buf.Write(p[:room])
		b.over = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *CappedBuffer) String() string { return b.buf.String() }

// Over 报告是否曾有字节落在 Max 之外。
func (b *CappedBuffer) Over() bool { return b.over }
