package adroute

import (
	"fmt"
	"reflect"
)

// canonicalFinalOutput mirrors snapshots.js: tools reconcile only by existing
// item/call identity, while regenerated non-tool IDs require one unique exact
// completed-content match. Array position is never identity evidence.
func canonicalFinalOutput(output []any, ids map[string]*toolCallState, itemIndexes map[string]int, completed map[string]map[string]any) ([]any, error) {
	finalIDs, callIDs := map[string]bool{}, map[string]bool{}
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok || str(item, "type") == "" {
			return nil, fmt.Errorf("invalid final output item")
		}
		if v, exists := item["id"]; exists {
			id, ok := v.(string)
			if !ok || id == "" || finalIDs[id] {
				return nil, fmt.Errorf("duplicate or invalid final item ID")
			}
			finalIDs[id] = true
		}
		if str(item, "type") == "function_call" {
			if v, exists := item["call_id"]; exists {
				cid, ok := v.(string)
				if !ok || cid == "" || callIDs[cid] {
					return nil, fmt.Errorf("duplicate or invalid final call ID")
				}
				callIDs[cid] = true
			}
		}
	}
	used := map[string]bool{}
	for _, raw := range output {
		item := raw.(map[string]any)
		id := str(item, "id")
		if str(item, "type") != "function_call" && itemIndexesHas(itemIndexes, id) {
			used[id] = true
		}
	}
	canonicalIDs := map[string]bool{}
	result := make([]any, 0, len(output))
	for _, raw := range output {
		item := raw.(map[string]any)
		typ, id, cid := str(item, "type"), str(item, "id"), str(item, "call_id")
		next := copyMap(item)
		if typ == "function_call" {
			if itemIndexesHas(itemIndexes, id) || itemIndexesHas(itemIndexes, cid) {
				return nil, fmt.Errorf("tool identity collides with streamed non-tool")
			}
			byID, byCall := ids[id], ids[cid]
			if byID != nil && byCall != nil && byID != byCall {
				return nil, fmt.Errorf("final tool IDs point to different calls")
			}
			if byCall != nil && byCall.callID != cid {
				return nil, fmt.Errorf("final call ID conflicts with stream identity")
			}
			known := byID
			if byCall != nil {
				known = byCall
			}
			if known != nil && known.id != "" && id != known.id {
				if id == "" {
					return nil, fmt.Errorf("final tool item ID missing")
				}
				next["id"] = known.id
			}
		} else {
			if ids[id] != nil {
				return nil, fmt.Errorf("final non-tool uses tool identity")
			}
			if previous := completed[id]; previous != nil && str(previous, "type") != typ {
				return nil, fmt.Errorf("non-tool item type changed")
			}
			if !itemIndexesHas(itemIndexes, id) {
				matches := []string{}
				for previousID, previous := range completed {
					if used[previousID] || str(previous, "type") != typ {
						continue
					}
					if sameNonToolContent(previous, item) {
						matches = append(matches, previousID)
					}
				}
				if len(matches) > 1 || len(matches) == 0 && len(itemIndexes) > 0 {
					return nil, fmt.Errorf("non-tool snapshot identity ambiguous")
				}
				if len(matches) == 1 {
					next["id"] = matches[0]
					used[matches[0]] = true
				}
			}
		}
		if newID := str(next, "id"); newID != "" {
			if canonicalIDs[newID] {
				return nil, fmt.Errorf("canonical final IDs collide")
			}
			canonicalIDs[newID] = true
		}
		result = append(result, next)
	}
	return result, nil
}
func itemIndexesHas(indexes map[string]int, key string) bool {
	if key == "" {
		return false
	}
	_, ok := indexes[key]
	return ok
}
func copyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
func sameNonToolContent(a, b map[string]any) bool {
	x, y := copyMap(a), copyMap(b)
	delete(x, "id")
	delete(x, "status")
	delete(y, "id")
	delete(y, "status")
	return reflect.DeepEqual(x, y)
}
