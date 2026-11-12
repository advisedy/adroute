package adroute

// This file contains the deliberately data-only adapter runtime. Adapter input
// is never evaluated and replay never performs network or model inference.
import (
	"adroute/internal/platform"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const AdapterPackageLimit = 256 * 1024
const AdapterFrameLimit = 1024 * 1024
const AdapterStateLimit = 4 * 1024 * 1024
const AdapterCallLimit = 64
const AdapterFileLimit = 128

type AdapterOptions struct {
	LateName                     bool `json:"lateName"`
	SplitCalls                   bool `json:"splitCalls"`
	NeutralizeCursorInstructions bool `json:"neutralizeCursorInstructions,omitempty"`
}
type AdapterCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type AdapterFixture struct {
	Name          string            `json:"name"`
	Events        []json.RawMessage `json:"events"`
	ExpectedCalls []AdapterCall     `json:"expectedCalls"`
	ExpectedTexts []string          `json:"expectedTexts,omitempty"`
}

// Preserve the distinction between an absent v1/v2 expectedTexts field and an
// explicitly empty v3 array. encoding/json's omitempty otherwise drops [] and
// makes a valid v3 fixture fail strict replay after persistence/reload.
func (f AdapterFixture) MarshalJSON() ([]byte, error) {
	type fixture struct {
		Name          string            `json:"name"`
		Events        []json.RawMessage `json:"events"`
		ExpectedCalls []AdapterCall     `json:"expectedCalls"`
		ExpectedTexts *[]string         `json:"expectedTexts,omitempty"`
	}
	var texts *[]string
	if f.ExpectedTexts != nil {
		texts = &f.ExpectedTexts
	}
	return json.Marshal(fixture{f.Name, f.Events, f.ExpectedCalls, texts})
}

type AdapterPackage struct {
	SchemaVersion int              `json:"schemaVersion"`
	ID            string           `json:"id"`
	Version       string           `json:"version"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Engine        string           `json:"engine"`
	Options       AdapterOptions   `json:"options"`
	Fixtures      []AdapterFixture `json:"fixtures"`
}
type AdapterFixtureResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Calls   int    `json:"calls,omitempty"`
	Message string `json:"message,omitempty"`
}
type AdapterVerification struct {
	ConfigurationValid bool                   `json:"configurationValid"`
	ReplayPassed       bool                   `json:"replayPassed"`
	LiveVerified       bool                   `json:"liveVerified"`
	Fixtures           []AdapterFixtureResult `json:"fixtures"`
}

var adapterIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var adapterVersionPattern = regexp.MustCompile(`^\d{1,5}\.\d{1,5}\.\d{1,5}$`)

func PackageRef(p AdapterPackage) string { return p.ID + "@" + p.Version }
func PackageHash(p AdapterPackage) string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ValidateAdapterPackage(p AdapterPackage) error {
	b, e := json.Marshal(p)
	if e != nil || len(b) > AdapterPackageLimit {
		return fmt.Errorf("adapter package exceeds 256KB or is not serializable")
	}
	if p.SchemaVersion != 1 || !adapterIDPattern.MatchString(p.ID) || !adapterVersionPattern.MatchString(p.Version) || !adapterLabel(p.Name, 100) || !adapterLabel(p.Description, 2000) {
		return fmt.Errorf("invalid adapter package metadata")
	}
	if p.Engine != "responses-tools-v1" && p.Engine != "responses-tools-v2" && p.Engine != "responses-tools-v3" {
		return fmt.Errorf("unsupported adapter engine")
	}
	if len(p.Fixtures) == 0 || len(p.Fixtures) > 16 {
		return fmt.Errorf("adapter must contain 1-16 fixtures")
	}
	for _, f := range p.Fixtures {
		if !adapterLabel(f.Name, 100) || len(f.Events) == 0 || len(f.Events) > 256 || f.ExpectedCalls == nil || len(f.ExpectedCalls) > 64 || p.Engine == "responses-tools-v3" && f.ExpectedTexts == nil {
			return fmt.Errorf("invalid adapter fixture")
		}
		for _, ev := range f.Events {
			var x map[string]any
			if len(ev) > AdapterFrameLimit || json.Unmarshal(ev, &x) != nil || x["type"] == nil {
				return fmt.Errorf("invalid adapter event")
			}
			if _, ok := x["type"].(string); !ok {
				return fmt.Errorf("invalid adapter event type")
			}
		}
		for _, c := range f.ExpectedCalls {
			if !adapterLabel(c.CallID, AdapterPackageLimit) || !adapterLabel(c.Name, AdapterPackageLimit) || !adapterLabel(c.Arguments, AdapterPackageLimit) {
				return fmt.Errorf("invalid expected call")
			}
		}
		if p.Engine == "responses-tools-v3" && f.ExpectedTexts == nil {
			return fmt.Errorf("v3 fixture must include expectedTexts")
		}
	}
	return nil
}
func ValidateAdapterJSON(raw []byte) (AdapterPackage, error) {
	if len(raw) > AdapterPackageLimit {
		return AdapterPackage{}, fmt.Errorf("adapter package exceeds 256KB")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || adapterFields(obj) != nil {
		return AdapterPackage{}, fmt.Errorf("invalid adapter package fields")
	}
	var p AdapterPackage
	if err := json.Unmarshal(raw, &p); err != nil {
		return AdapterPackage{}, err
	}
	if err := ValidateAdapterPackage(p); err != nil {
		return AdapterPackage{}, err
	}
	return p, nil
}

type sseEvent struct {
	Event string
	Data  []byte
}

func readSSE(r io.Reader) ([]sseEvent, error) {
	br := bufio.NewReaderSize(r, 32*1024)
	out := []sseEvent{}
	var block []byte
	total := 0
	for {
		line, e := br.ReadBytes('\n')
		total += len(line)
		if total > AdapterStateLimit {
			return nil, fmt.Errorf("adapter state limit")
		}
		if len(block)+len(line) > AdapterFrameLimit {
			return nil, fmt.Errorf("adapter frame limit")
		}
		block = append(block, line...)
		if len(line) == 1 && line[0] == '\n' || bytes.Equal(line, []byte("\r\n")) {
			ev, ok, err := parseSSEBlock(block)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, ev)
			}
			block = nil
			total = 0
		}
		if e == io.EOF {
			if len(block) > 0 {
				ev, ok, err := parseSSEBlock(block)
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, ev)
				}
			}
			break
		}
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func parseSSEBlock(b []byte) (sseEvent, bool, error) {
	ev := sseEvent{}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var ds []string
	for _, l := range lines {
		if strings.HasPrefix(l, "event:") {
			ev.Event = strings.TrimSpace(strings.TrimPrefix(l, "event:"))
		}
		if strings.HasPrefix(l, "data:") {
			v := strings.TrimPrefix(l, "data:")
			ds = append(ds, strings.TrimPrefix(v, " "))
		}
	}
	if len(ds) == 0 {
		return ev, false, nil
	}
	data := strings.Join(ds, "\n")
	if data == "[DONE]" {
		ev.Data = []byte(data)
		return ev, true, nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(data), &m) != nil || m == nil {
		return ev, false, fmt.Errorf("invalid SSE JSON")
	}
	if typ, _ := m["type"].(string); ev.Event != "" && typ != "" && ev.Event != typ {
		return ev, false, fmt.Errorf("SSE event type mismatch")
	}
	ev.Data = []byte(data)
	return ev, true, nil
}
func jsonMap(b []byte) map[string]any                  { var m map[string]any; _ = json.Unmarshal(b, &m); return m }
func jsonBytes(v any) []byte                           { b, _ := json.Marshal(v); return b }
func nested(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }
func str(m map[string]any, k string) string            { v, _ := m[k].(string); return v }
func num(m map[string]any, k string) (int, bool) {
	switch v := m[k].(type) {
	case float64:
		return int(v), v >= 0 && v == float64(int(v))
	case int:
		return v, v >= 0
	case int64:
		return int(v), v >= 0 && int64(int(v)) == v
	default:
		return 0, false
	}
}

type toolCallState struct {
	id, callID, name string
	index            int
	indexed          bool
	delta            string
	finals           []string
	items            []map[string]any
	addedNamed       bool
	added            bool
}

// RepairEvents validates and repairs a single complete Responses stream. Tool
// events are withheld until the completed snapshot has been checked.
func RepairEvents(p AdapterPackage, input []sseEvent) ([]sseEvent, error) {
	if err := ValidateAdapterPackage(p); err != nil {
		return nil, err
	}
	if p.Engine == "responses-tools-v3" {
		validator := p
		validator.Engine = "responses-tools-v2"
		if _, err := RepairEvents(validator, input); err != nil {
			return nil, fmt.Errorf("ambiguous original v3 identity snapshot: %w", err)
		}
		normalized, err := normalizeChannelEvents(input)
		if err != nil {
			return nil, err
		}
		return RepairEvents(validator, normalized)
	}
	calls := []*toolCallState{}
	byIdentity := map[string]*toolCallState{}
	byIndex := map[int]*toolCallState{}
	itemIndexes := map[string]int{}
	completedItems := map[string]map[string]any{}
	outputIndexes := map[int]int{}
	passthrough := []sseEvent{}
	terminal := false
	rawBytes := 0
	sequence := 0
	outputIndex := func(index int) (int, error) {
		if index < 0 {
			return 0, fmt.Errorf("invalid output index")
		}
		if mapped, ok := outputIndexes[index]; ok {
			return mapped, nil
		}
		mapped := len(outputIndexes)
		outputIndexes[index] = mapped
		return mapped, nil
	}
	outputSlotCount := func() int { return len(outputIndexes) }
	emit := func(m map[string]any, event string) (sseEvent, error) {
		if _, exists := m["output_index"]; exists {
			index, ok := num(m, "output_index")
			if !ok {
				return sseEvent{}, fmt.Errorf("invalid output index")
			}
			mapped, e := outputIndex(index)
			if e != nil {
				return sseEvent{}, e
			}
			m["output_index"] = mapped
		}
		if it := nested(m, "item"); it != nil && str(it, "type") != "function_call" {
			if id := str(it, "id"); id != "" {
				if idx, ok := num(m, "output_index"); ok {
					itemIndexes[id] = idx
				}
				if str(m, "type") == "response.output_item.done" {
					completedItems[id] = it
				}
			}
		}
		m["sequence_number"] = sequence
		sequence++
		if event == "" {
			event = str(m, "type")
		}
		return sseEvent{Event: event, Data: jsonBytes(m)}, nil
	}
	get := func(m map[string]any, create bool) (*toolCallState, error) {
		it := nested(m, "item")
		keys := []string{str(m, "item_id"), str(it, "id"), str(m, "call_id"), str(it, "call_id")}
		var c *toolCallState
		for _, k := range keys {
			if k != "" && byIdentity[k] != nil {
				if c != nil && c != byIdentity[k] {
					return nil, fmt.Errorf("ambiguous tool identity")
				}
				c = byIdentity[k]
			}
		}
		if i, ok := num(m, "output_index"); ok && byIndex[i] != nil {
			if c != nil && c != byIndex[i] {
				return nil, fmt.Errorf("ambiguous tool index")
			}
			c = byIndex[i]
		}
		if c == nil && !create {
			return nil, nil
		}
		if c == nil {
			if len(calls) >= AdapterCallLimit {
				return nil, fmt.Errorf("call limit")
			}
			c = &toolCallState{}
			calls = append(calls, c)
		}
		for _, k := range keys {
			if k != "" {
				if old := byIdentity[k]; old != nil && old != c {
					return nil, fmt.Errorf("ambiguous tool identity")
				}
				byIdentity[k] = c
			}
		}
		if i, ok := num(m, "output_index"); ok {
			if c.indexed && c.index != i {
				return nil, fmt.Errorf("conflicting output index %d vs %d for %s/%s", c.index, i, str(m, "type"), strings.Join(keys, ","))
			}
			c.index = i
			c.indexed = true
			byIndex[i] = c
		}
		name := str(it, "name")
		if name == "" {
			name = str(m, "name")
		}
		if name != "" {
			if len(name) > 256 || strings.ContainsAny(name, "\x00\x20\x7f") || c.name != "" && c.name != name {
				return nil, fmt.Errorf("conflicting tool names %q vs %q at %s", c.name, name, str(m, "type"))
			}
			c.name = name
		}
		if len(it) > 0 {
			c.items = append(c.items, it)
			id := str(it, "id")
			cid := str(it, "call_id")
			if id != "" {
				finalSnapshot, _ := m["_final_snapshot"].(bool)
				if c.id != "" && c.id != id && !(finalSnapshot && p.Engine != "responses-tools-v1") {
					return nil, fmt.Errorf("conflicting item ids")
				}
				if c.id == "" {
					c.id = id
				}
			}
			if cid != "" {
				if c.callID != "" && c.callID != cid {
					return nil, fmt.Errorf("conflicting call ids")
				}
				c.callID = cid
			}
			if name != "" && str(it, "name") != "" && str(m, "type") == "response.output_item.added" {
				c.addedNamed = true
			}
		}
		return c, nil
	}
	for _, ev := range input {
		m := jsonMap(ev.Data)
		if string(ev.Data) == "[DONE]" {
			if !terminal {
				return nil, fmt.Errorf("DONE before terminal")
			}
			continue
		}
		typ := str(m, "type")
		if typ == "" {
			typ = ev.Event
			m["type"] = typ
		}
		if terminal {
			return nil, fmt.Errorf("data after terminal")
		}
		retain := isToolEvent(m) || p.Engine != "responses-tools-v1" && (typ == "response.completed" || typ == "response.output_item.added" || typ == "response.output_item.done")
		if retain {
			rawBytes += len(ev.Data)
			if rawBytes > AdapterStateLimit {
				return nil, fmt.Errorf("state limit")
			}
		}
		if typ == "response.output_item.added" || typ == "response.output_item.done" {
			if str(nested(m, "item"), "type") == "function_call" {
				c, e := get(m, true)
				if e != nil {
					return nil, e
				}
				if typ == "response.output_item.added" && c.added {
					return nil, fmt.Errorf("duplicate added")
				}
				if typ == "response.output_item.added" {
					c.added = true
				}
				continue
			}
		}
		if typ == "response.function_call_arguments.delta" || typ == "response.function_call_arguments.done" {
			c, e := get(m, true)
			if e != nil {
				return nil, e
			}
			if typ == "response.function_call_arguments.delta" {
				c.delta += str(m, "delta")
			} else {
				c.finals = append(c.finals, str(m, "arguments"))
			}
			if len(c.delta) > 1024*1024 {
				return nil, fmt.Errorf("argument limit")
			}
			continue
		}
		if typ == "response.completed" {
			terminal = true
			done, e := repairComplete(p, m, calls, get, byIdentity, itemIndexes, completedItems, outputIndex, outputSlotCount, emit)
			if e != nil {
				return nil, e
			}
			passthrough = append(passthrough, done...)
			continue
		}
		if typ == "response.failed" || typ == "response.incomplete" || typ == "error" {
			terminal = true
			out, e := emit(m, ev.Event)
			if e != nil {
				return nil, e
			}
			passthrough = append(passthrough, out)
			continue
		}
		out, e := emit(m, ev.Event)
		if e != nil {
			return nil, e
		}
		passthrough = append(passthrough, out)
	}
	if !terminal {
		return nil, fmt.Errorf("missing terminal response event")
	}
	return passthrough, nil
}

// normalizeChannelEvents is implemented in adapter_channels.go.
func normalizeChannelEvents(input []sseEvent) ([]sseEvent, error) {
	return normalizeChannelResponse(input)
}

func hasData(v any) bool {
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
func openQuoted(prefix string) bool {
	quote := rune(0)
	code := 0
	r := []rune(prefix)
	for i := 0; i < len(r); i++ {
		ch := r[i]
		if ch == '\\' {
			i++
			continue
		}
		if ch == '`' {
			n := 1
			for i+1 < len(r) && r[i+1] == '`' {
				i++
				n++
			}
			if code == 0 {
				code = n
			} else if code == n {
				code = 0
			}
			continue
		}
		if code > 0 {
			continue
		}
		if (ch == '\'' || ch == '’') && i > 0 && i+1 < len(r) && isLetterNumber(r[i-1]) && isLetterNumber(r[i+1]) {
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '“':
			quote = '”'
		case '‘':
			quote = '’'
		}
	}
	return quote != 0 || code != 0
}
func isLetterNumber(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}
func repairComplete(p AdapterPackage, m map[string]any, calls []*toolCallState, get func(map[string]any, bool) (*toolCallState, error), byIdentity map[string]*toolCallState, itemIndexes map[string]int, completedItems map[string]map[string]any, outputIndex func(int) (int, error), outputSlotCount func() int, emit func(map[string]any, string) (sseEvent, error)) ([]sseEvent, error) {
	r := nested(m, "response")
	if str(r, "status") != "completed" {
		return nil, fmt.Errorf("response not completed")
	}
	output, ok := r["output"].([]any)
	if !ok {
		if len(calls) > 0 {
			return nil, fmt.Errorf("missing final output")
		}
		return []sseEvent{{Event: str(m, "type"), Data: jsonBytes(m)}}, nil
	}
	if p.Engine != "responses-tools-v1" {
		var e error
		output, e = canonicalFinalOutput(output, byIdentity, itemIndexes, completedItems)
		if e != nil {
			return nil, e
		}
	}
	for i, x := range output {
		it, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid output")
		}
		if str(it, "type") == "function_call" {
			finalIndex := i
			if known := byIdentity[str(it, "call_id")]; known != nil && known.indexed {
				finalIndex = known.index
			} else if known := byIdentity[str(it, "id")]; known != nil && known.indexed {
				finalIndex = known.index
			}
			q := map[string]any{"item": it, "output_index": float64(finalIndex), "_final_snapshot": true}
			if _, e := get(q, true); e != nil {
				return nil, e
			}
		}
	}
	named, anon := []*toolCallState{}, []*toolCallState{}
	for _, c := range calls {
		if c.name != "" {
			named = append(named, c)
		} else {
			anon = append(anon, c)
		}
	}
	var ghost *toolCallState
	if len(anon) > 0 {
		if !p.Options.SplitCalls || len(named) != 1 || len(anon) != 1 {
			return nil, fmt.Errorf("ambiguous anonymous call")
		}
		ghost = anon[0]
	}
	canonical := map[*toolCallState]map[string]any{}
	canonicalOrder := []*toolCallState{}
	for _, c := range named {
		if !c.addedNamed && !p.Options.LateName {
			return nil, fmt.Errorf("late name disabled")
		}
		if c.id == "" || c.callID == "" || !c.indexed {
			return nil, fmt.Errorf("missing real call identity")
		}
		args, e := callArgs(c)
		if e != nil {
			return nil, e
		}
		if ghost != nil {
			g, e := callArgs(ghost)
			if e != nil {
				return nil, e
			}
			if args != "" && args != "{}" && g != "" && args != g {
				return nil, fmt.Errorf("conflicting ghost args")
			}
			if args == "" || args == "{}" {
				args = g
			}
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(args), &parsed) != nil || parsed == nil {
			return nil, fmt.Errorf("invalid arguments JSON object")
		}
		it := map[string]any{}
		for _, src := range c.items {
			for k, v := range src {
				it[k] = v
			}
		}
		it["type"] = "function_call"
		it["id"] = c.id
		it["call_id"] = c.callID
		it["name"] = c.name
		it["arguments"] = args
		it["status"] = "completed"
		canonical[c] = it
		canonicalOrder = append(canonicalOrder, c)
	}
	// Construct the complete final snapshot before emitting any executable call.
	finalSlots := map[int]any{}
	assign := func(index int, item any) error {
		slot, e := outputIndex(index)
		if e != nil {
			return e
		}
		if _, exists := finalSlots[slot]; exists {
			return fmt.Errorf("final output slot collision")
		}
		finalSlots[slot] = item
		return nil
	}
	for i, value := range output {
		item, _ := value.(map[string]any)
		if str(item, "type") == "function_call" {
			continue
		}
		original := i
		if id := str(item, "id"); itemIndexesHas(itemIndexes, id) {
			original = itemIndexes[id]
		}
		if e := assign(original, item); e != nil {
			return nil, e
		}
	}
	for _, c := range canonicalOrder {
		it := canonical[c]
		if e := assign(c.index, it); e != nil {
			return nil, e
		}
	}
	final := make([]any, outputSlotCount())
	for slot, item := range finalSlots {
		if slot < 0 || slot >= len(final) {
			return nil, fmt.Errorf("final output index out of range")
		}
		final[slot] = item
	}
	for _, item := range final {
		if item == nil {
			return nil, fmt.Errorf("final output slots are incomplete")
		}
	}
	r2 := map[string]any{}
	for k, v := range r {
		r2[k] = v
	}
	r2["output"] = final
	completed := map[string]any{}
	for k, v := range m {
		completed[k] = v
	}
	completed["response"] = r2
	out := []sseEvent{}
	for _, c := range calls {
		it, ok := canonical[c]
		if !ok {
			continue
		}
		add := map[string]any{}
		for k, v := range it {
			add[k] = v
		}
		add["status"] = "in_progress"
		add["arguments"] = ""
		for _, generated := range []map[string]any{{"type": "response.output_item.added", "output_index": c.index, "item": add}, {"type": "response.function_call_arguments.delta", "output_index": c.index, "item_id": c.id, "delta": it["arguments"]}, {"type": "response.function_call_arguments.done", "output_index": c.index, "item_id": c.id, "name": c.name, "arguments": it["arguments"]}, {"type": "response.output_item.done", "output_index": c.index, "item": it}} {
			emitted, e := emit(generated, str(generated, "type"))
			if e != nil {
				return nil, e
			}
			out = append(out, emitted)
		}
	}
	emitted, e := emit(completed, "response.completed")
	if e != nil {
		return nil, e
	}
	out = append(out, emitted)
	return out, nil
}
func callArgs(c *toolCallState) (string, error) {
	if len(c.finals) > 0 {
		for _, v := range c.finals {
			if v != c.finals[0] {
				return "", fmt.Errorf("conflicting argument finals")
			}
		}
	}
	if len(c.finals) > 0 && c.delta != "" && c.finals[0] != c.delta {
		return "", fmt.Errorf("delta/final argument mismatch")
	}
	args := c.delta
	if len(c.finals) > 0 {
		args = c.finals[0]
	}
	var snapshots []string
	for _, i := range c.items {
		if a, ok := i["arguments"].(string); ok && a != "" {
			snapshots = append(snapshots, a)
		}
	}
	for _, s := range snapshots {
		if s != "{}" && args != "" && args != "{}" && s != args {
			return "", fmt.Errorf("argument snapshots conflict")
		}
		if args == "" || args == "{}" {
			args = s
		}
	}
	if len(args) > 1024*1024 {
		return "", fmt.Errorf("argument limit")
	}
	return args, nil
}
func makeEvent(typ string, m map[string]any) sseEvent {
	return sseEvent{Event: typ, Data: jsonBytes(m)}
}

func RepairSSE(p AdapterPackage, input io.Reader) ([]byte, error) {
	closer, ok := input.(io.ReadCloser)
	if !ok {
		closer = io.NopCloser(input)
	}
	var b bytes.Buffer
	if err := RepairStream(p, closer, &b, RepairStreamOptions{}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// RepairSSEWithTimeout places a bound on complete-response validation. A
// closable upstream is interrupted on timeout so the reader goroutine exits.
func RepairSSEWithTimeout(p AdapterPackage, input io.ReadCloser, timeout time.Duration) ([]byte, error) {
	var b bytes.Buffer
	if err := RepairStream(p, input, &b, RepairStreamOptions{ResponseWait: timeout, Wait: timeout}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func VerifyPackage(p AdapterPackage) (AdapterVerification, error) {
	if err := ValidateAdapterPackage(p); err != nil {
		return AdapterVerification{}, err
	}
	v := AdapterVerification{ConfigurationValid: true, LiveVerified: false, Fixtures: []AdapterFixtureResult{}}
	for _, f := range p.Fixtures {
		ev := make([]sseEvent, 0, len(f.Events))
		for _, raw := range f.Events {
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			ev = append(ev, makeEvent(str(m, "type"), m))
		}
		out, e := RepairEvents(p, ev)
		if e != nil {
			v.Fixtures = append(v.Fixtures, AdapterFixtureResult{Name: f.Name, Message: "fixture replay did not match expected output: " + e.Error()})
			continue
		}
		var calls []AdapterCall
		var texts []string
		for _, event := range out {
			m := jsonMap(event.Data)
			if str(m, "type") == "response.output_item.done" {
				it := nested(m, "item")
				if str(it, "type") == "function_call" {
					calls = append(calls, AdapterCall{str(it, "call_id"), str(it, "name"), str(it, "arguments")})
				}
			}
			if str(m, "type") == "response.completed" {
				r := nested(m, "response")
				for _, x := range arr(r["output"]) {
					it, _ := x.(map[string]any)
					if str(it, "type") == "function_call" {
						fc := AdapterCall{str(it, "call_id"), str(it, "name"), str(it, "arguments")}
						if !containsCall(calls, fc) {
							calls = append(calls, fc)
						}
					}
					if p.Engine == "responses-tools-v3" && str(it, "type") == "message" {
						for _, part := range arr(it["content"]) {
							q, _ := part.(map[string]any)
							if str(q, "type") == "output_text" {
								texts = append(texts, str(q, "text"))
							}
						}
					}
				}
			}
		}
		if !sameCalls(calls, f.ExpectedCalls) || p.Engine == "responses-tools-v3" && !sameStrings(texts, f.ExpectedTexts) {
			v.Fixtures = append(v.Fixtures, AdapterFixtureResult{Name: f.Name, Message: "fixture replay did not match expected output"})
		} else {
			v.Fixtures = append(v.Fixtures, AdapterFixtureResult{Name: f.Name, OK: true, Calls: len(calls)})
		}
	}
	v.ReplayPassed = true
	for _, f := range v.Fixtures {
		v.ReplayPassed = v.ReplayPassed && f.OK
	}
	return v, nil
}
func arr(v any) []any { a, _ := v.([]any); return a }
func containsCall(a []AdapterCall, c AdapterCall) bool {
	for _, x := range a {
		if x == c {
			return true
		}
	}
	return false
}
func sameCalls(a, b []AdapterCall) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ImportAdapter verifies the package locally and stores an immutable snapshot.
func (s *Store) ImportAdapter(p AdapterPackage) error {
	v, e := VerifyPackage(p)
	if e != nil {
		return e
	}
	if !v.ReplayPassed {
		return appError("adapter replay failed", 400, "ADAPTER_VERIFICATION_FAILED")
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	return s.mutate(func(st *State) error {
		for _, old := range st.Adapters {
			var q AdapterPackage
			if json.Unmarshal(old, &q) == nil && q.ID == p.ID && q.Version == p.Version {
				if PackageHash(q) != PackageHash(p) {
					return appError("adapter version is immutable", 409, "ADAPTER_VERSION_CONFLICT")
				}
				return nil
			}
		}
		st.Adapters = append(st.Adapters, raw)
		return nil
	})
}
func (s *Store) AdapterState() []map[string]any {
	st := s.Snapshot()
	out := []map[string]any{}
	for _, raw := range st.Adapters {
		var p AdapterPackage
		if json.Unmarshal(raw, &p) == nil {
			out = append(out, map[string]any{"ref": PackageRef(p), "id": p.ID, "version": p.Version, "name": p.Name, "engine": p.Engine, "hash": PackageHash(p), "verified": true, "liveVerified": false})
		}
	}
	return out
}
func (s *Store) ResolveAdapter(route Route) (*AdapterPackage, error) {
	if route.Profile == "" || route.Profile == "none" {
		return nil, nil
	}
	for _, raw := range s.Snapshot().Adapters {
		var p AdapterPackage
		if json.Unmarshal(raw, &p) == nil && PackageRef(p) == route.Profile {
			return &p, nil
		}
	}
	return nil, appError("adapter not found", 409, "ADAPTER_NOT_FOUND")
}

// AdapterDirectory is an untrusted candidate area. It does not watch files;
// only an explicit Refresh verifies/imports bounded candidates.
type AdapterDirectory struct {
	Store                    *Store
	Directory, WorkDirectory string
	mu                       sync.Mutex
	files                    []AdapterFileState
	status, directoryError   string
}

func NewAdapterDirectory(s *Store) *AdapterDirectory {
	p := s.Paths()
	return &AdapterDirectory{Store: s, Directory: filepath.Join(p.Home, "adapters"), WorkDirectory: filepath.Join(p.Home, "repair-work"), status: "unscanned", files: []AdapterFileState{}}
}
func (d *AdapterDirectory) Prepare() error {
	for _, path := range []string{d.Directory, d.WorkDirectory} {
		if e := os.MkdirAll(path, 0700); e != nil {
			return e
		}
		if e := privateDirectory(path); e != nil {
			return appError("repair directory is unsafe", 409, "ADAPTER_DIRECTORY_UNSAFE")
		}
	}
	return nil
}
func (d *AdapterDirectory) State() AdapterDirectoryState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stateLocked()
}
func (d *AdapterDirectory) Refresh() (AdapterDirectoryState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := d.Prepare(); e != nil {
		d.status = "error"
		d.directoryError = "修复包或工作目录不可访问、不是普通目录或为符号链接；未读取其他位置。"
		return d.stateLocked(), nil
	}
	root, path, e := openAdapterRoot(d.Directory)
	if e != nil {
		d.status = "error"
		d.directoryError = "修复包目录不可安全访问；已有验证快照和路由未替换。"
		return d.stateLocked(), nil
	}
	defer root.Close()
	names := []string{}
	reader, e := os.Open(path)
	if e != nil {
		d.status = "error"
		d.directoryError = "修复包目录不可读取；已有验证快照和路由未替换。"
		return d.stateLocked(), nil
	}
	for len(names) <= AdapterFileLimit {
		entries, re := reader.ReadDir(64)
		if re != nil && re != io.EOF {
			_ = reader.Close()
			d.status = "error"
			d.directoryError = "修复包目录不可读取；已有验证快照和路由未替换。"
			return d.stateLocked(), nil
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasSuffix(name, ".adroute-adapter.json") && !strings.HasPrefix(name, ".") {
				names = append(names, name)
				if len(names) > AdapterFileLimit {
					break
				}
			}
		}
		if re == io.EOF || len(names) > AdapterFileLimit {
			break
		}
	}
	_ = reader.Close()
	sort.Strings(names)
	tooMany := len(names) > AdapterFileLimit
	if tooMany {
		names = names[:AdapterFileLimit]
	}
	files := make([]AdapterFileState, 0, len(names))
	for _, name := range names {
		path := filepath.Join(path, name)
		stat, re := os.Lstat(path)
		if re != nil {
			files = append(files, AdapterFileState{File: name, Status: "invalid", Message: "文件不可安全读取，请检查类型、权限或写入状态。"})
			continue
		}
		meta, statErr := platform.Lstat(path)
		if !stat.Mode().IsRegular() || stat.Mode()&os.ModeSymlink != 0 || statErr != nil || meta.Nlink != 1 {
			files = append(files, AdapterFileState{File: name, Status: "invalid", Message: "拒绝读取链接、目录或其他非普通文件。"})
			continue
		}
		if stat.Size() > AdapterPackageLimit {
			files = append(files, AdapterFileState{File: name, Status: "invalid", Message: "修复包超过 256KB。"})
			continue
		}
		data, re := readAdapterCandidate(path, stat)
		pack := AdapterPackage{}
		if re == nil {
			pack, re = ValidateAdapterJSON(data)
		}
		if re == nil && name != PackageRef(pack)+".adroute-adapter.json" {
			re = fmt.Errorf("candidate filename does not match package identity")
		}
		if re == nil {
			verification, _ := VerifyPackage(pack)
			if !verification.ReplayPassed {
				re = appError("adapter fixture replay failed", 400, "ADAPTER_VERIFICATION_FAILED")
			}
		}
		if re == nil {
			re = d.Store.ImportAdapter(pack)
		}
		if re != nil {
			status := "invalid"
			var app *AppError
			if errors.As(re, &app) && app.Code == "ADAPTER_VERSION_CONFLICT" {
				status = "conflict"
			}
			message := "文件不可安全读取，请检查类型、权限或写入状态。"
			if errors.As(re, &app) {
				message = app.Message
			}
			files = append(files, AdapterFileState{File: name, Status: status, Message: message})
			continue
		}
		files = append(files, AdapterFileState{File: name, Ref: PackageRef(pack), Status: "verified", Hash: PackageHash(pack), Message: "格式及本地样例验证通过；真实上游未验证。"})
	}
	d.files = files
	d.status = "ready"
	d.directoryError = ""
	if tooMany {
		d.directoryError = "文件数超过 128，本次仅检查前 128 个；请先清理无用文件。"
	}
	return d.stateLocked(), nil
}
func (d *AdapterDirectory) Remove(ref string) error      { return d.RemoveCandidate(ref, "") }
func (d *AdapterDirectory) RemoveFile(file string) error { return d.RemoveCandidate("", file) }
func (d *AdapterDirectory) RemoveCandidate(ref, file string) error {
	if ref != "" && !validProfileRef(ref) {
		return appError("invalid adapter reference", 400, "ADAPTER_INVALID")
	}
	if file == "" {
		if ref == "" {
			return appError("candidate filename required", 400, "ADAPTER_INVALID")
		}
		file = ref + ".adroute-adapter.json"
	}
	if !validAdapterFilename(file) || ref != "" && file != ref+".adroute-adapter.json" {
		return appError("only direct adapter candidate files may be removed", 400, "ADAPTER_INVALID")
	}
	effective := strings.TrimSuffix(file, ".adroute-adapter.json")
	if ref != "" {
		effective = ref
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := d.Prepare(); e != nil {
		return appError("adapter directory is unsafe", 409, "ADAPTER_DIRECTORY_UNSAFE")
	}
	root, rootPath, e := openAdapterRoot(d.Directory)
	if e != nil {
		return appError("adapter directory is unsafe", 409, "ADAPTER_DIRECTORY_UNSAFE")
	}
	defer root.Close()
	path := filepath.Join(rootPath, file)
	id, e := newID()
	if e != nil {
		return e
	}
	staged := filepath.Join(rootPath, ".removed-"+id+".tmp")
	moved := false
	e = d.Store.mutate(func(st *State) error {
		if len(adapterBindings(*st, effective)) > 0 {
			return appError("adapter is still bound", 409, "ADAPTER_IN_USE")
		}
		registered := false
		for _, raw := range st.Adapters {
			var p AdapterPackage
			if json.Unmarshal(raw, &p) == nil && PackageRef(p) == effective {
				registered = true
			}
		}
		info, re := os.Lstat(path)
		if re == nil {
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return appError("cannot remove directory or special file", 409, "ADAPTER_DIRECTORY_UNSAFE")
			}
			if re = os.Rename(path, staged); re != nil {
				return re
			}
			moved = true
		} else if !os.IsNotExist(re) {
			return re
		}
		if !registered && !moved {
			return appError("adapter candidate not found", 404, "ADAPTER_NOT_FOUND")
		}
		kept := make([]json.RawMessage, 0, len(st.Adapters))
		for _, raw := range st.Adapters {
			var p AdapterPackage
			if json.Unmarshal(raw, &p) != nil || PackageRef(p) != effective {
				kept = append(kept, raw)
			}
		}
		st.Adapters = kept
		return nil
	})
	if e != nil {
		if moved {
			if rollback := os.Link(staged, path); rollback != nil {
				return appError("adapter removal failed and candidate restore failed", 409, "ADAPTER_REMOVE_FAILED")
			}
			_ = os.Remove(staged)
		}
		var app *AppError
		if errors.As(e, &app) {
			return e
		}
		return appError("移除失败，未报告清理成功；请检查文件权限和配置存储。", 409, "ADAPTER_REMOVE_FAILED")
	}
	if moved {
		if e = os.Remove(staged); e != nil {
			return appError("adapter removed but staged candidate cleanup failed", 409, "ADAPTER_REMOVE_FAILED")
		}
	}
	kept := d.files[:0]
	for _, entry := range d.files {
		if entry.File != file {
			kept = append(kept, entry)
		}
	}
	d.files = kept
	return nil
}
func validAdapterFilename(name string) bool {
	return len(name) > len(".adroute-adapter.json") && len(name) <= 255 && !strings.HasPrefix(name, ".") && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f") && strings.HasSuffix(name, ".adroute-adapter.json")
}
func openAdapterRoot(path string) (*os.File, string, error) {
	f, e := platform.OpenFile(path, os.O_RDONLY, 0)
	if e != nil {
		return nil, "", appError("adapter directory unsafe", 409, "ADAPTER_DIRECTORY_UNSAFE")
	}
	st, e := f.Stat()
	if e != nil || !st.IsDir() {
		_ = f.Close()
		return nil, "", appError("adapter directory unsafe", 409, "ADAPTER_DIRECTORY_UNSAFE")
	}
	if e = platform.ChmodFile(f, 0700); e != nil {
		_ = f.Close()
		return nil, "", e
	}
	root := path
	if _, e = os.Stat("/proc/self/fd"); e == nil {
		root = fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	}
	return f, root, nil
}

func readAdapterCandidate(path string, before os.FileInfo) ([]byte, error) {
	f, err := platform.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	a, statErr := platform.Stat(f)
	if statErr != nil || !opened.Mode().IsRegular() || a.Nlink != 1 || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("unsafe or changed adapter candidate")
	}
	b, err := io.ReadAll(io.LimitReader(f, AdapterPackageLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > AdapterPackageLimit {
		return nil, fmt.Errorf("adapter package too large")
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(opened, after) || after.Size() != int64(len(b)) || after.ModTime() != opened.ModTime() {
		return nil, fmt.Errorf("adapter changed during read")
	}
	return b, nil
}
