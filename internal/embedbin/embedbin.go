package embedbin

import (
	"embed"
	"fmt"
	"io/fs"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"log/slog"
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

	// 读取已安装的版本信息
	existingChecksums, err := readVersionFile(binDir)
	if err != nil {
		slog.Warn("Failed to parse version file, will reinstall all binaries", "error", err)
	}

	// 创建 checksums 副本，用于最终写入
	checksums := make(map[string]string)
	if existingChecksums != nil {
		for k, v := range existingChecksums {
			checksums[k] = v
		}
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

		// 计算嵌入文件的 MD5
		currentMD5 := computeMD5(data)
		destPath := filepath.Join(binDir, name)

		// 检查目标文件是否存在
		fileExists := true
		if _, statErr := os.Stat(destPath); os.IsNotExist(statErr) {
			fileExists = false
		}

		// 判断是否需要写入：文件不存在 或 MD5 不一致
		needWrite := false
		if !fileExists {
			needWrite = true
		} else if existingChecksums == nil {
			// version.json 不存在，需要写入
			needWrite = true
		} else if existingChecksums[name] != currentMD5 {
			// MD5 不一致，需要写入
			needWrite = true
		}

		if needWrite {
			if err := os.WriteFile(destPath, data, 0755); err != nil {
				return "", fmt.Errorf("failed to write binary %s: %w", name, err)
			}
			slog.Info("Updated binary", "name", name)
		}

		// 更新 checksums
		checksums[name] = currentMD5
	}

	// 写入更新后的版本信息
	if err := writeVersionFile(binDir, checksums); err != nil {
		slog.Warn("Failed to write version file", "error", err)
	}

	return binDir, nil
}

// computeMD5 calculates the MD5 hash of the given data and returns it as a hex string.
func computeMD5(data []byte) string {
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}

// readVersionFile reads the version.json file from binDir and returns the checksums map.
// Returns an empty map (nil error) if the file doesn't exist.
func readVersionFile(binDir string) (map[string]string, error) {
	versionFile := filepath.Join(binDir, "version.json")

	data, err := os.ReadFile(versionFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read version file: %w", err)
	}

	var version struct {
		Binaries map[string]string `json:"binaries"`
	}

	if err := json.Unmarshal(data, &version); err != nil {
		slog.Warn("Failed to parse version file, ignoring", "file", versionFile, "error", err)
		return nil, nil
	}

	return version.Binaries, nil
}

// writeVersionFile writes the checksums map to binDir/version.json.
func writeVersionFile(binDir string, checksums map[string]string) error {
	versionFile := filepath.Join(binDir, "version.json")

	version := struct {
		Binaries map[string]string `json:"binaries"`
	}{
		Binaries: checksums,
	}

	data, err := json.MarshalIndent(version, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal version data: %w", err)
	}

	return os.WriteFile(versionFile, data, 0644)
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
