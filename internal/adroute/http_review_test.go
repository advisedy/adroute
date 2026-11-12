package adroute

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func replayApp(t *testing.T) *HTTPApp {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/pixel-swe.adroute-adapter.json")
	if err != nil {
		t.Fatal(err)
	}
	var p AdapterPackage
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportAdapter(p); err != nil {
		t.Fatal(err)
	}
	return mustHTTPApp(HTTPOptions{Store: s, Port: 3001})
}
func replayCall(t *testing.T, a *HTTPApp, events []json.RawMessage) *httptest.ResponseRecorder {
	t.Helper()
	var p AdapterPackage
	for _, raw := range a.opts.Store.Snapshot().Adapters {
		_ = json.Unmarshal(raw, &p)
	}
	body, _ := json.Marshal(map[string]any{"ref": PackageRef(p), "events": events})
	r := httptest.NewRequest("POST", "http://127.0.0.1:3001/api/compatibility/replay", bytes.NewReader(body))
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+a.opts.Store.TokenPair().AdminToken)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func TestReplayRejectsEmptyUntypedOverCountAndOverSize(t *testing.T) {
	a := replayApp(t)
	tooLarge, _ := json.Marshal(map[string]any{"type": "response.completed", "padding": strings.Repeat("x", 262145)})
	cases := []struct {
		name   string
		events []json.RawMessage
	}{
		{"empty", nil},
		{"missing-type", []json.RawMessage{json.RawMessage(`{"response":{}}`)}},
		{"non-string-type", []json.RawMessage{json.RawMessage(`{"type":1}`)}},
		{"too-many", make([]json.RawMessage, 257)},
		{"too-large", []json.RawMessage{tooLarge}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := tc.events
			if tc.name == "too-many" {
				for i := range events {
					events[i] = json.RawMessage(`{"type":"response.output_text.delta","delta":"x"}`)
				}
			}
			w := replayCall(t, a, events)
			if w.Code < 400 {
				t.Fatalf("accepted invalid replay: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestReplayPassesOnlyCompletedResponseStatusCompleted(t *testing.T) {
	a := replayApp(t)
	cases := []struct {
		name, event string
		pass        bool
	}{
		{"success", `{"type":"response.completed","response":{"status":"completed"}}`, true},
		{"failed", `{"type":"response.completed","response":{"status":"failed"}}`, false},
		{"missing-status", `{"type":"response.completed","response":{}}`, false},
		{"other-terminal", `{"type":"response.failed","response":{"status":"completed"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := replayCall(t, a, []json.RawMessage{json.RawMessage(tc.event)})
			if w.Code != 200 {
				t.Fatalf("replay status=%d body=%s", w.Code, w.Body.String())
			}
			var got struct {
				ReplayPassed bool `json:"replayPassed"`
			}
			if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
				t.Fatal(e)
			}
			if got.ReplayPassed != tc.pass {
				t.Fatalf("replayPassed=%v want %v body=%s", got.ReplayPassed, tc.pass, w.Body.String())
			}
		})
	}
}
func TestEmbeddedAssetsOnlyServeGETAndHEAD(t *testing.T) {
	s, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	a := mustHTTPApp(HTTPOptions{Store: s, Assets: testEmbedded{}})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, "http://127.0.0.1:3001/", nil)
		r.Host = "127.0.0.1:3001"
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s status %d", method, w.Code)
		}
		if method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatalf("HEAD returned body length %d", w.Body.Len())
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(method, "http://127.0.0.1:3001/", bytes.NewBufferString("x"))
		r.Host = "127.0.0.1:3001"
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatalf("%s served asset", method)
		}
	}
}

func TestShutdownFlushesAcknowledgementBeforeCallback(t *testing.T) {
	s, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	var app *HTTPApp
	var server *httptest.Server
	var flushed atomic.Bool
	called := make(chan bool, 1)
	app = mustHTTPApp(HTTPOptions{Store: s, Port: 3001, InstanceID: "instance-test", Shutdown: func() { called <- flushed.Load() }})
	server = httptest.NewServer(app)
	defer server.Close()
	req, e := http.NewRequest(http.MethodPost, server.URL+"/internal/shutdown", nil)
	if e != nil {
		t.Fatal(e)
	}
	req.Host = "127.0.0.1:3001"
	req.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
	req.Header.Set("X-Adroute-Instance", "instance-test")
	// A recording wrapper makes the flush/callback ordering deterministic while
	// exercising the actual handler path; the separate daemon test exercises TCP.
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: &flushed}
	app.ServeHTTP(w, req)
	select {
	case wasFlushed := <-called:
		if !wasFlushed {
			t.Fatal("shutdown callback ran before acknowledgement flush")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown callback not invoked")
	}
	if w.Code != 202 {
		t.Fatalf("ack status %d", w.Code)
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed *atomic.Bool
}

func (w *flushRecorder) Flush() { w.flushed.Store(true); w.ResponseRecorder.Flush() }

type testEmbedded struct{}

func (testEmbedded) Open(name string) (fs.File, error) { return os.DirFS("../..").Open(name) }
