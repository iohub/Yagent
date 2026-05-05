package diff

import (
	"github.com/aymanbagabas/go-udiff"
)

// GenerateUnifiedDiff generates a unified diff string between old and new content.
// If the content is identical, returns an empty string.
func GenerateUnifiedDiff(filePath, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	oldLabel := "a/" + filePath
	newLabel := "b/" + filePath
	return udiff.Unified(oldLabel, newLabel, oldContent, newContent)
}
