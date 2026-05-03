package util

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"
)

// crashLogDir is the directory where crash logs are stored.
// Defaults to ~/.codeactor/logs/crash/ .
func crashLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codeactor", "logs", "crash"), nil
}

// RecoverPanic should be called as the first deferred function in main().
// It catches any panic, writes a detailed crash report to a date-stamped log file,
// and re-panics to preserve the original behavior.
//
// Usage:
//
//	func main() {
//	    defer util.RecoverPanic()
//	    // ... rest of main
//	}
func RecoverPanic() {
	r := recover()
	if r == nil {
		return
	}

	report := buildCrashReport(r)

	dir, err := crashLogDir()
	if err != nil {
		// Last resort: write to stderr
		fmt.Fprintf(os.Stderr, "FATAL PANIC (cannot determine log dir):\n%s\n", report)
		panic(r)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL PANIC (cannot create crash dir %s):\n%s\n", dir, report)
		panic(r)
	}

	filename := filepath.Join(dir, fmt.Sprintf("crash-%s.log", time.Now().Format("2006-01-02")))
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL PANIC (cannot open crash log %s):\n%s\n", filename, report)
		panic(r)
	}
	defer f.Close()

	if _, err := f.WriteString(report); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL PANIC (cannot write crash log):\n%s\n", report)
	}

	// Always write to stderr as well so the user sees it
	fmt.Fprintf(os.Stderr, "\n%s\n", report)

	// Re-panic to preserve original panic behavior
	panic(r)
}

func buildCrashReport(recovered interface{}) string {
	now := time.Now()
	stack := debug.Stack()

	return fmt.Sprintf(
		"================================================================================\n"+
			"CRASH REPORT\n"+
			"================================================================================\n"+
			"Timestamp  : %s\n"+
			"Go Version : %s\n"+
			"OS/Arch    : %s/%s\n"+
			"Panic      : %v\n"+
			"--------------------------------------------------------------------------------\n"+
			"Stack Trace:\n"+
			"%s\n"+
			"================================================================================\n\n",
		now.Format(time.RFC3339),
		runtime.Version(),
		runtime.GOOS, runtime.GOARCH,
		recovered,
		string(stack),
	)
}
