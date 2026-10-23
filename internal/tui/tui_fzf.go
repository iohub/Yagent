package tui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"yagent/internal/embedbin"
	"yagent/internal/supervise"

	tea "charm.land/bubbletea/v2"
)

// Empty path means the user cancelled (Esc/Ctrl-C).
type fzfFileSelectedMsg struct {
	path string
}

// pipeCommand captures fzf's stdout through a pipe because tea.Exec calls SetStdout(os.Stdout), which would override our redirection.
type pipeCommand struct {
	cmd *exec.Cmd
	pw  *io.PipeWriter
}

func (w *pipeCommand) Run() error {
	w.cmd.Stdout = w.pw
	return w.cmd.Run()
}

func (w *pipeCommand) SetStdin(r io.Reader) {
	w.cmd.Stdin = r
}

func (w *pipeCommand) SetStdout(io.Writer) {
	// Intentionally ignore - we always use our pipe
}

func (w *pipeCommand) SetStderr(w2 io.Writer) {
	w.cmd.Stderr = w2
}

// Security: native exec.Command with []string args instead of a shell string — projectDir may contain ', ", $, etc.
func runFzfCmd(projectDir string) tea.Cmd {
	projectDir = filepath.Clean(projectDir)
	if !filepath.IsAbs(projectDir) {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			projectDir = "."
		}
	}

	fzfBin, err := embedbin.BinPath("fzf")
	if err != nil {
		fzfBin = "fzf" // fallback to system fzf
	}

	if _, statErr := os.Stat(fzfBin); statErr != nil {
		fzfBin = "fzf"
	}

	findArgs := []string{
		projectDir,
		"-type", "f",
		"-not", "-path", "*/.git/*",
		"-not", "-path", "*/node_modules/*",
		"-not", "-path", "*/target/*",
		"-not", "-path", "*/dist/*",
		"-not", "-path", "*/.next/*",
		"-not", "-path", "*/.expo/*",
		"-not", "-path", "*/.yarn/*",
		"-not", "-path", "*/.pnpm-store/*",
		"-not", "-path", "*/vendor/*",
		"-not", "-path", "*/__pycache__/*",
		"-not", "-path", "*/.venv/*",
		"-not", "-path", "*/venv/*",
		"-not", "-path", "*/env/*",
		"-not", "-path", "*/.DS_Store",
	}
	findCmd := exec.Command("find", findArgs...)
	fileList, err := findCmd.Output()
	if err != nil {
		return func() tea.Msg { return fzfFileSelectedMsg{path: ""} }
	}

	pr, pw := io.Pipe()

	ch := make(chan string, 1)

	// Read output in a goroutine（消费方靠 <-ch 收，close(ch) 就是它的 join）
	supervise.Root().Go("tui/fzf-read", func(context.Context) {
		defer close(ch)
		data, err := io.ReadAll(pr)
		if err != nil || len(data) == 0 {
			return
		}
		ch <- strings.TrimSpace(string(data))
	})

	fzfArgs := []string{
		"--height=40%",
		"--layout=reverse",
		"--border",
		"--no-multi", // 只允许选择单个文件
		"--preview", "head -20 {} 2>/dev/null || cat {} 2>/dev/null || echo [binary file]",
		"--preview-window", "right:60%:wrap",
	}
	fzfCmd := exec.Command(fzfBin, fzfArgs...)
	fzfCmd.Stdin = strings.NewReader(string(fileList))

	execCmd := &pipeCommand{cmd: fzfCmd, pw: pw}

	return tea.Exec(execCmd, func(err error) tea.Msg {
		// Close the write end first to signal EOF to the reader goroutine
		pw.Close()

		selectedPath := <-ch

		pr.Close()

		if err != nil {
			return fzfFileSelectedMsg{path: ""}
		}

		if selectedPath == "" {
			return fzfFileSelectedMsg{path: ""}
		}

		relPath, relErr := filepath.Rel(projectDir, selectedPath)
		if relErr != nil {
			relPath = selectedPath
		}

		return fzfFileSelectedMsg{path: relPath}
	})
}
