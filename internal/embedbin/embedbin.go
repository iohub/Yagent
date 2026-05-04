package embedbin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ExtractBinaries extracts embedded binaries from the given embed.FS to ~/.codeactor/bin/
// subDir is the path within the FS where the binary files are located (e.g. "dist/bin").
func ExtractBinaries(binFS embed.FS, subDir string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	binDir := filepath.Join(homeDir, ".codeactor", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create bin directory: %w", err)
	}

	entries, err := fs.ReadDir(binFS, subDir)
	if err != nil {
		return "", fmt.Errorf("failed to read embedded %s: %w", subDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !isExecutableName(name) {
			continue
		}

		data, err := binFS.ReadFile(filepath.Join(subDir, name))
		if err != nil {
			return "", fmt.Errorf("failed to read embedded binary %s: %w", name, err)
		}

		destPath := filepath.Join(binDir, name)
		if err := os.WriteFile(destPath, data, 0755); err != nil {
			return "", fmt.Errorf("failed to write binary %s: %w", name, err)
		}
	}

	return binDir, nil
}

// isExecutableName checks if a filename looks like an executable binary
func isExecutableName(name string) bool {
	if len(name) == 1 {
		return false
	}
	if name[0] == '.' {
		return false
	}
	return true
}

// BinPath returns the full path to an extracted binary in ~/.codeactor/bin/
func BinPath(name string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".codeactor", "bin", name), nil
}
