package adroute

import (
	"io/fs"
	"testing"
)

func TestEmbeddedFilesIncludeRepairPromptTemplate(t *testing.T) {
	if _, err := fs.ReadFile(EmbeddedFiles(), "docs/repair-agent-prompt.md"); err != nil {
		t.Fatalf("built executable assets must contain prompt template: %v", err)
	}
}
