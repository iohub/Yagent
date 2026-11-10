package embedbin

import (
	"os"
	"testing"
)

// TestFilePerm 钉住"脚本类产物落盘 0700"这条 P10 约束。
//
// 为什么值得一条测试：权限只影响**安全**，不影响功能 —— 把所有文件写成 0755 时
// 全部单测照样绿，只有"同组用户能读到内嵌的生成策略"这一件事悄悄变了。
// plasma.pyz 里是完整的生成提示词，所以它必须与 workdir 的 0700 同一取向。
func TestFilePerm(t *testing.T) {
	cases := []struct {
		name string
		want os.FileMode
	}{
		{"plasma.pyz", 0o700}, // zipapp：解释执行，不需要 exec 位给他人
		{"helper.py", 0o700},  // 脚本：同上
		{"rg", 0o755},         // 原生可执行：保持既有权限
		{"browser-control", 0o755},
		{"PLASMA.PYZ", 0o700}, // 扩展名大小写不该改变判据（macOS 上 .PYZ 是常见笔误）
	}
	for _, c := range cases {
		if got := filePerm(c.name); got != c.want {
			t.Errorf("filePerm(%q) = %04o，期望 %04o", c.name, got, c.want)
		}
	}
}
