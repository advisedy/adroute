package adroute

import (
	"adroute/internal/platform"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type DiagnosticLimits struct {
	TTL       time.Duration
	MaxBytes  int
	MaxEvents int
}
type DiagnosticState struct {
	CaptureID  string `json:"captureId,omitempty"`
	Status     string `json:"status"`
	ModelID    string `json:"modelId,omitempty"`
	RouteID    string `json:"routeId,omitempty"`
	Model      string `json:"model,omitempty"`
	Raw        bool   `json:"raw,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Bytes      int    `json:"bytes,omitempty"`
	EventCount int    `json:"eventCount,omitempty"`
	Outcome    any    `json:"outcome,omitempty"`
}
type DiagnosticDetail struct {
	SchemaVersion int `json:"schemaVersion"`
	DiagnosticState
	Events           []map[string]any `json:"events"`
	Frames           []map[string]any `json:"frames,omitempty"`
	ParseError       bool             `json:"parseError"`
	PersistenceError bool             `json:"persistenceError"`
	Redacted         bool             `json:"redacted"`
	Warning          string           `json:"warning"`
	Actions          []map[string]any `json:"actions"`
	HeadersMS        int64            `json:"headersMs,omitempty"`
	HTTPStatus       int              `json:"httpStatus,omitempty"`
}
type Diagnostics struct {
	mu      sync.Mutex
	home    string
	limits  DiagnosticLimits
	current *diagnosticCapture
}
type diagnosticCapture struct {
	DiagnosticState
	target           Route // private in-memory upstream identity; never serialized
	path             string
	expires          time.Time
	started          time.Time
	secrets          []string
	events           []map[string]any
	frames           []map[string]any
	actions          []map[string]any
	partial          []byte
	parseError       bool
	persistenceError bool
	hash             string
	headersMS        int64
	httpStatus       int
}

func NewDiagnostics(home string, limits DiagnosticLimits) *Diagnostics {
	if limits.TTL <= 0 {
		limits.TTL = 2 * time.Minute
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 256 * 1024
	}
	if limits.MaxEvents <= 0 {
		limits.MaxEvents = 256
	}
	return &Diagnostics{home: filepath.Join(home, "diagnostics"), limits: limits}
}
func (d *Diagnostics) expireLocked() {
	if d.current != nil && time.Now().After(d.current.expires) {
		if d.current.Status == "capturing" {
			d.current.Truncated = true
		}
		d.current.Status = "expired"
	}
}
func (d *Diagnostics) State() DiagnosticState {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if d.current == nil {
		return DiagnosticState{Status: "off"}
	}
	s := d.current.DiagnosticState
	s.EventCount = len(d.current.events)
	return s
}
func (d *Diagnostics) Arm(route Route, model string, raw, consent bool) (DiagnosticState, error) {
	if route.ID == "" || route.BaseURL == "" {
		return DiagnosticState{}, appError("诊断需要一个已保存的 API 模型条目。", 400, "DIAGNOSIS_INVALID")
	}
	if model == "" || model != route.Model {
		return DiagnosticState{}, appError("诊断必须绑定一个明确的模型条目。", 400, "DIAGNOSIS_INVALID")
	}
	if raw && !consent {
		return DiagnosticState{}, appError("raw diagnostic content requires separate consent", 400, "DIAGNOSIS_CONSENT_REQUIRED")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if d.current != nil && (d.current.Status == "waiting" || d.current.Status == "capturing") {
		return DiagnosticState{}, appError("diagnostic already armed", 409, "DIAGNOSIS_BUSY")
	}
	if err := d.eraseFilesLocked(); err != nil {
		return DiagnosticState{}, err
	}
	id, e := newID()
	if e != nil {
		return DiagnosticState{}, e
	}
	c := &diagnosticCapture{DiagnosticState: DiagnosticState{CaptureID: id, Status: "waiting", ModelID: route.ID, RouteID: route.RouteID, Model: model, Raw: raw}, target: route, expires: time.Now().Add(d.limits.TTL), events: []map[string]any{}, frames: []map[string]any{}}
	if raw {
		if e = os.MkdirAll(d.home, 0700); e != nil {
			return DiagnosticState{}, e
		}
		if e = privateDirectory(d.home); e != nil {
			return DiagnosticState{}, e
		}
		c.path = filepath.Join(d.home, "capture-"+id+".json")
	}
	d.current = c
	return c.DiagnosticState, nil
}
func (d *Diagnostics) Claim(route Route, secrets []string) *DiagnosticCapture {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	c := d.current
	if c == nil || c.Status != "waiting" || !sameDiagnosticTarget(c.target, route) {
		return nil
	}
	c.Status = "capturing"
	c.started = time.Now()
	c.secrets = append([]string(nil), secrets...)
	return &DiagnosticCapture{owner: d, capture: c}
}

type DiagnosticCapture struct {
	owner   *Diagnostics
	capture *diagnosticCapture
}

var privateFieldRE = regexp.MustCompile(`(?i)(authorization|cookie|api.?key|access.?token|refresh.?token|secret)`)
var diagnosticCaptureFilenameRE = regexp.MustCompile(`^capture-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.json$`)

// atomicWrite uses os.CreateTemp(diagnosticsDir, ".adroute-*.tmp"); Go's
// replacement is the decimal rendering of a uint32, hence 1-10 digits.
var diagnosticWriterTempRE = regexp.MustCompile(`^\.adroute-[0-9]{1,10}\.tmp$`)

func safeDiagnostic(v any, secrets []string) any {
	switch x := v.(type) {
	case string:
		return Redact(x, secrets)
	case []any:
		r := make([]any, len(x))
		for i, z := range x {
			r[i] = safeDiagnostic(z, secrets)
		}
		return r
	case map[string]any:
		r := map[string]any{}
		for k, z := range x {
			if !privateFieldRE.MatchString(k) {
				r[Redact(k, secrets)] = safeDiagnostic(z, secrets)
			}
		}
		return r
	default:
		return v
	}
}
func (c *DiagnosticCapture) ObserveSSE(chunk []byte) {
	if c == nil || c.owner == nil {
		return
	}
	d := c.owner
	d.mu.Lock()
	defer d.mu.Unlock()
	x := c.capture
	if x != d.current || x.Status != "capturing" || x.Truncated {
		return
	}
	x.Bytes += len(chunk)
	if x.Bytes > d.limits.MaxBytes {
		x.Truncated = true
		return
	}
	x.partial = append(x.partial, chunk...)
	cut := bytes.LastIndex(x.partial, []byte("\n\n"))
	cr := bytes.LastIndex(x.partial, []byte("\r\n\r\n"))
	width := 2
	if cr > cut {
		cut = cr
		width = 4
	}
	if cut < 0 {
		if len(x.partial) > AdapterFrameLimit {
			x.Truncated = true
		}
		return
	}
	complete := append([]byte(nil), x.partial[:cut]...)
	x.partial = append([]byte(nil), x.partial[cut+width:]...)
	events, e := readSSE(bytes.NewReader(complete))
	if e != nil {
		x.parseError = true
		return
	}
	for _, ev := range events {
		if string(ev.Data) == "[DONE]" {
			continue
		}
		m := jsonMap(ev.Data)
		c.recordEventLocked(x, m, ev.Event)
	}
}
func (c *DiagnosticCapture) recordEventLocked(x *diagnosticCapture, m map[string]any, event string) {
	d := c.owner
	if len(x.events) >= d.limits.MaxEvents {
		x.Truncated = true
		return
	}
	typ := str(m, "type")
	if typ == "" {
		typ = event
	}
	summary := map[string]any{"elapsedMs": time.Since(x.started).Milliseconds(), "type": typ}
	for _, k := range []string{"output_index", "item_id", "call_id", "name"} {
		if v, ok := m[k]; ok {
			summary[k] = safeDiagnostic(v, x.secrets)
		}
	}
	if item := nested(m, "item"); item != nil {
		for _, k := range []string{"id", "call_id", "name"} {
			if v, ok := item[k]; ok {
				key := k
				if k == "id" {
					key = "item_id"
				}
				if _, exists := summary[key]; !exists {
					summary[key] = safeDiagnostic(v, x.secrets)
				}
			}
		}
		if a, ok := item["arguments"].(string); ok {
			summary["arguments"] = map[string]any{"bytes": len(a)}
		}
	}
	if id := str(m, "item_id"); id != "" {
		summary["item_id"] = Redact(id, x.secrets)
	}
	if id := str(m, "call_id"); id != "" {
		summary["call_id"] = Redact(id, x.secrets)
	}
	if args, ok := m["arguments"].(string); ok {
		summary["arguments"] = argumentShape(args)
	}
	if delta, ok := m["delta"].(string); ok {
		summary["deltaBytes"] = len(delta)
	}
	x.events = append(x.events, summary)
	if x.Raw {
		x.frames = append(x.frames, safeDiagnostic(m, x.secrets).(map[string]any))
	}
}
func (c *DiagnosticCapture) Action(v map[string]any) {
	if c == nil {
		return
	}
	d := c.owner
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.capture == d.current && c.capture.Status == "capturing" && len(c.capture.actions) < d.limits.MaxEvents {
		c.capture.actions = append(c.capture.actions, safeDiagnostic(v, c.capture.secrets).(map[string]any))
	}
}
func argumentShape(args string) map[string]any {
	result := map[string]any{"bytes": len(args)}
	var value map[string]any
	if json.Unmarshal([]byte(args), &value) == nil && value != nil {
		fields := map[string]any{}
		count := 0
		for key, v := range value {
			if count >= 50 {
				break
			}
			kind := fmt.Sprintf("%T", v)
			switch v.(type) {
			case []any:
				kind = "array"
			case map[string]any:
				kind = "object"
			case string:
				kind = "string"
			case bool:
				kind = "boolean"
			case nil:
				kind = "null"
			case float64:
				kind = "number"
			}
			fields[key] = kind
			count++
		}
		result["fields"] = fields
	}
	return result
}
func (c *DiagnosticCapture) Headers(status int) {
	if c == nil || c.owner == nil {
		return
	}
	d := c.owner
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.capture == d.current && c.capture.Status == "capturing" {
		c.capture.httpStatus = status
		c.capture.headersMS = time.Since(c.capture.started).Milliseconds()
	}
}
func (c *DiagnosticCapture) Finish(status int, code string) error {
	if c == nil {
		return nil
	}
	d := c.owner
	d.mu.Lock()
	defer d.mu.Unlock()
	x := c.capture
	if x != d.current || x.Status != "capturing" {
		return nil
	}
	if len(bytes.TrimSpace(x.partial)) > 0 {
		ev, ok, e := parseSSEBlock(x.partial)
		if e != nil {
			x.parseError = true
		} else if ok && string(ev.Data) != "[DONE]" {
			c.recordEventLocked(x, jsonMap(ev.Data), ev.Event)
		}
		x.partial = nil
	}
	x.Status = "ready"
	x.Outcome = map[string]any{"status": status, "code": Redact(code, x.secrets), "durationMs": time.Since(x.started).Milliseconds()}
	if x.Raw && !x.Truncated && !x.parseError {
		detail := d.detailLocked(x)
		b, e := json.MarshalIndent(detail, "", "  ")
		if e != nil {
			return e
		}
		b = append(b, '\n')
		if e = atomicWrite(x.path, b); e != nil {
			x.persistenceError = true
			return nil
		}
		sum := sha256.Sum256(b)
		x.hash = hex.EncodeToString(sum[:])
	}
	return nil
}
func (d *Diagnostics) detailLocked(x *diagnosticCapture) DiagnosticDetail {
	events := append([]map[string]any{}, x.events...)
	actions := append([]map[string]any{}, x.actions...)
	return DiagnosticDetail{SchemaVersion: 1, DiagnosticState: x.DiagnosticState, Events: events, Frames: append([]map[string]any(nil), x.frames...), Actions: actions, HeadersMS: x.headersMS, HTTPStatus: x.httpStatus, ParseError: x.parseError, PersistenceError: x.persistenceError, Redacted: true, Warning: "Redacted capture may contain code, paths, or business information and is not a lossless replay sample."}
}
func (d *Diagnostics) Detail() DiagnosticDetail {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if d.current == nil {
		return DiagnosticDetail{SchemaVersion: 1, DiagnosticState: DiagnosticState{Status: "off"}, Events: []map[string]any{}, Actions: []map[string]any{}, Redacted: true}
	}
	return d.detailLocked(d.current)
}

type DiagnosticHandoff struct {
	Path       string `json:"path"`
	CaptureID  string `json:"captureId"`
	SHA256     string `json:"sha256"`
	Replayable bool   `json:"replayable"`
}

func sameDiagnosticTarget(a, b Route) bool {
	return a.ID == b.ID && a.Model == b.Model && a.BaseURL == b.BaseURL && a.APIKey == b.APIKey && a.Profile == b.Profile
}

func (d *Diagnostics) Handoff(route Route, captureID string) (*DiagnosticHandoff, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	c := d.current
	if c == nil || c.CaptureID != captureID || !sameDiagnosticTarget(c.target, route) || c.Status != "ready" || !c.Raw || c.Truncated || c.parseError || c.persistenceError || c.hash == "" {
		return nil, nil
	}
	b, e := readDiagnosticCapture(c.path)
	if e != nil {
		return nil, nil
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	if hash != c.hash {
		return nil, nil
	}
	return &DiagnosticHandoff{Path: c.path, CaptureID: c.CaptureID, SHA256: hash, Replayable: len(c.frames) > 0}, nil
}
func readDiagnosticCapture(path string) ([]byte, error) {
	f, e := platform.OpenFile(path, os.O_RDONLY, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	meta, statErr := platform.Stat(f)
	if statErr != nil || !st.Mode().IsRegular() || meta.Nlink != 1 || st.Size() > 4*1024*1024 {
		return nil, fmt.Errorf("unsafe diagnostic capture")
	}
	b, e := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if e != nil || len(b) > 4*1024*1024 {
		return nil, fmt.Errorf("diagnostic capture limit")
	}
	after, e := f.Stat()
	if e != nil || !os.SameFile(st, after) || after.Size() != int64(len(b)) || after.ModTime() != st.ModTime() {
		return nil, fmt.Errorf("diagnostic capture changed")
	}
	return b, nil
}
func (d *Diagnostics) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := d.eraseFilesLocked(); e != nil {
		return e
	}
	d.current = nil
	return nil
}
func (d *Diagnostics) eraseFilesLocked() error {
	before, e := os.Lstat(d.home)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return appError("unsafe diagnostics directory", 409, "DIAGNOSIS_DIRECTORY_UNSAFE")
	}
	root, e := platform.OpenFile(d.home, os.O_RDONLY, 0)
	if e != nil {
		return e
	}
	defer root.Close()
	after, e := root.Stat()
	if e != nil || !os.SameFile(before, after) || !after.IsDir() {
		return appError("diagnostics directory changed", 409, "DIAGNOSIS_DIRECTORY_UNSAFE")
	}
	entries, e := root.ReadDir(-1)
	if e != nil {
		return e
	}
	rootPath := d.home
	if _, statErr := os.Stat("/proc/self/fd"); statErr == nil {
		rootPath = fmt.Sprintf("/proc/self/fd/%d", root.Fd())
	}
	for _, entry := range entries {
		name := entry.Name()
		if !diagnosticCaptureFilenameRE.MatchString(name) && !diagnosticWriterTempRE.MatchString(name) {
			continue
		}
		path := filepath.Join(rootPath, name)
		st, e := os.Lstat(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		// Remove the directory entry itself only. Lstat + Remove never opens or
		// follows a matching symlink, and directories/special files stay intact.
		if !st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}
func (d *Diagnostics) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current != nil && d.current.Status == "capturing" {
		d.current.Status = "expired"
	}
}
func (d *Diagnostics) String() string { return fmt.Sprintf("diagnostics(%s)", d.State().Status) }

type RepairPromptResult struct {
	Prompt             string         `json:"prompt"`
	Directory          string         `json:"directory"`
	WorkDirectory      string         `json:"workDirectory"`
	DiagnosisAvailable bool           `json:"diagnosisAvailable"`
	Target             map[string]any `json:"target"`
}

var promptFieldRE = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// GenerateRepairPrompt renders only the shipped template and a caller-supplied
// explicit target. It never infers a default provider or model.
func GenerateRepairPrompt(projectRoot string, store *Store, directory *AdapterDirectory, diagnostics *Diagnostics, modelID string, includeDiagnosis bool, diagnosisID string) (RepairPromptResult, error) {
	b, err := os.ReadFile(filepath.Join(projectRoot, "docs", "repair-agent-prompt.md"))
	if err != nil {
		return RepairPromptResult{}, appError("repair prompt template unavailable", 500, "PROMPT_UNAVAILABLE")
	}
	return GenerateRepairPromptFromTemplate(b, projectRoot, store, directory, diagnostics, modelID, includeDiagnosis, diagnosisID)
}

func GenerateRepairPromptFromTemplate(templateDocument []byte, projectRoot string, store *Store, directory *AdapterDirectory, diagnostics *Diagnostics, modelID string, includeDiagnosis bool, diagnosisID string) (RepairPromptResult, error) {
	return generateRepairPromptWithDocument(string(templateDocument), projectRoot, store, directory, diagnostics, modelID, includeDiagnosis, diagnosisID)
}

func generateRepairPromptWithDocument(doc, projectRoot string, store *Store, directory *AdapterDirectory, diagnostics *Diagnostics, modelID string, includeDiagnosis bool, diagnosisID string) (RepairPromptResult, error) {
	if directory == nil || store == nil {
		return RepairPromptResult{}, appError("repair prompt unavailable", 500, "PROMPT_UNAVAILABLE")
	}
	if err := directory.Prepare(); err != nil {
		return RepairPromptResult{}, err
	}
	if !filepath.IsAbs(projectRoot) {
		return RepairPromptResult{}, appError("project path must be absolute", 400, "INVALID_REQUEST")
	}
	var route Route
	var err error
	if modelID != "" {
		route, err = store.ModelRoute(modelID)
		if err != nil {
			return RepairPromptResult{}, err
		}
	}
	capture := (*DiagnosticHandoff)(nil)
	if includeDiagnosis && diagnostics != nil && modelID != "" {
		capture, err = diagnostics.Handoff(route, diagnosisID)
		if err != nil {
			return RepairPromptResult{}, err
		}
	}
	start := strings.Index(doc, "```text\n")
	if start < 0 {
		return RepairPromptResult{}, appError("repair prompt template unavailable", 500, "PROMPT_UNAVAILABLE")
	}
	start += len("```text\n")
	end := strings.Index(doc[start:], "\n```")
	if end < 0 {
		return RepairPromptResult{}, appError("repair prompt template unavailable", 500, "PROMPT_UNAVAILABLE")
	}
	template := doc[start : start+end]
	secrets := store.Secrets()
	safe := func(s string) string { return Redact(s, secrets) }
	diagPath := "尚无本目标完整且已授权诊断；请先引导用户采集并审查，不读取旧文件或认证配置"
	if capture != nil {
		diagPath = capture.Path
	}
	values := map[string]string{"adroute 项目绝对路径": mustJSON(projectRoot), "目标模型条目标识，由用户提供": "未指定：请先向用户确认，不推断网页默认或 Codex 会话选择", "目标真实模型 ID，不是对外模型 ID": "未指定：请先向用户确认真实模型 ID", "已审查且允许你读取的诊断文件绝对路径": mustJSON(diagPath), "允许写入临时测试文件的绝对路径": mustJSON(directory.WorkDirectory), "明确约定的候选包输出目录绝对路径": mustJSON(directory.Directory), "用户遇到的错误": "请用户补充实际报错和复现方式；不要凭空推断"}
	if route.ID != "" {
		values["目标模型条目标识，由用户提供"] = mustJSON(safe(route.RouteID))
	}
	if route.Model != "" {
		values["目标真实模型 ID，不是对外模型 ID"] = mustJSON(safe(route.Model))
	}
	var templateErr error
	prompt := promptFieldRE.ReplaceAllStringFunc(template, func(token string) string {
		m := promptFieldRE.FindStringSubmatch(token)
		v, ok := values[m[1]]
		if !ok {
			templateErr = fmt.Errorf("unknown prompt placeholder")
			return ""
		}
		return v
	})
	if templateErr != nil {
		return RepairPromptResult{}, appError("repair prompt template unavailable", 500, "PROMPT_UNAVAILABLE")
	}
	target := map[string]any(nil)
	if route.ID != "" {
		target = map[string]any{"modelId": safe(route.ID), "routeId": safe(route.RouteID), "groupName": safe(route.GroupName), "modelName": safe(route.ModelName), "model": safe(route.Model)}
		prompt += fmt.Sprintf("\n\n【目标模型条目】\nMODEL_ID=%s\nPUBLIC_ID=%s\nGROUP=%s\nMODEL_NAME=%s\n这些是管理标识，不是认证凭据；其他组或同名模型不属于本次目标。", mustJSON(safe(route.ID)), mustJSON(safe(route.RouteID)), mustJSON(safe(route.GroupName)), mustJSON(safe(route.ModelName)))
	}
	if capture != nil {
		prompt += fmt.Sprintf("\n\n【本次已授权诊断身份】\nCAPTURE_ID=%s\nSHA256=%s\n先验证 DIAGNOSTIC_PATH SHA256；不将原文输出到对话，指纹不符时停止。不得改读其他 capture 文件。", capture.CaptureID, capture.SHA256)
	}
	return RepairPromptResult{Prompt: prompt, Directory: directory.Directory, WorkDirectory: directory.WorkDirectory, DiagnosisAvailable: capture != nil, Target: target}, nil
}
func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }
