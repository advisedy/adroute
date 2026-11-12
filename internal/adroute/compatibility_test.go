package adroute

import (
	"adroute/internal/platform"
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV3StreamEmitsOnlyRepairedEvents(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v3.adroute-adapter.json")
	var input bytes.Buffer
	for _, event := range p.Fixtures[2].Events {
		input.WriteString("data: ")
		input.Write(event)
		input.WriteString("\n\n")
	}
	var output bytes.Buffer
	if err := RepairStream(p, io.NopCloser(&input), &output, RepairStreamOptions{}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil {
				counts[str(event, "type")]++
			}
		}
	}
	for _, typ := range []string{"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "response.completed"} {
		if counts[typ] != 1 {
			t.Errorf("%s emitted %d times, want 1", typ, counts[typ])
		}
	}
}

func TestV3StreamDoesNotLeakUnnormalizedText(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v3.adroute-adapter.json")
	var input bytes.Buffer
	for _, event := range p.Fixtures[0].Events {
		input.WriteString("data: ")
		input.Write(event)
		input.WriteString("\n\n")
	}
	var output bytes.Buffer
	if err := RepairStream(p, io.NopCloser(&input), &output, RepairStreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "SYNTHETIC_PRIVATE_PREFIX") || strings.Contains(output.String(), "<|close|>") {
		t.Fatal("raw text was leaked before normalization")
	}
}

func TestShippedAdapterPackagesReplayTheirFixtures(t *testing.T) {
	for _, name := range []string{"pixel-swe.adroute-adapter.json", "pixel-swe-v2.adroute-adapter.json", "pixel-swe-v3.adroute-adapter.json"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "examples", name))
			if err != nil {
				t.Fatal(err)
			}
			var pack AdapterPackage
			if err := json.Unmarshal(b, &pack); err != nil {
				t.Fatal(err)
			}
			got, err := VerifyPackage(pack)
			if err != nil {
				t.Fatal(err)
			}
			if !got.ConfigurationValid || !got.ReplayPassed || len(got.Fixtures) != len(pack.Fixtures) {
				t.Fatalf("verification failed: %+v", got)
			}
		})
	}
}

func TestV3ReplayDoesNotReturnPartialPrivateTextOnAmbiguousChannel(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "pixel-swe-v3.adroute-adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pack AdapterPackage
	if err = json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	input := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"content_index\":0,\"delta\":\"PRIVATE<|close|>think<|sep|><|open|>response<|sep|>one<|close|>think<|sep|><|open|>response<|sep|>two\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	out, err := RepairSSE(pack, strings.NewReader(input))
	if err == nil || len(out) != 0 {
		t.Fatalf("ambiguous v3 response leaked before validation: %q, %v", out, err)
	}
}

func TestAdapterDirectoryImportRemoveAndRejectHardlinks(t *testing.T) {
	home := t.TempDir()
	store, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "pixel-swe.adroute-adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p AdapterPackage
	if err = json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	d := NewAdapterDirectory(store)
	if err = d.Prepare(); err != nil {
		t.Fatal(err)
	}
	name := PackageRef(p) + ".adroute-adapter.json"
	path := filepath.Join(d.Directory, name)
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(path, path+".link"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Refresh(); err != nil {
		t.Fatal(err)
	}
	for _, item := range mustDirectoryState(t, d).Files {
		if item.File == name && item.Status == "verified" {
			t.Fatal("hard-linked candidate was verified")
		}
	}
	if err = os.Remove(path + ".link"); err != nil {
		t.Fatal(err)
	}
	if err = d.Remove(PackageRef(p)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("candidate still present: %v", err)
	}
}

func TestAdapterDirectoryStateMatchesFrontendPackageAndBindingShape(t *testing.T) {
	home := t.TempDir()
	s, e := OpenStore(home)
	if e != nil {
		t.Fatal(e)
	}
	p := fixturePackage(t, "pixel-swe.adroute-adapter.json")
	if e = s.ImportAdapter(p); e != nil {
		t.Fatal(e)
	}
	d := NewAdapterDirectory(s)
	state := d.State()
	if state.DirectoryStatus != "unscanned" || len(state.Packages) != 1 || state.Packages[0].SourceStatus != "unscanned" || !state.Packages[0].CanApply {
		t.Fatalf("unscanned frontend state mismatch: %+v", state)
	}
	gid, e := s.CreateGroup(GroupInput{Name: "directory"})
	if e != nil {
		t.Fatal(e)
	}
	mid, e := s.CreateModel(ModelInput{GroupID: gid, Name: "same", Model: "same", BaseURL: "https://directory.example/v1", APIKey: "synthetic-directory-key", Profile: PackageRef(p)})
	if e != nil {
		t.Fatal(e)
	}
	route, e := s.ModelRoute(mid)
	if e != nil {
		t.Fatal(e)
	}
	if e = d.Prepare(); e != nil {
		t.Fatal(e)
	}
	fileBytes, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(d.Directory, PackageRef(p)+".adroute-adapter.json"), fileBytes, 0600); e != nil {
		t.Fatal(e)
	}
	state, e = d.Refresh()
	if e != nil {
		t.Fatal(e)
	}
	if state.DirectoryStatus != "ready" || len(state.Files) != 1 || len(state.Packages) != 1 {
		t.Fatalf("refresh state mismatch: %+v", state)
	}
	packState := state.Packages[0]
	if packState.SourceStatus != "verified" || !packState.CanApply || packState.Description != p.Description || len(packState.Bindings) != 1 {
		t.Fatalf("package bindings/state shape mismatch: %+v", packState)
	}
	if packState.Bindings[0].ModelID != mid || packState.Bindings[0].RouteID != route.RouteID || packState.Bindings[0].Model != "same" || packState.Bindings[0].GroupName != "directory" {
		t.Fatalf("binding identity mismatch: %+v", packState.Bindings)
	}
}
func mustDirectoryState(t *testing.T, d *AdapterDirectory) AdapterDirectoryState {
	t.Helper()
	v, e := d.Refresh()
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestDiagnosticsRequireConsentAndExactModelIdentity(t *testing.T) {
	d := NewDiagnostics(t.TempDir(), DiagnosticLimits{})
	route := Route{ID: "model-one", RouteID: "adroute/work/one", Model: "same", BaseURL: "https://up.example/v1"}
	if _, err := d.Arm(route, "same", true, false); err == nil {
		t.Fatal("raw diagnostics armed without separate consent")
	}
	if _, err := d.Arm(route, "same", false, false); err != nil {
		t.Fatal(err)
	}
	other := route
	other.ID = "model-two"
	other.RouteID = "adroute/work/two"
	if c := d.Claim(other, []string{"secret"}); c != nil {
		t.Fatal("a different entry with the same real model id claimed the capture")
	}
	if c := d.Claim(route, []string{"secret"}); c == nil {
		t.Fatal("exact model did not claim diagnostic")
	}
}

func TestDiagnosticsCaptureSplitSSEFramesIncrementally(t *testing.T) {
	d := NewDiagnostics(t.TempDir(), DiagnosticLimits{})
	r := Route{ID: "m1", RouteID: "adroute/work/m", Model: "m", BaseURL: "https://up.example/v1"}
	if _, e := d.Arm(r, "m", false, false); e != nil {
		t.Fatal(e)
	}
	c := d.Claim(r, []string{"secret"})
	if c == nil {
		t.Fatal("claim failed")
	}
	c.ObserveSSE([]byte("event: response.created\ndata: {\"type\":"))
	if d.State().EventCount != 0 {
		t.Fatal("incomplete frame parsed")
	}
	c.ObserveSSE([]byte("\"response.created\"}\n\n"))
	if d.State().EventCount != 1 {
		t.Fatalf("split SSE frame not captured: %+v", d.State())
	}
}

func TestDiagnosticsKeepActionsSeparateAndFlushFinalPartialFrame(t *testing.T) {
	home := t.TempDir()
	d := NewDiagnostics(home, DiagnosticLimits{})
	route := Route{ID: "entry-actions", RouteID: "adroute/work/model-actions", Model: "model-actions", BaseURL: "https://up.example/v1"}
	if _, e := d.Arm(route, route.Model, false, false); e != nil {
		t.Fatal(e)
	}
	c := d.Claim(route, nil)
	if c == nil {
		t.Fatal("capture not claimed")
	}
	c.Action(map[string]any{"action": "retry", "secret": "do-not-keep"})
	c.Headers(429)
	c.ObserveSSE([]byte(`data: {"type":"response.output_text.delta","item_id":"m","content_index":0,"delta":"last"}`))
	if e := c.Finish(429, "rate limited"); e != nil {
		t.Fatal(e)
	}
	detail := d.Detail()
	if len(detail.Actions) != 1 || len(detail.Events) != 1 || detail.HTTPStatus != 429 || detail.HeadersMS < 0 {
		t.Fatalf("detail semantics mismatch: %+v", detail)
	}
	encoded, _ := json.Marshal(detail)
	if strings.Contains(string(encoded), "do-not-keep") {
		t.Fatal("action secrets persisted")
	}
}

func TestAdapterJSONStrictFieldsAndReplayFailureAreNotVerified(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "pixel-swe.adroute-adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(b), `"schemaVersion": 1`, `"schemaVersion": 1, "script": "unsafe"`, 1)
	if _, err = ValidateAdapterJSON([]byte(bad)); err == nil {
		t.Fatal("unknown executable package field accepted")
	}
	var p AdapterPackage
	if err = json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	p.Fixtures[0].ExpectedCalls[0].CallID = "wrong-call-id"
	v, err := VerifyPackage(p)
	if err != nil {
		t.Fatal(err)
	}
	if v.ReplayPassed {
		t.Fatal("incorrect expected call ID reported verified")
	}
}

func TestAdapterReplayTimeoutClosesUpstreamReader(t *testing.T) {
	b, e := os.ReadFile(filepath.Join("..", "..", "examples", "pixel-swe.adroute-adapter.json"))
	if e != nil {
		t.Fatal(e)
	}
	var p AdapterPackage
	if e = json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	start := time.Now()
	out, e := RepairSSEWithTimeout(p, reader, 10*time.Millisecond)
	if e == nil || len(out) != 0 {
		t.Fatalf("timeout did not fail closed: %q %v", out, e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout did not bound reader")
	}
}

func TestRawDiagnosticsRequireReviewablePrivateFileAndClear(t *testing.T) {
	home := t.TempDir()
	d := NewDiagnostics(home, DiagnosticLimits{})
	r := Route{ID: "m1", RouteID: "adroute/work/m", Model: "m", BaseURL: "https://up.example/v1"}
	if _, e := d.Arm(r, "m", true, true); e != nil {
		t.Fatal(e)
	}
	c := d.Claim(r, []string{"TOPSECRET"})
	c.ObserveSSE([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"authorization\":\"Bearer TOPSECRET\"}\n\n"))
	if e := c.Finish(200, ""); e != nil {
		t.Fatal(e)
	}
	s := d.State()
	h, e := d.Handoff(r, s.CaptureID)
	if e != nil || h == nil {
		t.Fatalf("handoff failed: %+v %v", h, e)
	}
	b, e := os.ReadFile(h.Path)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "TOPSECRET") || strings.Contains(string(b), "authorization") {
		t.Fatal("private value persisted")
	}
	st, e := os.Stat(h.Path)
	if e != nil || !platform.PrivateFile(h.Path, st) {
		t.Fatalf("capture mode unsafe: %v %v", st, e)
	}
	if e = d.Clear(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(h.Path); !os.IsNotExist(e) {
		t.Fatalf("capture file survived clear: %v", e)
	}
}

func TestDiagnosticsClearAndRearmRemoveWriterTempsWithoutFollowingSymlinks(t *testing.T) {
	home := t.TempDir()
	d := NewDiagnostics(home, DiagnosticLimits{})
	route := Route{ID: "entry-cleanup", RouteID: "adroute/work/model-cleanup", Model: "model-cleanup", BaseURL: "https://up.example/v1"}
	armAndFinish := func() DiagnosticState {
		t.Helper()
		state, err := d.Arm(route, route.Model, true, true)
		if err != nil {
			t.Fatal(err)
		}
		capture := d.Claim(route, []string{"synthetic-raw-secret"})
		if capture == nil {
			t.Fatal("expected claimed raw capture")
		}
		capture.ObserveSSE([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"content\":\"synthetic-raw-secret\"}}\n\n"))
		if err := capture.Finish(200, "ok"); err != nil {
			t.Fatal(err)
		}
		return state
	}
	plantTemp := func() string {
		t.Helper()
		f, err := os.CreateTemp(d.home, ".adroute-*.tmp")
		if err != nil {
			t.Fatal(err)
		}
		path := f.Name()
		if _, err = f.WriteString("synthetic-raw-secret-frame"); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := armAndFinish()
	firstCapture := filepath.Join(d.home, "capture-"+first.CaptureID+".json")
	temp := plantTemp()
	unrelated := filepath.Join(d.home, ".adroute-not-a-writer-temp.tmpx")
	if err := os.WriteFile(unrelated, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "symlink-target")
	if err := os.WriteFile(target, []byte("target remains"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d.home, ".adroute-1234567890.tmp")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := d.Clear(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{firstCapture, temp, link} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("matching capture/temp entry survived Clear: %s (%v)", path, err)
		}
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "target remains" {
		t.Fatalf("symlink target was touched: %q %v", got, err)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "preserve" {
		t.Fatalf("unrelated file was touched: %q %v", got, err)
	}

	second := armAndFinish()
	secondCapture := filepath.Join(d.home, "capture-"+second.CaptureID+".json")
	rearmTemp := plantTemp()
	if _, err := d.Arm(route, route.Model, true, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{secondCapture, rearmTemp} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("matching capture/temp entry survived re-arm: %s (%v)", path, err)
		}
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "preserve" {
		t.Fatalf("re-arm touched unrelated file: %q %v", got, err)
	}
}

func fixturePackage(t *testing.T, name string) AdapterPackage {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("..", "..", "examples", name))
	if e != nil {
		t.Fatal(e)
	}
	var p AdapterPackage
	if e = json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	return p
}
func encodeTestEvents(events []json.RawMessage) string {
	var b strings.Builder
	for _, raw := range events {
		m := jsonMap(raw)
		typ := str(m, "type")
		b.WriteString("event: " + typ + "\ndata: " + string(raw) + "\n\n")
	}
	return b.String()
}

func TestRepairStreamV1StreamsOrdinaryEventsButHoldsTools(t *testing.T) {
	p := fixturePackage(t, "pixel-swe.adroute-adapter.json")
	reader, writer := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- RepairStream(p, reader, outW, RepairStreamOptions{}); _ = outW.Close() }()
	text := `{"type":"response.output_text.delta","item_id":"m","content_index":0,"delta":"visible"}`
	if _, e := io.WriteString(writer, "event: response.output_text.delta\ndata: "+text+"\n\n"); e != nil {
		t.Fatal(e)
	}
	line, e := bufio.NewReader(outR).ReadString('\n')
	if e != nil || !strings.Contains(line, "response.output_text.delta") {
		t.Fatalf("ordinary text was not streamed before completion: %q %v", line, e)
	}
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	_, _ = io.WriteString(writer, encodeTestEvents(p.Fixtures[1].Events))
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestV2ReorderedParallelCallsUseStableCallIDs(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v2.adroute-adapter.json")
	base := p.Fixtures[2].Events
	other := make([]json.RawMessage, len(base))
	for i, b := range base {
		encoded := strings.ReplaceAll(string(b), "fc_empty", "fc_two")
		encoded = strings.ReplaceAll(encoded, "call_empty", "call_two")
		encoded = strings.ReplaceAll(encoded, "get_time", "get_date")
		var renamed map[string]any
		_ = json.Unmarshal([]byte(encoded), &renamed)
		if _, ok := renamed["output_index"]; ok {
			renamed["output_index"] = float64(1)
			encoded = string(jsonBytes(renamed))
		}
		other[i] = json.RawMessage(encoded)
	}
	var completed map[string]any
	_ = json.Unmarshal(base[len(base)-1], &completed)
	var firstFinal map[string]any
	_ = json.Unmarshal(base[len(base)-1], &firstFinal)
	_ = completed
	_ = firstFinal
	// The direct replay cases in the versioned JS suite are reproduced by editing the final output in place.
	merged := append([]json.RawMessage{}, base[:len(base)-1]...)
	merged = append(merged, other[:len(other)-1]...)
	last := map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_final_two", "call_id": "call_two", "name": "get_date", "arguments": "{}"}, map[string]any{"type": "function_call", "id": "fc_final_one", "call_id": "call_empty", "name": "get_time", "arguments": "{}"}}}}
	merged = append(merged, jsonBytes(last))
	out, e := RepairEvents(p, rawEvents(merged))
	if e != nil {
		t.Fatal(e)
	}
	calls := completedCalls(out)
	if len(calls) != 2 || calls[0].CallID != "call_empty" || calls[1].CallID != "call_two" {
		t.Fatalf("parallel tool identity/order lost: %+v", calls)
	}
}

func rawEvents(raw []json.RawMessage) []sseEvent {
	out := make([]sseEvent, 0, len(raw))
	for _, b := range raw {
		m := jsonMap(b)
		out = append(out, makeEvent(str(m, "type"), m))
	}
	return out
}
func completedCalls(events []sseEvent) []AdapterCall {
	for _, e := range events {
		m := jsonMap(e.Data)
		if str(m, "type") == "response.completed" {
			out := []AdapterCall{}
			for _, v := range arr(nested(m, "response")["output"]) {
				it, _ := v.(map[string]any)
				if str(it, "type") == "function_call" {
					out = append(out, AdapterCall{str(it, "call_id"), str(it, "name"), str(it, "arguments")})
				}
			}
			return out
		}
	}
	return nil
}

func TestV3RejectsReferenceQuotationAndContentPartEdges(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v3.adroute-adapter.json")
	texts := []string{"`prefix<|close|>think<|sep|><|open|>response<|sep|>answer`", `"prefix <|close|>think<|sep|><|open|>response<|sep|> answer"`, `'prefix <|close|>think<|sep|><|open|>response<|sep|> answer'`, `“prefix <|close|>think<|sep|><|open|>response<|sep|> answer”`, `Example: 'don't <|close|>think<|sep|><|open|>response<|sep|> answer' is literal.`, `Example: ‘don’t <|close|>think<|sep|><|open|>response<|sep|> answer’ is literal.`, `prefix <|close|>think<|sep|><|open|>response<|sep|> answer <|close|>think<|sep|><|open|>response<|sep|> other`}
	for _, text := range texts {
		events := textResponseEvents(text)
		if out, e := RepairEvents(p, events); e == nil || len(out) != 0 {
			t.Fatalf("quoted channel envelope accepted: %q", text)
		}
	}
	for _, text := range []string{"我 don't need guessing.<|close|>think<|sep|><|open|>response<|sep|>最终。", "中文前缀。<|close|>think<|sep|><|open|>response<|sep|>中文答复。"} {
		if _, e := RepairEvents(p, textResponseEvents(text)); e != nil {
			t.Fatalf("Unicode/contraction boundary was rejected: %q: %v", text, e)
		}
	}
	split := textResponseEvents("prefix <|close|>think<|sep|><|open|>response<|sep|>answer")
	done := jsonMap(split[len(split)-2].Data)
	item := nested(done, "item")
	whole := str(arr(item["content"])[0].(map[string]any), "text")
	runes := []rune(whole)
	item["content"] = []any{map[string]any{"type": "output_text", "text": string(runes[:10]), "annotations": []any{}}, map[string]any{"type": "output_text", "text": string(runes[10:]), "annotations": []any{}}}
	split[len(split)-2] = makeEvent(str(done, "type"), done)
	if out, e := RepairEvents(p, split); e == nil || len(out) > 0 {
		t.Fatal("channel boundary split across content parts accepted")
	}
}

func TestV3NormalizesEveryTextSurfaceAndConsistentAggregate(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v3.adroute-adapter.json")
	prefix := "Synthetic planning only."
	boundary := "<|close|>think<|sep|><|open|>response<|sep|>"
	answer := "最终答复。"
	whole := prefix + boundary + answer
	events := textResponseEvents(whole)
	for i, ev := range events {
		m := jsonMap(ev.Data)
		switch str(m, "type") {
		case "response.content_part.added":
			nested(m, "part")["text"] = ""
		case "response.completed":
			r := nested(m, "response")
			r["output_text"] = whole
		}
		events[i].Data = jsonBytes(m)
	}
	out, e := RepairEvents(p, events)
	if e != nil {
		t.Fatal(e)
	}
	for _, ev := range out {
		m := jsonMap(ev.Data)
		typ := str(m, "type")
		if typ == "response.output_text.delta" && str(m, "delta") != answer {
			t.Fatalf("delta not normalized: %+v", m)
		}
		if typ == "response.output_text.done" && str(m, "text") != answer {
			t.Fatalf("done not normalized: %+v", m)
		}
		if typ == "response.content_part.done" && str(nested(m, "part"), "text") != answer {
			t.Fatalf("content part not normalized: %+v", m)
		}
		if typ == "response.output_item.done" && str(arr(nested(m, "item")["content"])[0].(map[string]any), "text") != answer {
			t.Fatalf("item snapshot not normalized: %+v", m)
		}
		if typ == "response.completed" {
			r := nested(m, "response")
			if str(r, "output_text") != answer || str(arr(nested(m, "response")["output"])[0].(map[string]any), "id") == "" {
				t.Fatalf("aggregate/final snapshot not normalized: %+v", m)
			}
		}
	}
}

func TestV3RejectsOriginalSnapshotConflictsAndMultipleTextParts(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v3.adroute-adapter.json")
	events := textResponseEvents("Prefix <|close|>think<|sep|><|open|>response<|sep|>Answer")
	completed := jsonMap(events[len(events)-1].Data)
	item := arr(nested(completed, "response")["output"])[0].(map[string]any)
	item["id"] = "regenerated"
	item["content"] = []any{map[string]any{"type": "output_text", "text": "Different prefix <|close|>think<|sep|><|open|>response<|sep|>Answer", "annotations": []any{}}}
	events[len(events)-1] = makeEvent("response.completed", completed)
	if out, e := RepairEvents(p, events); e == nil || len(out) > 0 {
		t.Fatal("original identity conflict was normalized away")
	}
	multi := textResponseEvents("Prefix <|close|>think<|sep|><|open|>response<|sep|>Answer")
	done := jsonMap(multi[len(multi)-2].Data)
	part := nested(done, "item")
	part["content"] = append(arr(part["content"]), map[string]any{"type": "output_text", "text": "other", "annotations": []any{}})
	multi[len(multi)-2] = makeEvent(str(done, "type"), done)
	if out, e := RepairEvents(p, multi); e == nil || len(out) > 0 {
		t.Fatal("multiple text content parts accepted")
	}
}
func textResponseEvents(text string) []sseEvent {
	item := map[string]any{"id": "msg", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	ev := []map[string]any{{"type": "response.content_part.added", "item_id": "msg", "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}, {"type": "response.output_text.delta", "item_id": "msg", "content_index": 0, "delta": text}, {"type": "response.output_text.done", "item_id": "msg", "content_index": 0, "text": text}, {"type": "response.content_part.done", "item_id": "msg", "content_index": 0, "part": item["content"].([]any)[0]}, {"type": "response.output_item.done", "output_index": 0, "item": item}, {"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{item}}}}
	out := []sseEvent{}
	for _, m := range ev {
		out = append(out, makeEvent(str(m, "type"), m))
	}
	return out
}

func TestV2RejectsFinalOnlyNonToolIdentityBeforeReleasingTools(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v2.adroute-adapter.json")
	events := rawEvents(p.Fixtures[2].Events)
	completed := jsonMap(events[len(events)-1].Data)
	r := nested(completed, "response")
	r["output"] = append([]any{map[string]any{"type": "message", "id": "final-only", "role": "assistant", "content": []any{}}}, arr(r["output"])...)
	events[len(events)-1] = makeEvent("response.completed", completed)
	if out, e := RepairEvents(p, events); e == nil || len(out) != 0 {
		t.Fatal("final-only non-tool identity was guessed")
	}
}

func TestV2SnapshotReconciliationRejectsIdentityPayloadAndCollisionEdges(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v2.adroute-adapter.json")
	base := p.Fixtures[0].Events
	cases := map[string]func([]json.RawMessage){"changed non-tool content": func(e []json.RawMessage) {
		m := jsonMap(e[len(e)-1])
		output := arr(nested(m, "response")["output"])
		output[0].(map[string]any)["summary"] = []any{map[string]any{"type": "summary_text", "text": "different"}}
		e[len(e)-1] = jsonBytes(m)
	}, "duplicate final call ID": func(e []json.RawMessage) {
		m := jsonMap(e[len(e)-1])
		output := arr(nested(m, "response")["output"])
		call := copyMap(output[1].(map[string]any))
		call["id"] = "duplicate-final"
		output = append(output, call)
		nested(m, "response")["output"] = output
		e[len(e)-1] = jsonBytes(m)
	}, "final tool alias collides with non-tool": func(e []json.RawMessage) {
		m := jsonMap(e[len(e)-1])
		output := arr(nested(m, "response")["output"])
		output[1].(map[string]any)["id"] = "rs_stream"
		e[len(e)-1] = jsonBytes(m)
	}, "changed call identity": func(e []json.RawMessage) {
		m := jsonMap(e[len(e)-1])
		arr(nested(m, "response")["output"])[1].(map[string]any)["call_id"] = "unseen-call"
		e[len(e)-1] = jsonBytes(m)
	}}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			events := append([]json.RawMessage(nil), base...)
			mutate(events)
			if out, e := RepairEvents(p, rawEvents(events)); e == nil || len(out) != 0 {
				t.Fatalf("snapshot ambiguity escaped: out=%d err=%v", len(out), e)
			}
		})
	}
}

func TestRepairStreamV3BuffersBeforeAnyOutput(t *testing.T) {
	p := fixturePackage(t, "pixel-swe-v3.adroute-adapter.json")
	source := textResponseEvents("prefix <|close|>think<|sep|><|open|>response<|sep|>answer")
	input, writer := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RepairStream(p, input, outW, RepairStreamOptions{ResponseWait: time.Second})
		_ = outW.Close()
	}()
	frame := jsonBytes(jsonMap(source[0].Data))
	_, _ = io.WriteString(writer, "data: "+string(frame)+"\n\n")
	readDone := make(chan error, 1)
	go func() { one := make([]byte, 1); _, e := outR.Read(one); readDone <- e }()
	select {
	case e := <-readDone:
		if e == nil {
			t.Fatal("v3 emitted a prefix before full response validation")
		}
	case <-time.After(20 * time.Millisecond):
	}
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	_, _ = io.WriteString(writer, encodeTestEvents(rawFromSSE(source[1:])))
	_ = writer.Close()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func rawFromSSE(events []sseEvent) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(events))
	for _, e := range events {
		out = append(out, e.Data)
	}
	return out
}
