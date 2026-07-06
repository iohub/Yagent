package main

import (
	"fmt"
	"os"
	"os/exec"

	"codeactor/internal/tui/components"
	"codeactor/internal/tui/common"
)

func main() {
	styles := common.NewStyles(common.ThemeDark)

	// 中文演示
	d := components.NewConfirmDialog(styles, "SystemCommand",
		"rm -rf /tmp/test_cache",
		"此操作可能影响工作空间外的文件或系统环境",
		"req-auth-001", components.LanguageZh)
	d.SetBounds(80, 24)

	// 清屏
	cmd := exec.Command("clear")
	cmd.Stdout = os.Stdout
	cmd.Run()

	fmt.Println("\n━━━ 授权界面展示 (中文) ━━━\n")
	fmt.Print(d.View())
	fmt.Println("\n\n按 Enter 键继续...")
	fmt.Scanln()

	// 英文演示
	d2 := components.NewConfirmDialog(styles, "SystemCommand",
		"rm -rf /tmp/test_cache",
		"This operation may affect files outside the workspace",
		"req-auth-002", components.LanguageEn)
	d2.SetBounds(80, 24)

	cmd2 := exec.Command("clear")
	cmd2.Stdout = os.Stdout
	cmd2.Run()

	fmt.Println("\n━━━ Auth Dialog (English) ━━━\n")
	fmt.Print(d2.View())
	fmt.Println("\n\nPress Enter to exit...")
	fmt.Scanln()
}
