package adroute

// ReasoningLevels is the single fixed set of reasoning levels adroute publishes
// to every external client. Codex receives it as the model catalog's
// supported_reasoning_levels; OpenCode receives it as each model's variants.
//
// adroute forwards whatever the client sends as reasoning.effort unchanged, so
// this is a shared default menu, not a per-upstream capability claim. An
// upstream that rejects a level returns an error the user can see, and the
// entry can then be edited by hand.
//
// Keeping one list is the point: a level must never exist for one client and be
// missing for another.
var ReasoningLevels = []string{"low", "medium", "high", "xhigh", "max"}

// defaultReasoningLevel is what a client uses when the user has not picked a
// level. "none" keeps the previous behavior of sending no reasoning effort at
// all, so publishing a menu never silently raises cost or latency.
const defaultReasoningLevel = "none"

// codexReasoningLevels renders ReasoningLevels for the Codex model catalog.
func codexReasoningLevels() []map[string]string {
	out := make([]map[string]string, 0, len(ReasoningLevels))
	for _, level := range ReasoningLevels {
		out = append(out, map[string]string{"effort": level, "description": level})
	}
	return out
}

// openCodeReasoningVariants renders ReasoningLevels as OpenCode model variants.
// OpenCode sends the chosen id to the gateway as reasoning.effort.
func openCodeReasoningVariants() []any {
	out := make([]any, 0, len(ReasoningLevels))
	for _, level := range ReasoningLevels {
		out = append(out, map[string]any{"id": level, "settings": map[string]any{"reasoningEffort": level}})
	}
	return out
}
