package tui

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"codeactor/internal/embedbin"

	tea "github.com/charmbracelet/bubbletea"
)

// fzfFileSelectedMsg is sent when the user selects a file in the fzf fuzzy finder.
// If path is empty, the user cancelled (Esc/Ctrl-C).
type fzfFileSelectedMsg struct {
	path string
}

// execCommand wraps *exec.Cmd to satisfy tea.ExecCommand interface.
// In bubbletea v1.3.4, tea.ExecCommand requires SetStdin, SetStdout, and SetStderr methods.
type execCommand struct {
	*exec.Cmd
}

func (e *execCommand) SetStdin(r io.Reader) {
	e.Cmd.Stdin = r
}

func (e *execCommand) SetStdout(w io.Writer) {
	e.Cmd.Stdout = w
}

func (e *execCommand) SetStderr(w io.Writer) {
	e.Cmd.Stderr = w
}

// pipeCommand wraps *exec.Cmd and uses a pipe to capture fzf's stdout.
// This is needed because tea.Exec will call SetStdout(os.Stdout), which
// would override our shell redirection.
type pipeCommand struct {
	cmd *exec.Cmd
	pw  *io.PipeWriter
}

func (w *pipeCommand) Run() error {
	// Always set stdout to the pipe, regardless of what tea.Exec set
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

// runFzfCmd returns a Command that suspends the TUI, shows an fzf file picker,
// and returns the selected file path as fzfFileSelectedMsg.
//
// Security: Uses native exec.Command with []string args instead of shell
// string concatenation to prevent shell injection when projectDir contains
// special characters like ', ", $, etc.
func runFzfCmd(projectDir string) tea.Cmd {
	fzfBin, err := embedbin.BinPath("fzf")
	if err != nil {
		fzfBin = "fzf" // fallback to system fzf
	}

	// Verify fzf binary exists
	if _, statErr := os.Stat(fzfBin); statErr != nil {
		fzfBin = "fzf"
	}

	// Step 1: Run find to get file list (no shell, safe argument passing)
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
		// Return empty result on find error
		return func() tea.Msg { return fzfFileSelectedMsg{path: ""} }
	}

	// Step 2: Create pipe to capture fzf's stdout (selected file path)
	pr, pw := io.Pipe()

	// Channel to receive the selected path
	ch := make(chan string, 1)

	// Read output in a goroutine
	go func() {
		defer close(ch)
		buf := make([]byte, 4096)
		n, err := pr.Read(buf)
		if err != nil || n == 0 {
			return
		}
		ch <- strings.TrimSpace(string(buf[:n]))
	}()

	// Step 3: Build fzf command (no shell, safe args)
	fzfArgs := []string{
		"--height=40%",
		"--layout=reverse",
		"--border",
		"--preview", "head -20 {} 2>/dev/null || cat {} 2>/dev/null || echo [binary file]",
		"--preview-window", "right:60%:wrap",
	}
	fzfCmd := exec.Command(fzfBin, fzfArgs...)
	fzfCmd.Stdin = strings.NewReader(string(fileList))

	execCmd := &pipeCommand{cmd: fzfCmd, pw: pw}

	return tea.Exec(execCmd, func(err error) tea.Msg {
		// Close the pipe to signal EOF to the reader
		pw.Close()
		pr.Close()

		if err != nil {
			// User cancelled (Esc/Ctrl-C) or error
			return fzfFileSelectedMsg{path: ""}
		}

		selectedPath := <-ch
		if selectedPath == "" {
			return fzfFileSelectedMsg{path: ""}
		}

		// Convert to relative path
		relPath, err := filepath.Rel(projectDir, selectedPath)
		if err != nil {
			relPath = selectedPath
		}

		return fzfFileSelectedMsg{path: relPath}
	})
}
