package adroute

// Syntax-preserving Codex config transactions. TOML is parsed twice: the
// maintained semantic decoder validates the complete document, and go-toml's
// unstable AST supplies exact token offsets. Edits replace only owned value
// ranges or insert a key at an AST-selected table boundary.
import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const codexConfigLimit = 2 << 20

var codexProviderPath = []string{"model_providers", "adroute"}
var safeConfigToken = regexp.MustCompile(`^[a-f0-9]{64}$`)
var safeLocalURL = regexp.MustCompile(`^http://127\.0\.0\.1:([0-9]{1,5})$`)

type CodexConfigOptions struct {
	Home, CodexHome, URL, ProxyToken, Model, InstallationID string
	Env                                                     map[string]string
	CatalogPath                                             *string
}
type CodexConfigResult struct {
	Configured bool   `json:"configured"`
	Changed    bool   `json:"changed"`
	ConfigPath string `json:"configPath"`
	BackupPath string `json:"backupPath"`
}

type tomlEntry struct {
	path, scope                            []string
	valueStart, valueEnd, keyStart, keyEnd int
	inline, valueInline                    bool
	containerStart                         int
	siblings                               []*tomlEntry
}
type tomlTable struct {
	path                   []string
	headerStart, headerEnd int
}
type tomlDocument struct {
	source   string
	semantic map[string]any
	entries  map[string]*tomlEntry
	tables   []tomlTable
	rootEnd  int
}

func tomlPathKey(p []string) string    { b, _ := json.Marshal(p); return string(b) }
func tomlPathEqual(a, b []string) bool { return tomlPathKey(a) == tomlPathKey(b) }
func tomlHasPrefix(p, prefix []string) bool {
	return len(p) >= len(prefix) && tomlPathEqual(p[:len(prefix)], prefix)
}

func parseCodexTOML(source string) (*tomlDocument, error) {
	if len(source) > codexConfigLimit {
		return nil, codexProblem("CODEX_CONFIG_INVALID", "Codex config exceeds the 2 MB limit")
	}
	d := &tomlDocument{source: source, entries: map[string]*tomlEntry{}}
	if err := toml.Unmarshal([]byte(source), &d.semantic); err != nil {
		return nil, codexProblem("CODEX_CONFIG_INVALID", "Codex configuration is invalid TOML; the original file was preserved")
	}
	p := &unstable.Parser{KeepComments: true}
	p.Reset([]byte(source))
	active := []string{}
	rootEnd := 0
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table:
			active = tomlASTKey(n)
			if len(active) == 0 {
				return nil, codexProblem("CODEX_CONFIG_INVALID", "Codex configuration is invalid TOML")
			}
			ki := n.Key()
			ki.Next()
			off := int(ki.Node().Raw.Offset)
			start := lineStart(source, off)
			end := lineEnd(source, off)
			d.tables = append(d.tables, tomlTable{append([]string(nil), active...), start, end})
		case unstable.ArrayTable:
			active = tomlASTKey(n)
			ki := n.Key()
			ki.Next()
			off := int(ki.Node().Raw.Offset)
			d.tables = append(d.tables, tomlTable{append([]string(nil), active...), lineStart(source, off), lineEnd(source, off)})
		case unstable.KeyValue:
			path := append(append([]string(nil), active...), tomlASTKey(n)...)
			addASTEntry(d, n, path, active, false, -1, nil)
			rootEnd = max(rootEnd, lineEnd(source, entryEnd(d.entries[tomlPathKey(path)])))
		}
	}
	if p.Error() != nil {
		return nil, codexProblem("CODEX_CONFIG_INVALID", "Codex configuration is invalid TOML; the original file was preserved")
	}
	d.rootEnd = rootEnd
	return d, nil
}

func tomlASTKey(n *unstable.Node) []string {
	out := []string{}
	it := n.Key()
	for it.Next() {
		k := it.Node()
		out = append(out, string(k.Data))
	}
	return out
}
func entryEnd(e *tomlEntry) int {
	if e == nil {
		return 0
	}
	return e.valueEnd
}
func addASTEntry(d *tomlDocument, n *unstable.Node, path, scope []string, inline bool, container int, siblings []*tomlEntry) {
	v := n.Value()
	key := n.Key()
	if !key.Next() {
		return
	}
	k := key.Node()
	keyStart := int(k.Raw.Offset)
	lastKey := k
	for key.Next() {
		lastKey = key.Node()
	}
	keyEnd := int(lastKey.Raw.Offset + lastKey.Raw.Length)
	vs, ve := tomlValueRange(d.source, keyEnd, v)
	e := &tomlEntry{path: append([]string(nil), path...), scope: append([]string(nil), scope...), valueStart: vs, valueEnd: ve, keyStart: keyStart, keyEnd: keyEnd, inline: inline, valueInline: v.Kind == unstable.InlineTable, containerStart: container, siblings: siblings}
	d.entries[tomlPathKey(path)] = e
	if v.Kind == unstable.InlineTable {
		children := []*unstable.Node{}
		for c := v.Child(); c != nil; c = c.Next() {
			if c.Kind == unstable.KeyValue {
				children = append(children, c)
			}
		}
		list := make([]*tomlEntry, len(children))
		for i, c := range children {
			cp := append(append([]string(nil), path...), tomlASTKey(c)...)
			addASTEntry(d, c, cp, scope, true, int(v.Raw.Offset), nil)
			list[i] = d.entries[tomlPathKey(cp)]
		}
		for _, child := range list {
			child.siblings = list
		}
	}
}

func tomlValueRange(source string, keyEnd int, value *unstable.Node) (int, int) {
	if value.Raw.Length > 1 {
		return int(value.Raw.Offset), int(value.Raw.Offset + value.Raw.Length)
	}
	i := keyEnd
	for i < len(source) && (source[i] == ' ' || source[i] == '\t') {
		i++
	}
	if i >= len(source) || source[i] != '=' {
		return int(value.Raw.Offset), int(value.Raw.Offset + value.Raw.Length)
	}
	i++
	for i < len(source) && (source[i] == ' ' || source[i] == '\t') {
		i++
	}
	start := i
	switch value.Kind {
	case unstable.String:
		if value.Raw.Length > 0 {
			return int(value.Raw.Offset), int(value.Raw.Offset + value.Raw.Length)
		}
		return scanTOMLString(source, start)
	case unstable.InlineTable:
		end := matchingBrace(source, start)
		if end >= 0 {
			return start, end + 1
		}
	case unstable.Array:
		end := matchingTOMLDelim(source, start, '[', ']')
		if end >= 0 {
			return start, end + 1
		}
	default:
		for i < len(source) {
			c := source[i]
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ',' || c == ']' || c == '}' || c == '#' {
				break
			}
			i++
		}
		if i > start {
			return start, i
		}
	}
	return start, start
}
func scanTOMLString(s string, start int) (int, int) {
	if start >= len(s) {
		return start, start
	}
	quote := s[start]
	multi := start+2 < len(s) && s[start:start+3] == strings.Repeat(string(quote), 3)
	width := 1
	if multi {
		width = 3
	}
	for i := start + width; i < len(s); i++ {
		if quote == '"' && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			if multi && i+2 < len(s) && s[i:i+3] == strings.Repeat(string(quote), 3) {
				return start, i + 3
			}
			if !multi {
				return start, i + 1
			}
		}
	}
	return start, start
}
func matchingTOMLDelim(s string, start int, open byte, close byte) int {
	if start < 0 || start >= len(s) || s[start] != open {
		return -1
	}
	depth := 0
	for i := start; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\'' {
			a, b := scanTOMLString(s, i)
			if b <= a {
				return -1
			}
			i = b - 1
			continue
		}
		if c == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if c == open {
			depth++
		}
		if c == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
func lineStart(s string, off int) int {
	if off > len(s) {
		off = len(s)
	}
	i := strings.LastIndex(s[:off], "\n")
	return i + 1
}
func lineEnd(s string, off int) int {
	if off > len(s) {
		off = len(s)
	}
	if i := strings.IndexByte(s[off:], '\n'); i >= 0 {
		return off + i + 1
	}
	return len(s)
}

func (d *tomlDocument) raw(path []string) any {
	e := d.entries[tomlPathKey(path)]
	if e == nil {
		return nil
	}
	return d.source[e.valueStart:e.valueEnd]
}
func tomlEqualRaw(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	decode := func(raw any) (any, bool) {
		var m map[string]any
		if toml.Unmarshal([]byte("v = "+raw.(string)), &m) != nil {
			return nil, false
		}
		return m["v"], true
	}
	x, ok := decode(a)
	if !ok {
		return false
	}
	y, ok := decode(b)
	return ok && reflect.DeepEqual(x, y)
}
func tomlKeyPart(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}
func tomlKeyPath(parts []string) string {
	a := make([]string, len(parts))
	for i, p := range parts {
		a[i] = tomlKeyPart(p)
	}
	return strings.Join(a, ".")
}
func tomlValue(s string) string { b, _ := json.Marshal(s); return string(b) }

func (d *tomlDocument) edit(start, end int, replacement string) (*tomlDocument, error) {
	if start < 0 || end < start || end > len(d.source) {
		return nil, codexProblem("CODEX_CONFIG_INVALID", "TOML syntax range was invalid")
	}
	return parseCodexTOML(d.source[:start] + replacement + d.source[end:])
}

func (d *tomlDocument) set(path []string, raw any) (*tomlDocument, error) {
	e := d.entries[tomlPathKey(path)]
	if e != nil && raw != nil {
		return d.edit(e.valueStart, e.valueEnd, raw.(string))
	}
	if e != nil && raw == nil {
		if !e.inline {
			start := lineStart(d.source, e.keyStart)
			end := lineEnd(d.source, e.valueEnd)
			return d.edit(start, end, "")
		}
		close := matchingBrace(d.source, e.containerStart)
		if close < 0 {
			return nil, codexProblem("CODEX_CONFIG_INVALID", "Invalid inline table range")
		}
		idx := -1
		for i, sib := range e.siblings {
			if sib == e {
				idx = i
				break
			}
		}
		start, end := e.keyStart, e.valueEnd
		if idx+1 < len(e.siblings) {
			next := e.siblings[idx+1]
			end = next.keyStart
		}
		if idx == len(e.siblings)-1 && idx > 0 {
			prev := e.siblings[idx-1]
			start = prev.valueEnd
		}
		return d.edit(start, end, "")
	}
	if raw == nil {
		return d, nil
	}
	// A scalar ancestor cannot be expanded as a table.
	for n := 1; n < len(path); n++ {
		if d.entries[tomlPathKey(path[:n])] != nil {
			entry := d.entries[tomlPathKey(path[:n])]
			if !entry.valueInline {
				return nil, codexProblem("CODEX_CONFIG_CONFLICT", "Codex configuration structure conflicts with adroute field "+tomlKeyPath(path))
			}
		}
	}
	// Add into the deepest existing inline table ancestor.
	for n := len(path) - 1; n > 0; n-- {
		parent := d.entries[tomlPathKey(path[:n])]
		if parent == nil {
			continue
		}
		if !parent.valueInline {
			return nil, codexProblem("CODEX_CONFIG_CONFLICT", "Codex configuration structure conflicts with adroute field "+tomlKeyPath(path))
		}
		close := matchingBrace(d.source, parent.valueStart)
		if close < 0 {
			return nil, codexProblem("CODEX_CONFIG_INVALID", "Invalid inline table range")
		}
		has := false
		for _, child := range d.entries {
			if child.inline && child.containerStart == parent.valueStart {
				has = true
				break
			}
		}
		ins := ""
		if has {
			beforeClose := strings.TrimSpace(d.source[parent.valueStart+1 : close])
			if beforeClose != "" && !strings.HasSuffix(beforeClose, ",") {
				ins = ","
			}
		}
		ins += " " + tomlKeyPath(path[n:]) + " = " + raw.(string)
		return d.edit(close, close, ins)
	}
	// Prefer an existing explicit table, otherwise create only the required table.
	var table *tomlTable
	for i := range d.tables {
		candidate := &d.tables[i]
		if tomlHasPrefix(path, candidate.path) && len(candidate.path) < len(path) && (table == nil || len(candidate.path) > len(table.path)) {
			table = candidate
		}
	}
	if table == nil {
		prefix := path[:len(path)-1]
		if len(prefix) == 0 {
			return d.insertRoot(path, raw.(string))
		}
		header := "[" + tomlKeyPath(prefix) + "]"
		eol := d.eol()
		text := header + eol + tomlKeyPart(path[len(path)-1]) + " = " + raw.(string) + eol
		if len(d.source) > 0 && !strings.HasSuffix(d.source, "\n") {
			text = eol + text
		}
		return d.edit(len(d.source), len(d.source), text)
	}
	return d.insertInTable(table, path[len(table.path):], raw.(string))
}

func (d *tomlDocument) eol() string {
	if strings.Contains(d.source, "\r\n") {
		return "\r\n"
	}
	return "\n"
}
func (d *tomlDocument) insertRoot(path []string, raw string) (*tomlDocument, error) {
	offset := len(d.source)
	for _, t := range d.tables {
		if t.headerStart < offset {
			offset = t.headerStart
		}
	}
	for _, e := range d.entries {
		if !e.inline && len(e.scope) == 0 && len(e.path) > 0 && e.keyStart < offset {
			end := lineEnd(d.source, e.valueEnd)
			if end > 0 {
				offset = end
			}
		}
	}
	prefix := ""
	if offset > 0 && !strings.HasSuffix(d.source[:offset], "\n") {
		prefix = d.eol()
	}
	return d.edit(offset, offset, prefix+tomlKeyPath(path)+" = "+raw+d.eol())
}
func (d *tomlDocument) insertInTable(t *tomlTable, key []string, raw string) (*tomlDocument, error) {
	next := len(d.source)
	for _, other := range d.tables {
		if other.headerStart > t.headerStart && other.headerStart < next {
			next = other.headerStart
		}
	}
	offset := t.headerEnd
	for _, e := range d.entries {
		if !e.inline && tomlPathEqual(e.scope, t.path) && e.keyStart >= t.headerStart && e.keyStart < next {
			end := lineEnd(d.source, e.valueEnd)
			if end > offset {
				offset = end
			}
		}
	}
	if offset > next {
		offset = next
	}
	text := tomlKeyPath(key) + " = " + raw + d.eol()
	if offset > 0 && !strings.HasSuffix(d.source[:offset], "\n") {
		text = d.eol() + text
	}
	return d.edit(offset, offset, text)
}

func matchingBrace(s string, open int) int {
	if open < 0 || open >= len(s) || s[open] != '{' {
		return -1
	}
	depth := 0
	quote := byte(0)
	multi := false
	escape := false
	for i := open; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if quote == '"' && !multi && escape {
				escape = false
				continue
			}
			if quote == '"' && !multi && c == '\\' {
				escape = true
				continue
			}
			if c == quote {
				if multi && i+2 < len(s) && s[i:i+3] == strings.Repeat(string(quote), 3) {
					i += 2
					quote = 0
					multi = false
				} else if !multi {
					quote = 0
				}
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			if i+2 < len(s) && s[i:i+3] == strings.Repeat(string(c), 3) {
				multi = true
				i += 2
			}
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

type codexField struct {
	Path   []string `json:"path"`
	Before any      `json:"before"`
	After  any      `json:"after"`
}
type codexJournal struct {
	Version        int          `json:"version"`
	Home           string       `json:"home"`
	Owner          string       `json:"owner"`
	CodexHome      string       `json:"codexHome"`
	BackupPath     string       `json:"backupPath"`
	BeforeExists   bool         `json:"beforeExists"`
	BeforeText     string       `json:"beforeText"`
	BaselineExists bool         `json:"baselineExists"`
	BaselineText   string       `json:"baselineText"`
	AppliedText    string       `json:"appliedText"`
	Fields         []codexField `json:"fields"`
	PendingText    *string      `json:"pendingText,omitempty"`
}
type codexOwner struct {
	Home  string `json:"home"`
	Owner string `json:"owner"`
}

func codexProblem(code, message string) error { return appError(message, 409, code) }
func restoreText(r codexJournal) string       { return r.BaselineText }
func restoreExists(r codexJournal) bool       { return r.BaselineExists }

func codexPlan(source string, o CodexConfigOptions) (string, []codexField, error) {
	d, e := parseCodexTOML(source)
	if e != nil {
		return "", nil, e
	}
	cfg := d.semantic
	profile, _ := cfg["profile"].(string)
	if raw, exists := cfg["profile"]; exists {
		if !exists || profile == "" {
			return "", nil, codexProblem("CODEX_CONFIG_INVALID", "The active Codex profile is invalid")
		}
		profiles, _ := cfg["profiles"].(map[string]any)
		if val, ok := profiles[profile].(map[string]any); !ok || val == nil {
			return "", nil, codexProblem("CODEX_CONFIG_INVALID", "The active Codex profile is invalid")
		}
		_ = raw
	}
	providers, _ := cfg["model_providers"].(map[string]any)
	providerValue, exists := providers["adroute"]
	provider, providerIsMap := providerValue.(map[string]any)
	if exists && (!providerIsMap || provider == nil) {
		return "", nil, codexProblem("CODEX_CONFIG_CONFLICT", "Existing adroute provider config has an incompatible structure")
	}
	for _, k := range []string{"auth", "aws", "gateway_oauth"} {
		if _, ok := provider[k]; ok {
			return "", nil, codexProblem("CODEX_CONFIG_CONFLICT", "Existing adroute provider uses another authentication mechanism")
		}
	}
	changes := []struct {
		path  []string
		after any
	}{{[]string{"model_provider"}, tomlValue("adroute")}}
	if o.Model != "" {
		changes = append(changes, struct {
			path  []string
			after any
		}{[]string{"model"}, tomlValue(o.Model)})
	}
	if o.CatalogPath != nil {
		changes = append(changes, struct {
			path  []string
			after any
		}{[]string{"model_catalog_json"}, tomlValue(*o.CatalogPath)})
	}
	if profile != "" {
		changes = append(changes, struct {
			path  []string
			after any
		}{[]string{"profiles", profile, "model_provider"}, tomlValue("adroute")})
		if o.Model != "" {
			changes = append(changes, struct {
				path  []string
				after any
			}{[]string{"profiles", profile, "model"}, tomlValue(o.Model)})
		}
		if o.CatalogPath != nil {
			changes = append(changes, struct {
				path  []string
				after any
			}{[]string{"profiles", profile, "model_catalog_json"}, tomlValue(*o.CatalogPath)})
		}
	}
	for _, kv := range []struct{ k, v string }{{"name", "adroute"}, {"base_url", strings.TrimRight(o.URL, "/") + "/v1"}, {"wire_api", "responses"}, {"experimental_bearer_token", o.ProxyToken}} {
		changes = append(changes, struct {
			path  []string
			after any
		}{append(append([]string(nil), codexProviderPath...), kv.k), tomlValue(kv.v)})
	}
	changes = append(changes, struct {
		path  []string
		after any
	}{append(append([]string(nil), codexProviderPath...), "requires_openai_auth"), "false"}, struct {
		path  []string
		after any
	}{append(append([]string(nil), codexProviderPath...), "supports_websockets"), "false"})
	changes = append(changes, struct {
		path  []string
		after any
	}{append(append([]string(nil), codexProviderPath...), "env_key"), nil}, struct {
		path  []string
		after any
	}{append(append([]string(nil), codexProviderPath...), "env_key_instructions"), nil})
	for _, entry := range d.entries {
		if len(entry.path) == 4 && tomlHasPrefix(entry.path, codexProviderPath) && (entry.path[2] == "http_headers" || entry.path[2] == "env_http_headers") && strings.EqualFold(entry.path[3], "authorization") {
			changes = append(changes, struct {
				path  []string
				after any
			}{entry.path, nil})
		}
	}
	fields := make([]codexField, 0, len(changes))
	for _, c := range changes {
		before := d.raw(c.path)
		fields = append(fields, codexField{append([]string(nil), c.path...), before, c.after})
		if !tomlEqualRaw(before, c.after) {
			d, e = d.set(c.path, c.after)
			if e != nil {
				return "", nil, e
			}
		}
	}
	return d.source, fields, nil
}

func rollbackCodex(source string, r codexJournal) (string, int, error) {
	if source == r.AppliedText || r.PendingText != nil && source == *r.PendingText {
		return restoreText(r), 0, nil
	}
	d, e := parseCodexTOML(source)
	if e != nil {
		return "", 0, e
	}
	preserved := 0
	token := []string{"model_providers", "adroute", "experimental_bearer_token"}
	tokenField := false
	for _, f := range r.Fields {
		if tomlPathEqual(f.Path, token) {
			tokenField = true
		}
	}
	managed := !tokenField || tomlEqualRaw(d.raw(token), fieldAfter(r.Fields, token))
	for i := len(r.Fields) - 1; i >= 0; i-- {
		f := r.Fields[i]
		current := d.raw(f.Path)
		if tomlEqualRaw(current, f.After) && (f.After != nil || managed) {
			d, e = d.set(f.Path, f.Before)
			if e != nil {
				return "", 0, e
			}
		} else {
			preserved++
		}
	}
	return d.source, preserved, nil
}
func fieldAfter(fields []codexField, path []string) any {
	for _, f := range fields {
		if tomlPathEqual(f.Path, path) {
			return f.After
		}
	}
	return nil
}

func validateCodexJournal(r codexJournal, home string) error {
	bad := func() error {
		return codexProblem("CODEX_CONFIG_RECORD_INVALID", "Codex connection record is invalid; existing configuration was preserved")
	}
	if r.Version != 2 || r.Home != home || r.Owner == "" || !filepath.IsAbs(r.CodexHome) || filepath.Clean(r.CodexHome) != r.CodexHome || strings.ContainsRune(r.CodexHome, 0) || !filepath.IsAbs(r.BackupPath) || !strings.HasPrefix(r.BackupPath, filepath.Join(home, "codex")+string(os.PathSeparator)) || !regexp.MustCompile(`^config\.before-[a-f0-9]{32}\.toml$`).MatchString(filepath.Base(r.BackupPath)) || !r.BeforeExists && r.BeforeText != "" || len(r.Fields) > 100 || len(r.BeforeText) > codexConfigLimit || len(r.BaselineText) > codexConfigLimit || len(r.AppliedText) > codexConfigLimit {
		return bad()
	}
	base, e := parseCodexTOML(r.BaselineText)
	if e != nil {
		return bad()
	}
	applied, e := parseCodexTOML(r.AppliedText)
	if e != nil {
		return bad()
	}
	if r.PendingText != nil {
		if len(*r.PendingText) > codexConfigLimit {
			return bad()
		}
		if _, e = parseCodexTOML(*r.PendingText); e != nil {
			return bad()
		}
	}
	seen := map[string]bool{}
	replay := base
	for _, f := range r.Fields {
		if len(f.Path) == 0 {
			return bad()
		}
		if !allowedCodexField(f.Path, base.semantic) {
			return bad()
		}
		key := tomlPathKey(f.Path)
		if seen[key] || base.raw(f.Path) != f.Before || !tomlEqualRaw(applied.raw(f.Path), f.After) {
			return bad()
		}
		seen[key] = true
		if f.After != nil {
			if s, ok := f.After.(string); !ok {
				return bad()
			} else {
				vdoc, ve := parseCodexTOML("v = " + s)
				if ve != nil || len(vdoc.semantic) != 1 || vdoc.raw([]string{"v"}) != s {
					return bad()
				}
				if f.Path[len(f.Path)-1] == "model_catalog_json" {
					v, ok := vdoc.semantic["v"].(string)
					if !ok || !filepath.IsAbs(v) || strings.ContainsRune(v, 0) {
						return bad()
					}
				}
			}
		}
		if !tomlEqualRaw(f.Before, f.After) {
			replay, e = replay.set(f.Path, f.After)
			if e != nil {
				return bad()
			}
		}
	}
	if replay.source != r.AppliedText {
		return bad()
	}
	required := [][]string{{"model_provider"}}
	for _, k := range []string{"name", "base_url", "wire_api", "experimental_bearer_token", "requires_openai_auth", "supports_websockets", "env_key", "env_key_instructions"} {
		required = append(required, append(append([]string(nil), codexProviderPath...), k))
	}
	for _, p := range required {
		if !seen[tomlPathKey(p)] {
			return bad()
		}
	}
	if profile, ok := base.semantic["profile"].(string); ok && profile != "" && !seen[tomlPathKey([]string{"profiles", profile, "model_provider"})] {
		return bad()
	}
	return nil
}

func allowedCodexField(path []string, baseline map[string]any) bool {
	last := path[len(path)-1]
	if len(path) == 1 {
		return last == "model_provider" || last == "model" || last == "model_catalog_json"
	}
	if len(path) == 3 && path[0] == "profiles" {
		profile, _ := baseline["profile"].(string)
		return profile != "" && path[1] == profile && (last == "model_provider" || last == "model" || last == "model_catalog_json")
	}
	if len(path) == 3 && tomlPathEqual(path[:2], codexProviderPath) {
		switch last {
		case "name", "base_url", "wire_api", "experimental_bearer_token", "requires_openai_auth", "supports_websockets", "env_key", "env_key_instructions":
			return true
		}
	}
	return len(path) == 4 && tomlPathEqual(path[:2], codexProviderPath) && (path[2] == "http_headers" || path[2] == "env_http_headers") && strings.EqualFold(path[3], "authorization")
}

func ConnectCodex(o CodexConfigOptions) (CodexConfigResult, error) {
	if o.Home == "" {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_FAILED", "Invalid adroute home")
	}
	home, e := filepath.Abs(o.Home)
	if e != nil {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_FAILED", "Invalid adroute home")
	}
	home = filepath.Clean(home)
	codexHome := o.CodexHome
	if codexHome == "" {
		codexHome = o.Env["CODEX_HOME"]
	}
	if codexHome == "" {
		userHome := o.Env["HOME"]
		if userHome == "" {
			userHome, _ = os.UserHomeDir()
		}
		codexHome = filepath.Join(userHome, ".codex")
	}
	codexHome, e = filepath.Abs(codexHome)
	if e != nil {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_UNREADABLE", "Invalid CODEX_HOME")
	}
	codexHome = filepath.Clean(codexHome)
	port := ""
	m := safeLocalURL.FindStringSubmatch(o.URL)
	if len(m) == 2 {
		port = m[1]
	}
	if port == "" {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_FAILED", "Local Codex setup parameters are invalid")
	}
	pn, _ := strconv.Atoi(port)
	if pn < 1 || pn > 65535 || !safeConfigToken.MatchString(o.ProxyToken) || o.InstallationID == "" || strings.ContainsRune(o.InstallationID, 0) {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_FAILED", "Local Codex setup parameters are invalid")
	}
	if o.CatalogPath != nil && (!filepath.IsAbs(*o.CatalogPath) || strings.ContainsRune(*o.CatalogPath, 0)) {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_FAILED", "Catalog path must be absolute")
	}
	if e = ensureSafeDirectory(codexHome, 0700); e != nil {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_UNREADABLE", "CODEX_HOME must be a real directory")
	}
	changed := false
	if e = withCodexLock(codexHome, func(ownerPath string) error {
		var err error
		changed, err = connectCodexLocked(o, home, codexHome, ownerPath)
		return err
	}); e != nil {
		return CodexConfigResult{}, e
	}
	resultPath := filepath.Join(codexHome, "config.toml")
	recordPath := filepath.Join(home, "codex", "connection.json")
	r, e := readCodexJournal(recordPath)
	if e != nil {
		return CodexConfigResult{}, e
	}
	if r == nil {
		return CodexConfigResult{}, codexProblem("CODEX_CONFIG_RECORD_INVALID", "Codex connection record disappeared during setup")
	}
	return CodexConfigResult{Configured: true, Changed: changed, ConfigPath: resultPath, BackupPath: r.BackupPath}, nil
}

func connectCodexLocked(o CodexConfigOptions, home, codexHome, ownerPath string) (bool, error) {
	paths := codexPaths(home)
	record, e := readCodexJournal(paths.record)
	if e != nil {
		return false, e
	}
	if record != nil {
		if e = validateCodexJournal(*record, home); e != nil {
			return false, e
		}
		if record.CodexHome != codexHome || record.Owner != o.InstallationID {
			return false, codexProblem("CODEX_CONFIG_CONFLICT", "This adroute installation already manages another Codex target")
		}
	}
	owner, e := readOwner(ownerPath)
	if e != nil {
		return false, e
	}
	if owner != nil && (owner.Home != home || owner.Owner != o.InstallationID) {
		return false, codexProblem("CODEX_CONFIG_CONFLICT", "Codex config is owned by another adroute installation")
	}
	configPath := filepath.Join(codexHome, "config.toml")
	current, e := readSafeText(configPath)
	if e != nil {
		return false, e
	}
	baseline := current.text
	if record != nil && current.exists {
		baseline, _, e = rollbackCodex(current.text, *record)
		if e != nil {
			return false, e
		}
	}
	next, fields, e := codexPlan(baseline, o)
	if e != nil {
		return false, e
	}
	if len(next) > codexConfigLimit {
		return false, codexProblem("CODEX_CONFIG_INVALID", "Configured Codex TOML exceeds the size limit")
	}
	if record != nil && record.PendingText == nil && current.exists && current.text == next && owner != nil {
		return false, nil
	}
	if e = ensurePrivateDirectory(paths.dir); e != nil {
		return false, codexProblem("CODEX_CONFIG_UNREADABLE", "Cannot create private adroute Codex state directory")
	}
	backup := filepath.Join(paths.dir, "config.before-"+randomHexString(16)+".toml")
	if record != nil {
		backup = record.BackupPath
	} else {
		if e = writeExclusive(backup, []byte(current.text), 0600); e != nil {
			return false, codexProblem("CODEX_CONFIG_FAILED", "Cannot create private Codex backup")
		}
		if e = syncDir(backup); e != nil {
			return false, codexProblem("CODEX_CONFIG_FAILED", "Cannot durably save Codex backup")
		}
	}
	beforeExists := current.exists
	beforeText := current.text
	baselineExists := current.exists
	if record != nil {
		beforeExists = record.BeforeExists
		beforeText = record.BeforeText
		if current.exists {
			baselineExists = record.BaselineExists || baseline != restoreText(*record)
		} else {
			baselineExists = false
		}
	}
	r := codexJournal{Version: 2, Home: home, Owner: o.InstallationID, CodexHome: codexHome, BackupPath: backup, BeforeExists: beforeExists, BeforeText: beforeText, BaselineExists: baselineExists, BaselineText: baseline, AppliedText: next, Fields: fields}
	pending := current.text
	r.PendingText = &pending
	if e = atomicCodexJSON(paths.record, r); e != nil {
		return false, codexProblem("CODEX_CONFIG_FAILED", "Cannot durably write Codex recovery journal")
	}
	if e = atomicCodexJSON(ownerPath, codexOwner{home, o.InstallationID}); e != nil {
		return false, codexProblem("CODEX_CONFIG_FAILED", "Cannot durably write Codex ownership record")
	}
	if e = replaceSafeText(configPath, current, next, false); e != nil {
		return false, e
	}
	r.PendingText = nil
	if e = atomicCodexJSON(paths.record, r); e != nil {
		return false, codexProblem("CODEX_CONFIG_FAILED", "Codex config is written but journal finalization failed; restore can recover")
	}
	return !current.exists || current.text != next, nil
}

func RestoreCodex(home string) (map[string]any, error) {
	if home == "" {
		return nil, codexProblem("CODEX_CONFIG_FAILED", "Invalid adroute home")
	}
	abs, e := filepath.Abs(home)
	if e != nil {
		return nil, codexProblem("CODEX_CONFIG_FAILED", "Invalid adroute home")
	}
	home = filepath.Clean(abs)
	paths := codexPaths(home)
	r, e := readCodexJournal(paths.record)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return map[string]any{"restored": false, "alreadyRestored": true}, nil
	}
	if e = validateCodexJournal(*r, home); e != nil {
		return nil, e
	}
	changed, preserved := false, 0
	if e = withCodexLock(r.CodexHome, func(ownerPath string) error {
		var err error
		changed, preserved, err = restoreCodexLocked(paths, *r, ownerPath)
		return err
	}); e != nil {
		return nil, e
	}
	return map[string]any{"restored": true, "changed": changed, "preservedChanges": preserved, "configPath": filepath.Join(r.CodexHome, "config.toml"), "backupPath": r.BackupPath}, nil
}
func restoreCodexLocked(paths codexPathSet, r codexJournal, ownerPath string) (bool, int, error) {
	owner, e := readOwner(ownerPath)
	if e != nil {
		return false, 0, e
	}
	if owner != nil && (owner.Home != paths.home || owner.Owner != r.Owner) {
		return false, 0, codexProblem("CODEX_CONFIG_CONFLICT", "Codex ownership changed; configuration was not overwritten")
	}
	path := filepath.Join(r.CodexHome, "config.toml")
	current, e := readSafeText(path)
	if e != nil {
		return false, 0, e
	}
	changed, preserved := false, 0
	if current.exists {
		restored, count, e := rollbackCodex(current.text, r)
		if e != nil {
			return false, 0, e
		}
		preserved = count
		remove := !restoreExists(r) && restored == ""
		if e = replaceSafeText(path, current, restored, remove); e != nil {
			return false, 0, e
		}
		changed = remove || restored != current.text
	}
	if e = os.Remove(paths.record); e != nil && !os.IsNotExist(e) {
		return false, 0, codexProblem("CODEX_CONFIG_FAILED", "Cannot remove Codex connection journal")
	}
	if owner != nil {
		if e = os.Remove(ownerPath); e != nil && !os.IsNotExist(e) {
			return false, 0, codexProblem("CODEX_CONFIG_FAILED", "Cannot remove Codex ownership record")
		}
	}
	return changed, preserved, syncDir(paths.record)
}

type codexPathSet struct{ home, dir, record string }

func codexPaths(home string) codexPathSet {
	d := filepath.Join(home, "codex")
	return codexPathSet{home, d, filepath.Join(d, "connection.json")}
}
func randomHexString(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func ensureSafeDirectory(path string, mode os.FileMode) error {
	abs, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	vol := filepath.VolumeName(abs)
	cur := vol + string(os.PathSeparator)
	rest := strings.TrimPrefix(abs, cur)
	for _, part := range strings.Split(rest, string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, e := os.Lstat(cur)
		if os.IsNotExist(e) {
			if e = os.Mkdir(cur, mode); e != nil && !os.IsExist(e) {
				return e
			}
			info, e = os.Lstat(cur)
		}
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe directory path")
		}
	}
	return nil
}
func ensurePrivateDirectory(path string) error {
	if e := ensureSafeDirectory(path, 0700); e != nil {
		return e
	}
	info, e := os.Lstat(path)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe private directory")
	}
	if e = os.Chmod(path, 0700); e != nil {
		return e
	}
	return nil
}

type codexTextSnapshot struct {
	exists bool
	text   string
	dev    uint64
	ino    uint64
	nlink  uint64
	size   int64
	mtime  int64
	ctime  int64
}

func readSafeText(path string) (codexTextSnapshot, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e == syscall.ENOENT {
		return codexTextSnapshot{}, nil
	}
	if e != nil {
		return codexTextSnapshot{}, codexProblem("CODEX_CONFIG_UNREADABLE", "Codex config cannot be safely opened")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return codexTextSnapshot{}, codexProblem("CODEX_CONFIG_UNREADABLE", "Codex config cannot be safely read")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || !ok || stat.Nlink != 1 || st.Size() > codexConfigLimit {
		return codexTextSnapshot{}, codexProblem("CODEX_CONFIG_INVALID", "Codex config must be a regular file under 2 MB")
	}
	b, e := io.ReadAll(io.LimitReader(f, codexConfigLimit+1))
	if e != nil || len(b) > codexConfigLimit {
		return codexTextSnapshot{}, codexProblem("CODEX_CONFIG_INVALID", "Codex config exceeds the size limit")
	}
	after, e := f.Stat()
	if e != nil {
		return codexTextSnapshot{}, codexProblem("CODEX_CONFIG_UNREADABLE", "Codex config cannot be safely read")
	}
	as, ok := after.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev != as.Dev || stat.Ino != as.Ino || stat.Size != as.Size || stat.Mtim != as.Mtim || stat.Ctim != as.Ctim {
		return codexTextSnapshot{}, codexProblem("CODEX_CONFIG_CHANGED", "Codex config changed during read")
	}
	return codexTextSnapshot{exists: true, text: string(b), dev: uint64(stat.Dev), ino: stat.Ino, nlink: uint64(stat.Nlink), size: st.Size(), mtime: stat.Mtim.Sec*1e9 + stat.Mtim.Nsec, ctime: stat.Ctim.Sec*1e9 + stat.Ctim.Nsec}, nil
}
func writeExclusive(path string, data []byte, mode os.FileMode) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	ok = true
	return nil
}
func atomicCodexJSON(path string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return atomicCodexText(path, b)
}
func atomicCodexText(path string, data []byte) error {
	tmp := path + "." + randomHexString(16) + ".tmp"
	if e := writeExclusive(tmp, data, 0600); e != nil {
		return e
	}
	if e := os.Rename(tmp, path); e != nil {
		_ = os.Remove(tmp)
		return e
	}
	return syncDir(path)
}
func syncDir(path string) error {
	f, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func replaceSafeText(path string, previous codexTextSnapshot, next string, remove bool) error {
	return replaceSafeTextWithHook(path, previous, next, remove, nil)
}

// replaceSafeTextWithHook retains the portable contract: cooperating writers
// serialize on the Codex lock, and immediately before rename/unlink we recheck
// that the leaf still has the captured inode and contents. POSIX rename/unlink
// has no portable inode-conditional form; an uncooperative writer can still
// race in the tiny interval between this final check and the syscall.
func replaceSafeTextWithHook(path string, previous codexTextSnapshot, next string, remove bool, beforeFinalCheck func()) error {
	now, e := readSafeText(path)
	if e != nil {
		return e
	}
	if now != previous {
		return codexProblem("CODEX_CONFIG_CHANGED", "Codex config changed during the transaction; no concurrent edit was overwritten")
	}
	var staged string
	if !remove && (!previous.exists || previous.text != next) {
		staged = path + "." + randomHexString(16) + ".tmp"
		if e = writeExclusive(staged, []byte(next), 0600); e != nil {
			return codexProblem("CODEX_CONFIG_FAILED", "Cannot stage Codex config")
		}
		defer os.Remove(staged)
	}
	if beforeFinalCheck != nil {
		beforeFinalCheck()
	}
	// Final metadata/content check is deliberately adjacent to rename/unlink.
	final, e := readSafeText(path)
	if e != nil {
		return e
	}
	if final != previous {
		return codexProblem("CODEX_CONFIG_CHANGED", "Codex config changed during the transaction; no concurrent edit was overwritten")
	}
	if remove {
		if previous.exists {
			if e = os.Remove(path); e != nil {
				return codexProblem("CODEX_CONFIG_FAILED", "Cannot remove managed Codex config")
			}
			return syncDir(path)
		}
		return nil
	}
	if staged != "" {
		if e = os.Rename(staged, path); e != nil {
			return codexProblem("CODEX_CONFIG_FAILED", "Cannot atomically write Codex config")
		}
		staged = ""
		if e = syncDir(path); e != nil {
			return codexProblem("CODEX_CONFIG_FAILED", "Cannot durably save Codex config")
		}
	}
	return nil
}

func readCodexJournal(path string) (*codexJournal, error) {
	r, e := readPrivateJSON[codexJournal](path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, codexProblem("CODEX_CONFIG_RECORD_INVALID", "Codex connection record is invalid")
	}
	return &r, nil
}
func readOwner(path string) (*codexOwner, error) {
	o, e := readPrivateJSON[codexOwner](path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, codexProblem("CODEX_CONFIG_RECORD_INVALID", "Codex ownership record is invalid")
	}
	return &o, nil
}
func readPrivateJSON[T any](path string) (T, error) {
	var zero T
	v, e := readSafeText(path)
	if e != nil {
		return zero, e
	}
	if !v.exists {
		return zero, os.ErrNotExist
	}
	var out T
	dec := json.NewDecoder(strings.NewReader(v.text))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil {
		return zero, errors.New("invalid JSON record")
	}
	return out, nil
}

type codexLockObservation struct {
	dev, ino uint64
	pid      int
	nonce    string
	data     []byte
	modTime  time.Time
}

// observeCodexLock opens without following links and binds contents to an inode.
func observeCodexLock(path string) (codexLockObservation, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return codexLockObservation{}, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return codexLockObservation{}, e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || sys.Nlink != 1 {
		return codexLockObservation{}, errors.New("unsafe lock")
	}
	raw, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(raw) > 4096 {
		return codexLockObservation{}, errors.New("invalid lock")
	}
	after, e := f.Stat()
	if e != nil {
		return codexLockObservation{}, e
	}
	as := after.Sys().(*syscall.Stat_t)
	if sys.Dev != as.Dev || sys.Ino != as.Ino || sys.Size != as.Size || sys.Mtim != as.Mtim || sys.Ctim != as.Ctim {
		return codexLockObservation{}, errors.New("lock changed while reading")
	}
	var data struct {
		PID   int    `json:"pid"`
		Nonce string `json:"nonce"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return codexLockObservation{}, errors.New("invalid lock")
	}
	return codexLockObservation{uint64(sys.Dev), sys.Ino, data.PID, data.Nonce, raw, st.ModTime()}, nil
}
func matchesCodexLock(path string, observed codexLockObservation) bool {
	current, e := observeCodexLock(path)
	return e == nil && current.dev == observed.dev && current.ino == observed.ino && current.pid == observed.pid && current.nonce == observed.nonce && bytes.Equal(current.data, observed.data)
}

// reclaimCodexLock verifies the stale observation twice using no-follow reads,
// including inode, pid, nonce and full bytes. Like portable unlink generally,
// the final pathname Remove cannot be an atomic inode-CAS against a hostile
// same-user writer; cooperating processes are serialized by the target lock.
func reclaimCodexLock(path string, observed codexLockObservation) bool {
	if !matchesCodexLock(path, observed) {
		return false
	}
	alive := false
	if observed.pid > 0 {
		e := syscall.Kill(observed.pid, 0)
		alive = e == nil || e == syscall.EPERM
	}
	if alive || observed.pid == 0 && time.Since(observed.modTime) < 30*time.Second {
		return false
	}
	if !matchesCodexLock(path, observed) {
		return false
	}
	return os.Remove(path) == nil
}
func removeCodexLockIfMatches(path string, observed codexLockObservation) {
	if matchesCodexLock(path, observed) {
		_ = os.Remove(path)
	}
}

func withCodexLock(codexHome string, work func(ownerPath string) error) error {
	return withCodexLockHook(codexHome, work, nil)
}

// The marker lock is protected by a stable kernel advisory lock inode. The
// guard is created once and intentionally never unlinked; after process death,
// flock is released by the kernel and the next process can reclaim a stale
// marker while holding the guard. This serializes cooperating reclaimers, not
// arbitrary external processes that ignore the guard protocol.
func withCodexLockHook(codexHome string, work func(ownerPath string) error, afterStaleObservation func()) error {
	if e := ensureSafeDirectory(codexHome, 0700); e != nil {
		return codexProblem("CODEX_CONFIG_UNREADABLE", "CODEX_HOME must be a real directory")
	}
	stateDir := filepath.Join(codexHome, ".adroute")
	if e := ensurePrivateDirectory(stateDir); e != nil {
		return codexProblem("CODEX_CONFIG_UNREADABLE", "Cannot access private Codex lock directory")
	}
	guardPath := filepath.Join(stateDir, "lock.guard")
	guardFD, e := syscall.Open(guardPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return codexProblem("CODEX_CONFIG_BUSY", "Cannot safely open Codex lock guard")
	}
	guard := os.NewFile(uintptr(guardFD), guardPath)
	guardStat, e := guard.Stat()
	if e != nil {
		_ = guard.Close()
		return codexProblem("CODEX_CONFIG_BUSY", "Cannot inspect Codex lock guard")
	}
	guardSys, ok := guardStat.Sys().(*syscall.Stat_t)
	if !ok || !guardStat.Mode().IsRegular() || guardSys.Nlink != 1 {
		_ = guard.Close()
		return codexProblem("CODEX_CONFIG_BUSY", "Codex lock guard is not a private regular file")
	}
	if e = guard.Chmod(0600); e != nil {
		_ = guard.Close()
		return codexProblem("CODEX_CONFIG_BUSY", "Cannot secure Codex lock guard")
	}
	if e = syscall.Flock(guardFD, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = guard.Close()
		return codexProblem("CODEX_CONFIG_BUSY", "Another Codex setup or restore operation is active")
	}
	defer func() { _ = syscall.Flock(guardFD, syscall.LOCK_UN); _ = guard.Close() }()
	lockPath := filepath.Join(stateDir, "lock.json")
	nonce := randomHexString(16)
	for tries := 0; tries < 2; tries++ {
		data, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "nonce": nonce})
		e := writeExclusive(lockPath, data, 0600)
		if e == nil {
			observed, oe := observeCodexLock(lockPath)
			if oe != nil {
				return codexProblem("CODEX_CONFIG_BUSY", "Cannot verify Codex config lock")
			}
			defer func() {
				removeCodexLockIfMatches(lockPath, observed)
			}()
			return work(filepath.Join(stateDir, "owner.json"))
		}
		if !os.IsExist(e) {
			return codexProblem("CODEX_CONFIG_BUSY", "Cannot acquire Codex config lock")
		}
		observed, oe := observeCodexLock(lockPath)
		if oe != nil {
			return codexProblem("CODEX_CONFIG_BUSY", "Codex config operation is locked")
		}
		if !codexLockIsStale(observed) {
			return codexProblem("CODEX_CONFIG_BUSY", "Another Codex config operation is active")
		}
		if afterStaleObservation != nil {
			afterStaleObservation()
		}
		if !reclaimCodexLock(lockPath, observed) {
			return codexProblem("CODEX_CONFIG_BUSY", "Another Codex config operation is active or its lock changed")
		}
	}
	return codexProblem("CODEX_CONFIG_BUSY", "Cannot acquire Codex config lock")
}

func codexLockIsStale(observed codexLockObservation) bool {
	if observed.pid > 0 {
		e := syscall.Kill(observed.pid, 0)
		return e != nil && e != syscall.EPERM
	}
	return time.Since(observed.modTime) >= 30*time.Second
}
