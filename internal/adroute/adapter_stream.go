package adroute

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// RepairStreamOptions bounds framing, retained state, arguments, calls and
// response/tool completion time. Zero fields take the protocol defaults.
type RepairStreamOptions struct {
	FrameBytes    int
	ArgumentBytes int
	StateBytes    int
	Calls         int
	Wait          time.Duration
	ResponseWait  time.Duration
}

func (o RepairStreamOptions) defaults() RepairStreamOptions {
	if o.FrameBytes <= 0 {
		o.FrameBytes = AdapterFrameLimit
	}
	if o.ArgumentBytes <= 0 {
		o.ArgumentBytes = 1024 * 1024
	}
	if o.StateBytes <= 0 {
		o.StateBytes = AdapterStateLimit
	}
	if o.Calls <= 0 {
		o.Calls = AdapterCallLimit
	}
	if o.Wait <= 0 {
		o.Wait = 30 * time.Second
	}
	if o.ResponseWait <= 0 {
		o.ResponseWait = 5 * time.Minute
	}
	return o
}

// RepairStream frames incrementally. v1/v2 forward ordinary events immediately
// while withholding all executable tool events until final validation. v3
// emits no bytes until the entire response, channel envelopes and snapshots
// have been validated. The input must be closable to enforce deadlines.
func RepairStream(pack AdapterPackage, input io.ReadCloser, output io.Writer, options RepairStreamOptions) error {
	if err := ValidateAdapterPackage(pack); err != nil {
		return err
	}
	if input == nil || output == nil {
		return appError("stream input and output are required", 400, "INVALID_REQUEST")
	}
	limits := options.defaults()
	v3 := pack.Engine == "responses-tools-v3"
	var timerMu sync.Mutex
	var timer *time.Timer
	timeoutCode := ""
	armTimer := func(d time.Duration, code string) {
		timerMu.Lock()
		defer timerMu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timeoutCode = code
		timer = time.AfterFunc(d, func() { _ = input.Close() })
	}
	stopTimer := func() {
		timerMu.Lock()
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		timeoutCode = ""
		timerMu.Unlock()
	}
	defer stopTimer()
	defer input.Close()
	armTimer(limits.ResponseWait, "ADAPTER_WAIT_TIMEOUT")
	all := []sseEvent{}
	comments := [][]byte{}
	var pending []byte
	indexMap := map[int]int{}
	sequence := 0
	stateBytes, toolBytes, callCount := 0, 0, 0
	seenCalls := map[string]bool{}
	argumentDeltas := map[string]int{}
	terminal := false
	write := func(b []byte) error { _, err := output.Write(b); return err }
	emitRaw := func(ev sseEvent) error {
		m := jsonMap(ev.Data)
		if m == nil {
			return nil
		}
		if i, ok := num(m, "output_index"); ok {
			slot, found := indexMap[i]
			if !found {
				slot = len(indexMap)
				indexMap[i] = slot
			}
			m["output_index"] = slot
		}
		m["sequence_number"] = sequence
		sequence++
		var b bytes.Buffer
		if ev.Event != "" {
			fmt.Fprintf(&b, "event: %s\n", ev.Event)
		}
		fmt.Fprintf(&b, "data: %s\n\n", jsonBytes(m))
		return write(b.Bytes())
	}
	emitValidated := func(events []sseEvent, allEvents bool) error {
		for _, ev := range events {
			m := jsonMap(ev.Data)
			if m == nil {
				continue
			}
			typ := str(m, "type")
			if !allEvents && !isToolEvent(m) && typ != "response.completed" && typ != "response.failed" && typ != "response.incomplete" && typ != "error" {
				continue
			}
			// RepairEvents has already applied the response-local output-index
			// bijection; remapping these compact indices a second time is unsafe.
			m["sequence_number"] = sequence
			sequence++
			var b bytes.Buffer
			if ev.Event != "" {
				fmt.Fprintf(&b, "event: %s\n", ev.Event)
			}
			fmt.Fprintf(&b, "data: %s\n\n", jsonBytes(m))
			if err := write(b.Bytes()); err != nil {
				return err
			}
		}
		return nil
	}
	consume := func(block, wire []byte) error {
		ev, hasData, err := parseSSEBlock(block)
		if err != nil {
			return appError("invalid SSE event JSON", 502, "ADAPTER_INVALID_STREAM")
		}
		if !hasData {
			if v3 {
				comments = append(comments, append([]byte(nil), wire...))
				return nil
			}
			return write(wire)
		}
		if string(ev.Data) == "[DONE]" {
			if !terminal {
				return appError("DONE before terminal response", 502, "ADAPTER_INVALID_STREAM")
			}
			if v3 {
				comments = append(comments, append([]byte(nil), wire...))
				return nil
			}
			return write(wire)
		}
		m := jsonMap(ev.Data)
		typ := str(m, "type")
		if terminal {
			return appError("data after terminal response", 502, "ADAPTER_INVALID_STREAM")
		}
		all = append(all, ev)
		retained := v3 || isToolEvent(m) || pack.Engine == "responses-tools-v2" && (typ == "response.output_item.added" || typ == "response.output_item.done" || typ == "response.completed")
		if retained {
			if v3 {
				stateBytes += len(wire)
			} else {
				stateBytes += len(ev.Data)
			}
		}
		if stateBytes > limits.StateBytes && (v3 || pack.Engine == "responses-tools-v2") {
			return appError("retained adapter state exceeds limit", 502, "ADAPTER_LIMIT")
		}
		if isToolEvent(m) {
			toolBytes += len(ev.Data)
			if toolBytes > limits.StateBytes {
				return appError("retained tool state exceeds limit", 502, "ADAPTER_LIMIT")
			}
			identity := str(m, "item_id")
			if identity == "" {
				identity = str(nested(m, "item"), "id")
			}
			if identity == "" {
				identity = str(m, "call_id")
			}
			if identity == "" {
				identity = str(nested(m, "item"), "call_id")
			}
			if i, ok := num(m, "output_index"); ok {
				identity = fmt.Sprintf("index:%d", i)
			}
			if identity == "" {
				identity = fmt.Sprintf("anonymous:%d", callCount)
			}
			if !seenCalls[identity] {
				seenCalls[identity] = true
				callCount++
			}
			if callCount > limits.Calls {
				return appError("pending call count exceeds limit", 502, "ADAPTER_LIMIT")
			}
			argKey := identity
			if delta, ok := m["delta"].(string); ok {
				argumentDeltas[argKey] += len(delta)
			}
			argLength := argumentDeltas[argKey]
			if value, ok := m["arguments"].(string); ok && len(value) > argLength {
				argLength = len(value)
			}
			if value, ok := nested(m, "item")["arguments"].(string); ok && len(value) > argLength {
				argLength = len(value)
			}
			if argLength > limits.ArgumentBytes {
				return appError("tool arguments exceed limit", 502, "ADAPTER_LIMIT")
			}
			if !v3 {
				armTimer(limits.Wait, "ADAPTER_WAIT_TIMEOUT")
			}
		}
		if typ == "response.completed" || typ == "response.failed" || typ == "response.incomplete" || typ == "error" {
			terminal = true
			stopTimer()
			transformed, e := RepairEvents(pack, all)
			if e != nil {
				code := "ADAPTER_INVALID_STREAM"
				if stringsContainsAmbiguous(e.Error()) {
					code = "ADAPTER_AMBIGUOUS_CALL"
				}
				return appError("adapter rejected ambiguous or incomplete response", 502, code)
			}
			if v3 {
				for _, raw := range comments {
					if e = write(raw); e != nil {
						return e
					}
				}
				return emitValidated(transformed, true)
			}
			return emitValidated(transformed, false)
		}
		if v3 {
			return nil
		}
		if !isToolEvent(m) {
			return emitRaw(ev)
		}
		return nil
	}
	buf := make([]byte, 16*1024)
	for {
		n, err := input.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				lf := bytes.Index(pending, []byte("\n\n"))
				cr := bytes.Index(pending, []byte("\r\n\r\n"))
				pos, width := lf, 2
				if cr >= 0 && (pos < 0 || cr < pos) {
					pos, width = cr, 4
				}
				if pos < 0 {
					if len(pending) > limits.FrameBytes {
						return appError("SSE frame exceeds limit", 502, "ADAPTER_LIMIT")
					}
					break
				}
				block := append([]byte(nil), pending[:pos]...)
				wire := append([]byte(nil), pending[:pos+width]...)
				pending = append([]byte(nil), pending[pos+width:]...)
				if len(block) > limits.FrameBytes {
					return appError("SSE frame exceeds limit", 502, "ADAPTER_LIMIT")
				}
				if e := consume(block, wire); e != nil {
					return e
				}
			}
		}
		if err == io.EOF {
			if len(bytes.TrimSpace(pending)) > 0 {
				block := bytes.TrimSpace(pending)
				if len(block) > limits.FrameBytes {
					return appError("SSE frame exceeds limit", 502, "ADAPTER_LIMIT")
				}
				if e := consume(block, append(append([]byte(nil), block...), '\n', '\n')); e != nil {
					return e
				}
			}
			break
		}
		if err != nil {
			timerMu.Lock()
			code := timeoutCode
			timerMu.Unlock()
			if code != "" {
				return appError("adapter response deadline exceeded", 504, code)
			}
			return err
		}
	}
	if !terminal {
		return appError("upstream ended before terminal Responses event", 502, "ADAPTER_INVALID_STREAM")
	}
	return nil
}
func stringsContainsAmbiguous(s string) bool {
	return bytes.Contains([]byte(s), []byte("ambiguous")) || bytes.Contains([]byte(s), []byte("conflict"))
}
func isToolEvent(m map[string]any) bool {
	typ := str(m, "type")
	if typ == "response.function_call_arguments.delta" || typ == "response.function_call_arguments.done" {
		return true
	}
	if typ == "response.output_item.added" || typ == "response.output_item.done" {
		return str(nested(m, "item"), "type") == "function_call"
	}
	return false
}

// RepairStreamBytes wraps the streaming engine for finite synthetic inputs.
func RepairStreamBytes(pack AdapterPackage, input []byte, options RepairStreamOptions) ([]byte, error) {
	var out bytes.Buffer
	if err := RepairStream(pack, io.NopCloser(bytes.NewReader(input)), &out, options); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ParseRepairedEvents is useful to callers and tests that need to inspect a
// complete result without reparsing framing details.
func ParseRepairedEvents(raw []byte) ([]json.RawMessage, error) {
	events, err := readSSE(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(events))
	for _, e := range events {
		if string(e.Data) != "[DONE]" {
			out = append(out, append(json.RawMessage(nil), e.Data...))
		}
	}
	return out, nil
}
