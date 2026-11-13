package adroute

import (
	"adroute/internal/platform"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

type Store struct {
	mu             sync.RWMutex
	home           string
	state          State
	tokens         Tokens
	catalogEnabled bool
	catalogDirty   bool
	catalogErr     error
	writeConfig    func(string, []byte) error
}

func PathsFor(home string) Paths {
	return Paths{Home: home, Config: filepath.Join(home, "config.json"), Tokens: filepath.Join(home, "tokens.json"), Catalog: filepath.Join(home, "codex", "models.json"), Run: filepath.Join(home, "run"), State: filepath.Join(home, "run", "service.json"), Lock: filepath.Join(home, "run", "control.lock"), Logs: filepath.Join(home, "logs"), Log: filepath.Join(home, "logs", "service.log")}
}
func (s *Store) Paths() Paths { return PathsFor(s.home) }

func OpenStore(home string) (*Store, error) {
	if home == "" {
		return nil, fmt.Errorf("home is required")
	}
	var err error
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	if err := privateDirectory(home); err != nil {
		return nil, err
	}
	paths := PathsFor(home)
	for _, dir := range []string{paths.Run, paths.Logs} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := privateDirectory(dir); err != nil {
			return nil, err
		}
	}
	s := &Store{home: home}
	data, err := readPrivateFile(paths.Config)
	if err == nil {
		if err = json.Unmarshal(data, &s.state); err != nil {
			return nil, appError("CONFIG_INVALID: invalid configuration preserved", 500, "CONFIG_INVALID")
		}
		if err = ValidateState(s.state); err != nil {
			return nil, appError("CONFIG_INVALID: invalid configuration preserved", 500, "CONFIG_INVALID")
		}
		for _, raw := range s.state.Adapters {
			var pack AdapterPackage
			if json.Unmarshal(raw, &pack) != nil {
				return nil, appError("CONFIG_INVALID: invalid configuration preserved", 500, "CONFIG_INVALID")
			}
			verification, verifyErr := VerifyPackage(pack)
			if verifyErr != nil || !verification.ReplayPassed {
				return nil, appError("CONFIG_INVALID: adapter replay verification failed; configuration preserved", 500, "CONFIG_INVALID")
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		s.state = State{Version: ConfigVersion, Groups: []Group{}, Models: []Model{}, Aggregates: []Aggregate{}}
		if err = s.persist(s.state); err != nil {
			return nil, err
		}
	}
	if err = s.loadTokens(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) loadTokens() error {
	p := filepath.Join(s.home, "tokens.json")
	b, e := readPrivateFile(p)
	if e == nil {
		if decodeStrict(b, &s.tokens) != nil || !validToken(s.tokens.AdminToken) || !validToken(s.tokens.ProxyToken) || strings.TrimSpace(s.tokens.InstallationID) == "" {
			return appError("TOKENS_INVALID", 500, "TOKENS_INVALID")
		}
		return nil
	}
	if !os.IsNotExist(e) {
		return e
	}
	admin, err := randomHex(32)
	if err != nil {
		return err
	}
	proxy, err := randomHex(32)
	if err != nil {
		return err
	}
	installation, err := newID()
	if err != nil {
		return err
	}
	s.tokens = Tokens{AdminToken: admin, ProxyToken: proxy, InstallationID: installation}
	encoded, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(p, append(encoded, '\n'))
}
func randomHexFrom(source io.Reader, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(source, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func randomHex(n int) (string, error) { return randomHexFrom(rand.Reader, n) }
func atomicWrite(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".adroute-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = platform.ChmodFile(f, 0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = io.Copy(f, bytes.NewReader(b)); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return platform.SyncDir(path)
}
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data directory must be a real directory")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.IsDir() {
		return fmt.Errorf("data directory changed while opening")
	}
	return platform.ChmodFile(f, 0700)
}
func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("data file must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("data file changed while opening")
	}
	if err = platform.ChmodFile(f, 0600); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}
func validToken(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil && strings.ToLower(v) == v
}

// LoadTokens reads the existing daemon credential file without creating it or
// touching model configuration. It is for recovery/control-plane identity checks.
func LoadTokens(home string) (Tokens, error) {
	var tokens Tokens
	data, err := readPrivateFile(filepath.Join(home, "tokens.json"))
	if err != nil {
		return tokens, err
	}
	if json.Unmarshal(data, &tokens) != nil || !validToken(tokens.AdminToken) || !validToken(tokens.ProxyToken) || strings.TrimSpace(tokens.InstallationID) == "" {
		return Tokens{}, appError("TOKENS_INVALID", 500, "TOKENS_INVALID")
	}
	return tokens, nil
}
func validUUID(v string) bool {
	return len(v) == 36 && v[8] == '-' && v[13] == '-' && v[18] == '-' && v[23] == '-'
}
func (s *Store) persist(st State) error {
	b, e := json.MarshalIndent(st, "", "  ")
	if e != nil {
		return e
	}
	path := filepath.Join(s.home, "config.json")
	if s.writeConfig != nil {
		return s.writeConfig(path, append(b, '\n'))
	}
	return atomicWrite(path, append(b, '\n'))
}
func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}
func (s *Store) mutate(f func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := cloneState(s.state)
	if e := f(&n); e != nil {
		return e
	}
	n.Revision++
	if e := ValidateState(n); e != nil {
		return e
	}
	if e := s.persist(n); e != nil {
		return e
	}
	s.state = n
	if s.catalogEnabled {
		if e := s.publishCatalogLocked(); e != nil {
			s.catalogDirty = true
			s.catalogErr = e
		} else {
			s.catalogDirty = false
			s.catalogErr = nil
		}
	}
	return nil
}
func (s *Store) publishCatalogLocked() error {
	dir := filepath.Join(s.home, "codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := privateDirectory(dir); err != nil {
		return err
	}
	b, err := json.MarshalIndent(catalogFor(s.state), "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "models.json"), append(b, '\n'))
}

// RetryCatalogPublication retries the post-commit catalog side effect. Config
// and in-memory state are already committed whenever publication is pending.
func (s *Store) RetryCatalogPublication() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.catalogEnabled {
		return nil
	}
	if err := s.publishCatalogLocked(); err != nil {
		s.catalogDirty = true
		s.catalogErr = err
		return err
	}
	s.catalogDirty = false
	s.catalogErr = nil
	return nil
}
func (s *Store) CatalogPublicationStatus() (pending bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.catalogDirty, s.catalogErr
}
func (s *Store) AdminState() State { return s.Snapshot() }

func (s *Store) Secrets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := []string{s.tokens.AdminToken, s.tokens.ProxyToken}
	for _, m := range s.state.Models {
		v = append(v, m.APIKey)
	}
	return v
}
func (s *Store) TokenPair() Tokens { return s.tokens }
func (s *Store) AuthorizeAdmin(candidate string) bool {
	return ConstantTimeToken(candidate, s.tokens.AdminToken)
}
func (s *Store) AuthorizeProxy(candidate string) bool {
	return ConstantTimeToken(candidate, s.tokens.ProxyToken)
}

func (s *Store) Resolve(model string) (Route, error) {
	s.mu.RLock()
	st := s.state
	s.mu.RUnlock()
	return ResolveState(st, model)
}

// ModelRoute resolves one saved model entry by its stable internal id. It is
// used by management features (diagnostics, repair prompt, connection tests).
func (s *Store) ModelRoute(id string) (Route, error) {
	snapshot := s.Snapshot()
	for _, m := range snapshot.Models {
		if m.ID == id {
			return routeFor(snapshot, m), nil
		}
	}
	return Route{}, appError("model entry not found", 404, "MODEL_NOT_FOUND")
}

func routeFor(st State, m Model) Route {
	group := ""
	for _, g := range st.Groups {
		if g.ID == m.GroupID {
			group = g.Name
		}
	}
	return Route{ID: m.ID, GroupName: group, ModelName: m.Name, RouteID: PublicModelID(group, m.Name), Model: m.Model, BaseURL: m.BaseURL, APIKey: m.APIKey, Profile: m.Profile, Disabled: m.Disabled}
}

func ResolveState(st State, model string) (Route, error) {
	if model == "" || utf16Length(model) > 300 || strings.TrimSpace(model) == "" || strings.ContainsFunc(model, unicode.IsControl) {
		return Route{}, appError("invalid model", 400, "INVALID_MODEL")
	}
	if err := publicExposureError(st, model); err != nil {
		return Route{}, err
	}
	if model == DefaultRouteID {
		if st.DefaultModelID != "" {
			for _, m := range st.Models {
				if m.ID == st.DefaultModelID && !m.Disabled {
					return routeFor(st, m), nil
				}
			}
		}
		return Route{}, appError("no active model", 503, "NO_DEFAULT_MODEL")
	}
	group, name, ok := ParsePublicModelID(model)
	if !ok {
		return Route{}, appError("model route not found", 404, "MODEL_ROUTE_NOT_FOUND")
	}
	found := false
	for _, m := range st.Models {
		r := routeFor(st, m)
		if r.GroupName != group || r.ModelName != name {
			continue
		}
		if m.Disabled {
			found = true
			continue
		}
		return r, nil
	}
	if found {
		return Route{}, appError("model route is disabled", 403, "MODEL_ROUTE_DISABLED")
	}
	return Route{}, appError("model route not found", 404, "MODEL_ROUTE_NOT_FOUND")
}

// ResolveChain expands a public model id into the ordered upstream candidates
// behind it. A plain model resolves to exactly one candidate; an aggregate
// resolves to its enabled members in configured order, repeated MaxRounds times.
func ResolveChain(st State, model string) ([]Route, error) {
	if model == "" || utf16Length(model) > 300 || strings.TrimSpace(model) == "" || strings.ContainsFunc(model, unicode.IsControl) {
		return nil, appError("invalid model", 400, "INVALID_MODEL")
	}
	if err := publicExposureError(st, model); err != nil {
		return nil, err
	}
	if model == DefaultRouteID {
		if st.DefaultModelID == "" {
			return nil, appError("no active model", 503, "NO_DEFAULT_MODEL")
		}
		for _, m := range st.Models {
			if m.ID == st.DefaultModelID && !m.Disabled {
				return []Route{routeFor(st, m)}, nil
			}
		}
		for i := range st.Aggregates {
			if st.Aggregates[i].ID == st.DefaultModelID && !st.Aggregates[i].Disabled {
				return aggregateChain(st, &st.Aggregates[i])
			}
		}
		return nil, appError("no active model", 503, "NO_DEFAULT_MODEL")
	}
	group, name, ok := ParsePublicModelID(model)
	if !ok {
		return nil, appError("model route not found", 404, "MODEL_ROUTE_NOT_FOUND")
	}
	if group == aggregateNamespace {
		agg := findAggregateByName(st, name)
		if agg == nil {
			return nil, appError("aggregate route not found", 404, "AGGREGATE_NOT_FOUND")
		}
		if agg.Disabled {
			return nil, appError("aggregate route is disabled", 403, "AGGREGATE_DISABLED")
		}
		return aggregateChain(st, agg)
	}
	found := false
	for _, m := range st.Models {
		r := routeFor(st, m)
		if r.GroupName != group || r.ModelName != name {
			continue
		}
		if m.Disabled {
			found = true
			continue
		}
		return []Route{r}, nil
	}
	if found {
		return nil, appError("model route is disabled", 403, "MODEL_ROUTE_DISABLED")
	}
	return nil, appError("model route not found", 404, "MODEL_ROUTE_NOT_FOUND")
}

// publicExposureError gates only the requested public entry, never its members.
// A default alias is subject to the target's exposure without clearing the
// internal default selection.
func publicExposureError(st State, model string) error {
	for _, m := range st.Models {
		if (model == DefaultRouteID && st.DefaultModelID == m.ID) || model == routeFor(st, m).RouteID {
			if m.ExposureDisabled {
				return appError("model is not publicly exposed", 403, "MODEL_NOT_EXPOSED")
			}
			return nil
		}
	}
	for _, a := range st.Aggregates {
		if (model == DefaultRouteID && st.DefaultModelID == a.ID) || model == AggregateRouteID(a.Name) {
			if a.ExposureDisabled {
				return appError("aggregate is not publicly exposed", 403, "AGGREGATE_NOT_EXPOSED")
			}
			return nil
		}
	}
	return nil
}

// aggregateChain resolves one open aggregate into its ordered member routes,
// expanded for the configured number of retry rounds.
func aggregateChain(st State, agg *Aggregate) ([]Route, error) {
	members := aggregateMembers(st, agg)
	if len(members) == 0 {
		return nil, appError("聚合的所有成员都不可用。", 503, "AGGREGATE_EMPTY")
	}
	rounds := agg.MaxRounds
	if rounds < 1 || rounds > MaxAggregateRounds {
		rounds = DefaultMaxRounds
	}
	chain := []Route{}
	for i := 0; i < rounds; i++ {
		chain = append(chain, members...)
	}
	return chain, nil
}

func (s *Store) RoutingSnapshot() func(string) ([]Route, error) {
	snapshot := s.Snapshot()
	return func(model string) ([]Route, error) { return ResolveChain(snapshot, model) }
}
func (s *Store) Catalog() map[string]any {
	st := s.Snapshot()
	return catalogFor(st)
}
func catalogFor(st State) map[string]any {
	models := []map[string]any{}
	if hasPublicDefault(st) {
		models = append(models, catalogEntry(DefaultRouteID, defaultModelName(st), 0, defaultContextWindow(st)))
	}
	for _, m := range st.Models {
		if m.Disabled || m.ExposureDisabled {
			continue
		}
		r := routeFor(st, m)
		models = append(models, catalogEntry(r.RouteID, r.GroupName+" / "+r.ModelName, len(models), m.ContextWindow))
	}
	for _, agg := range st.Aggregates {
		if agg.Disabled || agg.ExposureDisabled || len(aggregateMembers(st, &agg)) == 0 {
			continue
		}
		models = append(models, catalogEntry(AggregateRouteID(agg.Name), "聚合 / "+agg.Name, len(models), aggregateContextWindow(st, &agg)))
	}
	return map[string]any{"models": models}
}

// defaultContextWindow is the window of whatever adroute/default resolves to.
func defaultContextWindow(st State) int {
	for _, m := range st.Models {
		if m.ID == st.DefaultModelID {
			return m.ContextWindow
		}
	}
	for _, a := range st.Aggregates {
		if a.ID == st.DefaultModelID {
			return aggregateContextWindow(st, &a)
		}
	}
	return 0
}

// aggregateContextWindow publishes the smallest window among members, and only
// when every member declares one. A failover chain tries members in order, so
// the largest value would promise a capacity the weakest member cannot honor;
// an undeclared member would make any total a guess.
func aggregateContextWindow(st State, agg *Aggregate) int {
	smallest := 0
	for _, id := range agg.Members {
		index := modelIndex(&st, id)
		if index < 0 || st.Models[index].ContextWindow == 0 {
			return 0
		}
		if window := st.Models[index].ContextWindow; smallest == 0 || window < smallest {
			smallest = window
		}
	}
	return smallest
}

func hasPublicDefault(st State) bool {
	return hasEnabledDefault(st) && publicExposureError(st, DefaultRouteID) == nil
}

func hasEnabledDefault(st State) bool {
	if st.DefaultModelID == "" {
		return false
	}
	for _, m := range st.Models {
		if m.ID == st.DefaultModelID && !m.Disabled {
			return true
		}
	}
	for _, a := range st.Aggregates {
		if a.ID == st.DefaultModelID && !a.Disabled {
			return true
		}
	}
	return false
}

const catalogInstructions = "You are a coding assistant. Follow the user instructions, use the available tools when appropriate, and verify changes before reporting results."

func defaultModelName(st State) string {
	for _, m := range st.Models {
		if m.ID == st.DefaultModelID {
			r := routeFor(st, m)
			return "跟随网页默认 · " + r.GroupName + " / " + r.ModelName
		}
	}
	for _, a := range st.Aggregates {
		if a.ID == st.DefaultModelID {
			return "跟随网页默认 · 聚合 / " + a.Name
		}
	}
	return "跟随网页默认路由"
}

// catalogEntry renders one model for the Codex catalog. contextWindow is
// omitted when zero so Codex keeps its own fallback rather than trusting a
// number adroute does not know. max_context_window is deliberately not sent:
// Codex derives it from context_window.
func catalogEntry(slug, name string, priority int, contextWindow int) map[string]any {
	entry := map[string]any{"slug": slug, "display_name": name, "priority": priority, "visibility": "list", "supported_in_api": true, "default_reasoning_level": defaultReasoningLevel, "supported_reasoning_levels": codexReasoningLevels(), "supports_reasoning_summary_parameter": false, "support_verbosity": false, "shell_type": "unified_exec", "truncation_policy": map[string]any{"mode": "tokens", "limit": 10000}, "experimental_supported_tools": []any{}, "input_modalities": []string{"text"}, "base_instructions": catalogInstructions}
	if contextWindow > 0 {
		entry["context_window"] = contextWindow
	}
	return entry
}

// OpenAIModelList is the public GET /v1/models payload. The id itself is the
// public route id so clients that only render ids still select correctly.
func (s *Store) OpenAIModelList() map[string]any {
	st := s.Snapshot()
	out := []map[string]any{}
	for _, group := range groupOrder(st) {
		for _, m := range st.Models {
			if m.GroupID != group.ID || m.Disabled || m.ExposureDisabled {
				continue
			}
			r := routeFor(st, m)
			out = append(out, map[string]any{"id": r.RouteID, "name": r.GroupName + " / " + r.ModelName, "display_name": r.GroupName + " / " + r.ModelName, "object": "model", "owned_by": r.GroupName})
		}
	}
	for _, agg := range st.Aggregates {
		if agg.Disabled || agg.ExposureDisabled || len(aggregateMembers(st, &agg)) == 0 {
			continue
		}
		name := "聚合 / " + agg.Name
		out = append(out, map[string]any{"id": AggregateRouteID(agg.Name), "name": name, "display_name": name, "object": "model", "owned_by": aggregateNamespace})
	}
	if hasPublicDefault(st) {
		out = append(out, map[string]any{"id": DefaultRouteID, "name": defaultModelName(st), "display_name": defaultModelName(st), "object": "model", "owned_by": "adroute"})
	}
	return map[string]any{"object": "list", "data": out}
}
func (s *Store) EnableCatalog() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.publishCatalogLocked(); err != nil {
		return err
	}
	s.catalogEnabled = true
	s.catalogDirty = false
	s.catalogErr = nil
	return nil
}
func NormalizeBaseURL(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || utf16Length(raw) > 2048 {
		return "", appError("invalid base URL", 400, "INVALID_REQUEST")
	}
	if strings.HasSuffix(u.Path, "/responses") || strings.HasSuffix(u.Path, "/models") || strings.HasSuffix(u.Path, "/chat/completions") {
		return "", appError("base URL must be API root", 400, "INVALID_REQUEST")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if h, port, e := net.SplitHostPort(u.Host); e == nil && ((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		u.Host = h
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// NormalizeWebsite validates the optional model website link. Empty stays
// empty; otherwise it must be an http(s) URL and is trimmed of a trailing slash.
func NormalizeWebsite(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	u, e := url.Parse(value)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || utf16Length(value) > 2048 {
		return "", appError("网站地址无效；请填写 http:// 或 https:// 开头的完整网址，或留空。", 400, "INVALID_REQUEST")
	}
	return strings.TrimRight(value, "/"), nil
}

func ConstantTimeToken(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
