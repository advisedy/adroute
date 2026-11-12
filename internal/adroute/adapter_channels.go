package adroute

import (
	"fmt"
	"strings"
)

const responseChannelBoundary = "<|close|>think<|sep|><|open|>response<|sep|>"

type channelTextSnapshot struct {
	object      map[string]any
	field       string
	placeholder bool
}
type channelTextRecord struct {
	id               string
	index            int
	snapshots        []channelTextSnapshot
	deltas           []map[string]any
	deltaIndexes     []int
	original, answer string
	repaired         bool
}

// normalizeChannelResponse ports channels.js: collect every representation,
// prove all snapshots agree, validate every repair, then mutate the isolated
// event copies in one commit pass.
func normalizeChannelResponse(input []sseEvent) ([]sseEvent, error) {
	records := map[string]*channelTextRecord{}
	record := func(id string, index int) (*channelTextRecord, error) {
		if id == "" || index < 0 {
			return nil, fmt.Errorf("ambiguous channel text identity")
		}
		key := fmt.Sprintf("%s\x00%d", id, index)
		if records[key] == nil {
			records[key] = &channelTextRecord{id: id, index: index}
		}
		return records[key], nil
	}
	snapshot := func(obj map[string]any, field, id string, index int, placeholder bool) error {
		value, ok := obj[field].(string)
		if !ok {
			return fmt.Errorf("ambiguous channel text snapshot")
		}
		r, e := record(id, index)
		if e != nil {
			return e
		}
		r.snapshots = append(r.snapshots, channelTextSnapshot{object: obj, field: field, placeholder: placeholder})
		_ = value
		return nil
	}
	collectItem := func(item map[string]any, placeholder bool) error {
		if str(item, "type") != "message" {
			return nil
		}
		id := str(item, "id")
		parts := arr(item["content"])
		var textParts strings.Builder
		for i, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("ambiguous channel content part")
			}
			if str(part, "type") == "output_text" {
				textParts.WriteString(str(part, "text"))
				if e := snapshot(part, "text", id, i, placeholder); e != nil {
					return e
				}
			}
		}
		joined := textParts.String()
		if strings.Contains(joined, responseChannelBoundary) {
			partMarkers := 0
			for _, raw := range parts {
				part, _ := raw.(map[string]any)
				if str(part, "type") == "output_text" {
					partMarkers += strings.Count(str(part, "text"), responseChannelBoundary)
				}
			}
			if partMarkers != strings.Count(joined, responseChannelBoundary) {
				return fmt.Errorf("ambiguous channel marker split across content parts")
			}
		}
		return nil
	}
	values := make([]map[string]any, len(input))
	for i, frame := range input {
		if string(frame.Data) == "[DONE]" {
			return nil, fmt.Errorf("ambiguous channel termination")
		}
		m := jsonMap(frame.Data)
		if m == nil {
			return nil, fmt.Errorf("ambiguous channel event")
		}
		values[i] = m
		if response, exists := m["response"]; exists {
			r, ok := response.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("ambiguous channel response snapshot")
			}
			if aggregate, exists := r["output_text"]; exists {
				text, ok := aggregate.(string)
				output, valid := r["output"].([]any)
				if !ok || !valid {
					return nil, fmt.Errorf("ambiguous channel aggregate output_text")
				}
				var joined strings.Builder
				for _, raw := range output {
					item, ok := raw.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("ambiguous channel aggregate output")
					}
					if str(item, "type") == "message" {
						for _, partRaw := range arr(item["content"]) {
							part, _ := partRaw.(map[string]any)
							if str(part, "type") == "output_text" {
								joined.WriteString(str(part, "text"))
							}
						}
					}
				}
				if text != joined.String() {
					return nil, fmt.Errorf("ambiguous channel aggregate snapshot conflict")
				}
			}
		}
		typ := str(m, "type")
		if typ == "response.output_text.delta" {
			delta, ok := m["delta"].(string)
			idx, valid := num(m, "content_index")
			if !ok || !valid {
				return nil, fmt.Errorf("ambiguous channel delta")
			}
			r, e := record(str(m, "item_id"), idx)
			if e != nil {
				return nil, e
			}
			_ = delta
			r.deltas = append(r.deltas, m)
			r.deltaIndexes = append(r.deltaIndexes, i)
		} else if typ == "response.output_text.done" {
			idx, ok := num(m, "content_index")
			if !ok {
				return nil, fmt.Errorf("ambiguous channel done identity")
			}
			if e := snapshot(m, "text", str(m, "item_id"), idx, false); e != nil {
				return nil, e
			}
		}
		if (typ == "response.content_part.added" || typ == "response.content_part.done") && str(nested(m, "part"), "type") == "output_text" {
			idx, ok := num(m, "content_index")
			if !ok {
				return nil, fmt.Errorf("ambiguous channel content part index")
			}
			if e := snapshot(nested(m, "part"), "text", str(m, "item_id"), idx, typ == "response.content_part.added"); e != nil {
				return nil, e
			}
		}
		if item := nested(m, "item"); item != nil {
			if e := collectItem(item, strings.HasSuffix(typ, ".added")); e != nil {
				return nil, e
			}
		}
		if response := nested(m, "response"); response != nil {
			placeholder := typ == "response.created"
			for _, raw := range arr(response["output"]) {
				item, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("ambiguous channel response output")
				}
				if e := collectItem(item, placeholder); e != nil {
					return nil, e
				}
			}
		}
	}
	clean := map[string]string{}
	repairedIDs := map[string]bool{}
	for key, r := range records {
		var vals []string
		for _, s := range r.snapshots {
			v := s.object[s.field].(string)
			if !s.placeholder || v != "" {
				vals = append(vals, v)
			}
		}
		if len(r.deltas) > 0 {
			var b strings.Builder
			for _, d := range r.deltas {
				b.WriteString(d["delta"].(string))
			}
			vals = append(vals, b.String())
		}
		if len(vals) == 0 {
			r.original = ""
		} else {
			r.original = vals[0]
			for _, v := range vals[1:] {
				if v != r.original {
					return nil, fmt.Errorf("ambiguous channel text snapshots conflict")
				}
			}
		}
		answer, e := channelAnswer(r.original)
		if e != nil {
			return nil, e
		}
		r.answer = answer
		r.repaired = answer != r.original
		clean[key] = answer
		if r.repaired {
			repairedIDs[r.id] = true
		}
	}
	for id := range repairedIDs {
		nonempty := 0
		for _, r := range records {
			if r.id == id && r.original != "" {
				nonempty++
			}
		}
		if nonempty != 1 {
			return nil, fmt.Errorf("ambiguous channel has multiple text parts")
		}
		for _, m := range values {
			if str(m, "type") == "response.output_text.annotation.added" && str(m, "item_id") == id {
				return nil, fmt.Errorf("ambiguous channel annotation offsets")
			}
		}
	}
	for _, r := range records {
		if !r.repaired {
			continue
		}
		for _, s := range r.snapshots {
			if channelHasData(s.object["annotations"]) || channelHasData(s.object["logprobs"]) {
				return nil, fmt.Errorf("ambiguous channel text offsets")
			}
		}
		for _, d := range r.deltas {
			if channelHasData(d["logprobs"]) {
				return nil, fmt.Errorf("ambiguous channel delta offsets")
			}
		}
	}
	// No mutation occurs before this point. Replace the first text delta with
	// the entire public answer and suppress later deltas, updating every snapshot.
	for i, m := range values {
		typ := str(m, "type")
		id := str(m, "item_id")
		idx, _ := num(m, "content_index")
		key := fmt.Sprintf("%s\x00%d", id, idx)
		if r := records[key]; r != nil && r.repaired {
			switch typ {
			case "response.output_text.delta":
				first := len(r.deltaIndexes) > 0 && r.deltaIndexes[0] == i
				if !first {
					continue
				}
				m["delta"] = r.answer
			case "response.output_text.done":
				m["text"] = r.answer
			case "response.content_part.added", "response.content_part.done":
				part := nested(m, "part")
				if !strings.HasSuffix(typ, ".added") || str(part, "text") != "" {
					part["text"] = r.answer
				}
			}
		}
		update := func(item map[string]any) {
			if str(item, "type") != "message" {
				return
			}
			for j, raw := range arr(item["content"]) {
				part, _ := raw.(map[string]any)
				key := fmt.Sprintf("%s\x00%d", str(item, "id"), j)
				if r := records[key]; r != nil && r.repaired {
					part["text"] = r.answer
				}
			}
		}
		if item := nested(m, "item"); item != nil {
			update(item)
		}
		if response := nested(m, "response"); response != nil {
			for _, raw := range arr(response["output"]) {
				item, _ := raw.(map[string]any)
				update(item)
			}
			if _, ok := response["output_text"]; ok {
				var b strings.Builder
				for _, raw := range arr(response["output"]) {
					item, _ := raw.(map[string]any)
					if str(item, "type") == "message" {
						for _, partRaw := range arr(item["content"]) {
							part, _ := partRaw.(map[string]any)
							if str(part, "type") == "output_text" {
								b.WriteString(str(part, "text"))
							}
						}
					}
				}
				response["output_text"] = b.String()
			}
		}
		input[i].Data = jsonBytes(m)
	}
	_ = clean
	return input, nil
}

func channelAnswer(text string) (string, error) {
	if !strings.Contains(text, "<|") {
		return text, nil
	}
	if strings.Count(text, responseChannelBoundary) != 1 {
		return "", fmt.Errorf("ambiguous channel marker")
	}
	parts := strings.SplitN(text, responseChannelBoundary, 2)
	prefix, suffix := parts[0], parts[1]
	trimPrefix, trimSuffix := strings.TrimSpace(prefix), strings.TrimSpace(suffix)
	if trimPrefix == "" || trimSuffix == "" || strings.Contains(prefix, "<|") || strings.Contains(suffix, "<|") || strings.Contains(prefix, "```") || openQuoted(prefix) || strings.HasSuffix(prefix, "`") || strings.HasSuffix(prefix, "\"") || strings.HasSuffix(prefix, "'") || strings.HasPrefix(suffix, "`") || strings.HasPrefix(suffix, "\"") || strings.HasPrefix(suffix, "'") {
		return "", fmt.Errorf("ambiguous channel quote or nested marker")
	}
	return suffix, nil
}
func channelHasData(v any) bool {
	switch x := v.(type) {
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case string:
		return x != ""
	default:
		return false
	}
}
