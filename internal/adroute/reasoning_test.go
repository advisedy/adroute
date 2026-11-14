package adroute

import (
	"strings"
	"testing"
)

// Every external client must be offered the same reasoning levels. The two
// renderings differ in shape but never in content, so a level added to one
// client cannot silently go missing from the other.
func TestReasoningLevelsAreSharedByEveryClient(t *testing.T) {
	if len(ReasoningLevels) == 0 {
		t.Fatal("no reasoning levels published")
	}
	seen := map[string]bool{}
	for _, level := range ReasoningLevels {
		if strings.TrimSpace(level) != level || level == "" {
			t.Fatalf("invalid level %q", level)
		}
		if seen[level] {
			t.Fatalf("duplicate level %q", level)
		}
		seen[level] = true
	}

	// Codex catalog: supported_reasoning_levels[].effort
	entry := catalogEntry("adroute/a/b", "B", 0, 0)
	codex, _ := entry["supported_reasoning_levels"].([]map[string]string)
	if len(codex) != len(ReasoningLevels) {
		t.Fatalf("codex levels = %#v", codex)
	}
	for i, level := range ReasoningLevels {
		if codex[i]["effort"] != level {
			t.Fatalf("codex level %d = %q, want %q", i, codex[i]["effort"], level)
		}
	}

	// OpenCode config: models[].variants[].id with reasoningEffort.
	variants := openCodeReasoningVariants()
	if len(variants) != len(ReasoningLevels) {
		t.Fatalf("opencode variants = %#v", variants)
	}
	for i, level := range ReasoningLevels {
		v, _ := variants[i].(map[string]any)
		settings, _ := v["settings"].(map[string]any)
		if v["id"] != level || settings["reasoningEffort"] != level {
			t.Fatalf("opencode variant %d = %#v, want %q", i, v, level)
		}
	}

	// The default must not promise a level the menu omits.
	if seen[defaultReasoningLevel] {
		t.Fatalf("default level %q must stay outside the published menu", defaultReasoningLevel)
	}
	if entry["default_reasoning_level"] != defaultReasoningLevel {
		t.Fatalf("catalog default = %#v", entry["default_reasoning_level"])
	}
}
