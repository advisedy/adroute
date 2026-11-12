package adroute

import (
	"strings"
	"testing"
)

func TestRepairPromptAcceptsWindowsLineEndings(t *testing.T) {
	s := newTestStore(t)
	document := []byte("Instructions\r\n```text\r\nProject: {{adroute 项目绝对路径}}\r\n```\r\n")
	result, err := GenerateRepairPromptFromTemplate(document, t.TempDir(), s, NewAdapterDirectory(s), nil, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Prompt, "Project: ") || strings.Contains(result.Prompt, "{{") {
		t.Fatalf("template was not rendered: %q", result.Prompt)
	}
}
