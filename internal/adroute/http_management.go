package adroute

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *HTTPApp) stateJSON() map[string]any {
	st := a.opts.Store.AdminState()
	groups := []map[string]any{}
	for _, g := range st.Groups {
		total, open := 0, 0
		for _, m := range st.Models {
			if m.GroupID != g.ID {
				continue
			}
			total++
			if !m.Disabled && !m.ExposureDisabled {
				open++
			}
		}
		groups = append(groups, map[string]any{"id": g.ID, "name": g.Name, "archived": g.Archived, "modelCount": total, "openCount": open})
	}
	models := []map[string]any{}
	for _, m := range st.Models {
		r := routeFor(st, m)
		models = append(models, map[string]any{"id": m.ID, "groupId": m.GroupID, "groupName": r.GroupName, "name": m.Name, "model": m.Model, "baseUrl": m.BaseURL, "apiKey": m.APIKey, "website": m.Website, "disabled": m.Disabled, "exposureDisabled": m.ExposureDisabled, "favorite": m.Favorite, "archived": m.Archived, "contextWindow": m.ContextWindow, "open": !m.Disabled && !m.ExposureDisabled, "profile": m.Profile, "routeId": r.RouteID})
	}
	aggregates := []map[string]any{}
	for _, agg := range st.Aggregates {
		members := []map[string]any{}
		open := 0
		for _, member := range agg.Members {
			entry := map[string]any{"modelId": member}
			if idx := modelIndex(&st, member); idx >= 0 {
				m := st.Models[idx]
				r := routeFor(st, m)
				entry["groupName"] = r.GroupName
				entry["name"] = m.Name
				entry["routeId"] = r.RouteID
				entry["disabled"] = m.Disabled
				if !m.Disabled {
					open++
				}
			} else {
				entry["missing"] = true
			}
			members = append(members, entry)
		}
		aggregates = append(aggregates, map[string]any{"id": agg.ID, "name": agg.Name, "routeId": AggregateRouteID(agg.Name), "members": members, "memberCount": len(agg.Members), "openMemberCount": open, "maxRounds": agg.MaxRounds, "disabled": agg.Disabled, "exposureDisabled": agg.ExposureDisabled, "favorite": agg.Favorite, "archived": agg.Archived, "open": !agg.Disabled && !agg.ExposureDisabled})
	}
	out := map[string]any{
		"version":        ServiceVersion,
		"revision":       st.Revision,
		"configVersion":  st.Version,
		"url":            fmt.Sprintf("http://127.0.0.1:%d", a.opts.Port),
		"proxyUrl":       fmt.Sprintf("http://127.0.0.1:%d/v1", a.opts.Port),
		"groups":         groups,
		"models":         models,
		"aggregates":     aggregates,
		"defaultModelId": st.DefaultModelID,
		"defaultRouteId": DefaultRouteID,
		"capabilities": map[string]any{
			"groups":             true,
			"models":             true,
			"aggregates":         true,
			"modelAvailability":  true,
			"modelExposure":      true,
			"exposureBulk":       true,
			"organization":       true,
			"modelCopy":          true,
			"configTransfer":     true,
			"quickTest":          true,
			"quickTestTimeoutMs": QuickTestTimeout.Milliseconds(),
			"compatibility":      true,
			"adapterDirectory":   true,
			"clients":            true,
		},
	}
	a.requestMu.Lock()
	requests := append([]map[string]any{}, a.requests...)
	a.requestMu.Unlock()
	out["requests"] = requests
	pending, _ := a.opts.Store.CatalogPublicationStatus()
	publication := map[string]any{"pending": pending}
	if pending {
		publication["message"] = "模型配置已保存，但 Codex 模型目录发布失败。处理文件或权限问题后，请重试发布目录。"
	}
	out["catalogPublication"] = publication
	return out
}

func (a *HTTPApp) upstreams() *Upstreams {
	return NewUpstreams(a.opts.Client, a.opts.Port)
}

// managementReq is one management API request with its path already parsed.
// Handlers take it instead of closing over a 400-line function, so each route
// can be read and tested on its own.
type managementReq struct {
	app   *HTTPApp
	w     http.ResponseWriter
	r     *http.Request
	path  string
	parts []string
	store *Store
}

func (m *managementReq) writeState(status int) { m.app.json(m.w, status, m.app.stateJSON()) }

// decode reads the body strictly: unknown fields, trailing content and (except
// for api/default) explicit nulls are rejected.
func (m *managementReq) decode(limit int64, v any) bool {
	body, err := decodeBody(m.r, limit)
	if err == nil {
		if m.path == "api/default" {
			err = strictManagementJSON(body, v, "modelId")
		} else {
			err = strictManagementJSON(body, v)
		}
	}
	if err != nil {
		m.app.fail(m.w, err)
		return false
	}
	return true
}

// requireEmptyBody accepts only a blank body, for endpoints that take no input.
func (m *managementReq) requireEmptyBody(rejection string) bool {
	body, err := decodeBody(m.r, 64<<10)
	if err != nil {
		m.app.fail(m.w, err)
		return false
	}
	if len(bytes.TrimSpace(body)) != 0 {
		m.app.fail(m.w, appError(rejection, 400, "INVALID_REQUEST"))
		return false
	}
	return true
}

// decodeEnabledFlag reads the single {enabled} flag shared by the exposure and
// availability endpoints. A missing, null or non-boolean value is rejected, so
// a typo can never be read as "close everything".
func (m *managementReq) decodeEnabledFlag() (bool, bool) {
	var v struct {
		Enabled *bool `json:"enabled"`
	}
	if !m.decode(64<<10, &v) {
		return false, false
	}
	if v.Enabled == nil {
		m.app.fail(m.w, appError("enabled must be a boolean", 400, "INVALID_REQUEST"))
		return false, false
	}
	return *v.Enabled, true
}

// managementRoute binds one method and path pattern to a handler. A pattern
// segment is a literal, "*" for one captured segment, or "a|b" for a choice of
// literals. Routes are tried in order and the first match wins.
type managementRoute struct {
	method  string
	pattern string
	handle  func(*managementReq, string)
}

var managementRoutes = []managementRoute{
	{http.MethodGet, "api/state", (*managementReq).getState},
	{http.MethodPut, "api/models|aggregates/*/exposure", (*managementReq).setExposure},
	{http.MethodPut, "api/exposure/models", (*managementReq).setAllModelExposure},
	{http.MethodPut, "api/exposure/aggregates", (*managementReq).setAllAggregateExposure},
	{http.MethodPut, "api/models/*/params", (*managementReq).setModelParams},
	{http.MethodPut, "api/groups|models|aggregates/*/organization", (*managementReq).setOrganization},
	{http.MethodGet, "api/config/export", (*managementReq).exportConfig},
	{http.MethodPost, "api/config/import", (*managementReq).importConfig},
	{http.MethodPost, "api/catalog/retry", (*managementReq).retryCatalog},
	{http.MethodGet, "api/setup", (*managementReq).getSetup},
	{http.MethodPost, "api/groups", (*managementReq).createGroup},
	{http.MethodPut, "api/groups/*", (*managementReq).updateGroup},
	{http.MethodDelete, "api/groups/*", (*managementReq).deleteGroup},
	{http.MethodPost, "api/aggregates", (*managementReq).createAggregate},
	{http.MethodPut, "api/aggregates/*", (*managementReq).updateAggregate},
	{http.MethodDelete, "api/aggregates/*", (*managementReq).deleteAggregate},
	{http.MethodPut, "api/aggregates/*/availability", (*managementReq).setAggregateAvailability},
	{http.MethodPost, "api/models/discover", (*managementReq).discoverModels},
	{http.MethodPost, "api/models", (*managementReq).createModel},
	{http.MethodPost, "api/models/*/copy", (*managementReq).copyModel},
	{http.MethodPost, "api/models/*/discover", (*managementReq).discoverModel},
	{http.MethodPut, "api/models/*", (*managementReq).updateModel},
	{http.MethodDelete, "api/models/*", (*managementReq).deleteModel},
	{http.MethodPut, "api/models/*/availability", (*managementReq).setModelAvailability},
	{http.MethodPost, "api/models/*/quick-test", (*managementReq).quickTest},
	{http.MethodPost, "api/models/*/tool-structure-test", (*managementReq).toolStructureTest},
	{http.MethodPost, "api/default", (*managementReq).setDefault},
	{http.MethodPost, "api/opencode/sync", (*managementReq).syncOpenCode},
}

// matchManagementPattern reports whether path fits pattern and returns the
// segment captured by "*".
func matchManagementPattern(pattern, path string) (string, bool) {
	ps := strings.Split(pattern, "/")
	xs := strings.Split(path, "/")
	if len(ps) != len(xs) {
		return "", false
	}
	captured := ""
	for i, seg := range ps {
		switch {
		case seg == "*":
			captured = xs[i]
		case strings.Contains(seg, "|"):
			matched := false
			for _, alt := range strings.Split(seg, "|") {
				if alt == xs[i] {
					matched = true
					break
				}
			}
			if !matched {
				return "", false
			}
		case seg != xs[i]:
			return "", false
		}
	}
	return captured, true
}

func (a *HTTPApp) managementAPI(w http.ResponseWriter, r *http.Request) bool {
	m := &managementReq{app: a, w: w, r: r, path: strings.Trim(r.URL.Path, "/"), store: a.opts.Store}
	m.parts = strings.Split(m.path, "/")
	if strings.HasPrefix(m.path, "api/clients") {
		// The client endpoints decline paths they do not own, so a miss keeps
		// falling through to the rest of the table.
		if a.clientsAPI(w, r, m.path, m.parts) {
			return true
		}
	}
	for _, route := range managementRoutes {
		if route.method != r.Method {
			continue
		}
		arg, ok := matchManagementPattern(route.pattern, m.path)
		if !ok {
			continue
		}
		route.handle(m, arg)
		return true
	}
	if strings.HasPrefix(m.path, "api/compatibility") {
		a.compatibilityAPI(w, r, m.path, m.parts, m.decode, m.writeState)
		return true
	}
	return false
}

func (m *managementReq) getState(string) {
	m.app.json(m.w, 200, m.app.stateJSON())
}

func (m *managementReq) setExposure(id string) {
	enabled, ok := m.decodeEnabledFlag()
	if !ok {
		return
	}
	var err error
	if m.parts[1] == "models" {
		err = m.store.SetModelExposure(id, enabled)
	} else {
		err = m.store.SetAggregateExposure(id, enabled)
	}
	if err != nil {
		m.app.fail(m.w, err)
	} else {
		m.writeState(200)
	}
}

// setAllModelExposure flips public access for every model entry at once. It is
// the bulk counterpart of setExposure and deliberately does not touch aggregates.
func (m *managementReq) setAllModelExposure(string) {
	enabled, ok := m.decodeEnabledFlag()
	if !ok {
		return
	}
	if _, e := m.store.SetAllModelExposure(enabled); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

// setAllAggregateExposure is the aggregate page's own switch; it never reads or
// writes model exposure.
func (m *managementReq) setAllAggregateExposure(string) {
	enabled, ok := m.decodeEnabledFlag()
	if !ok {
		return
	}
	if _, e := m.store.SetAllAggregateExposure(enabled); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

// setModelParams is a narrow endpoint on purpose: editing one published number
// must not require resending group, name, upstream id and credential, where one
// stale field would silently rewrite the entry.
func (m *managementReq) setModelParams(id string) {
	var v struct {
		ContextWindow *int `json:"contextWindow"`
	}
	if !m.decode(64<<10, &v) {
		return
	}
	if v.ContextWindow == nil {
		m.app.fail(m.w, appError("contextWindow 必须是整数；传 0 表示不设置。", 400, "INVALID_REQUEST"))
		return
	}
	if e := m.store.SetModelContextWindow(id, *v.ContextWindow); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) setOrganization(id string) {
	var input OrganizationInput
	if !m.decode(64<<10, &input) {
		return
	}
	if err := m.store.SetOrganization(m.parts[1], id, input); err != nil {
		m.app.fail(m.w, err)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) exportConfig(string) {
	m.app.json(m.w, http.StatusOK, configBackup{Format: "adroute-config", Version: 1, Config: m.store.Snapshot()})
}

func (m *managementReq) importConfig(string) {
	body, err := decodeBody(m.r, 128<<20)
	var input configImport
	if err == nil && decodeStrict(body, &input) != nil {
		err = appError("配置文件格式错误，现有配置未改变。", 400, "INVALID_BACKUP")
	}
	if err == nil {
		err = m.store.importConfig(input)
	}
	if err != nil {
		m.app.fail(m.w, err)
	} else {
		m.writeState(http.StatusOK)
	}
}

func (m *managementReq) retryCatalog(string) {
	if !m.requireEmptyBody("目录重试请求不接受参数。") {
		return
	}
	if e := m.store.RetryCatalogPublication(); e != nil {
		m.app.fail(m.w, appError("配置仍已保存，但客户端目录发布失败。请检查目录文件及权限后重试。", 503, "CATALOG_PUBLICATION_FAILED"))
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) getSetup(string) {
	tok := m.store.TokenPair().ProxyToken
	base := fmt.Sprintf("http://127.0.0.1:%d/v1", m.app.opts.Port)
	config := fmt.Sprintf("# adroute start 自动合并到 Codex 配置；此处为配置预览\nmodel_provider = \"adroute\"\nmodel = %q\nmodel_catalog_json = %q\n\n[model_providers.adroute]\nname = \"adroute\"\nbase_url = %q\nwire_api = \"responses\"\nexperimental_bearer_token = %q\nrequires_openai_auth = false\nsupports_websockets = false\n", DefaultRouteID, m.store.Paths().Catalog, base, tok)
	m.app.json(m.w, 200, map[string]any{"proxyUrl": base, "proxyToken": tok, "codexConfig": config, "shellSetup": "adroute start\ncodex"})
}

func (m *managementReq) createGroup(string) {
	var v GroupInput
	if !m.decode(64<<10, &v) {
		return
	}
	if _, e := m.store.CreateGroup(v); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(201)
	}
}

func (m *managementReq) updateGroup(id string) {
	var v GroupInput
	if !m.decode(64<<10, &v) {
		return
	}
	if e := m.store.RenameGroup(id, v); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) deleteGroup(id string) {
	if e := m.store.DeleteGroup(id); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) createAggregate(string) {
	var v AggregateInput
	if !m.decode(64<<10, &v) {
		return
	}
	if _, e := m.store.CreateAggregate(v); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(201)
	}
}

func (m *managementReq) updateAggregate(id string) {
	var v AggregateInput
	if !m.decode(64<<10, &v) {
		return
	}
	if e := m.store.UpdateAggregate(id, v); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) deleteAggregate(id string) {
	if e := m.store.DeleteAggregate(id); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) setAggregateAvailability(id string) {
	enabled, ok := m.decodeEnabledFlag()
	if !ok {
		return
	}
	if e := m.store.SetAggregateEnabled(id, enabled); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) discoverModels(string) {
	var v DiscoverInput
	if !m.decode(64<<10, &v) {
		return
	}
	result, e := m.app.upstreams().Discover(m.r.Context(), v.BaseURL, v.APIKey, 30*time.Second)
	if e != nil {
		m.app.fail(m.w, e)
	} else {
		m.app.json(m.w, 200, result)
	}
}

func (m *managementReq) createModel(string) {
	var v ModelInput
	if !m.decode(64<<10, &v) {
		return
	}
	if _, e := m.store.CreateModel(v); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(201)
	}
}

func (m *managementReq) copyModel(id string) {
	if !m.requireEmptyBody("复制请求不接受参数。") {
		return
	}
	if _, e := m.store.CopyModel(id); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(201)
	}
}

// discoverModel may reuse the saved credential of an existing model while the
// form still lets a new (unsaved) base URL or credential win.
func (m *managementReq) discoverModel(id string) {
	var v struct {
		BaseURL string `json:"baseUrl,omitempty"`
		APIKey  string `json:"apiKey,omitempty"`
	}
	if !m.decode(64<<10, &v) {
		return
	}
	saved, e := m.store.ModelRoute(id)
	if e != nil {
		m.app.fail(m.w, e)
		return
	}
	base := strings.TrimSpace(v.BaseURL)
	if base == "" {
		base = saved.BaseURL
	}
	key := strings.TrimSpace(v.APIKey)
	if key == "" {
		key = saved.APIKey
	}
	result, e := m.app.upstreams().Discover(m.r.Context(), base, key, 30*time.Second)
	if e != nil {
		m.app.fail(m.w, e)
	} else {
		m.app.json(m.w, 200, result)
	}
}

func (m *managementReq) updateModel(id string) {
	var v ModelInput
	if !m.decode(64<<10, &v) {
		return
	}
	if e := m.store.UpdateModel(id, v); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) deleteModel(id string) {
	if e := m.store.DeleteModel(id); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) setModelAvailability(id string) {
	enabled, ok := m.decodeEnabledFlag()
	if !ok {
		return
	}
	if e := m.store.SetModelEnabled(id, enabled); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) quickTest(id string) {
	if !m.requireEmptyBody("测速请求不接受参数。") {
		return
	}
	route, e := m.store.ModelRoute(id)
	if e != nil {
		m.app.fail(m.w, e)
		return
	}
	result, e := m.app.upstreams().QuickTest(m.r.Context(), route)
	if e != nil {
		m.app.fail(m.w, e)
	} else {
		m.app.json(m.w, 200, result)
	}
}

func (m *managementReq) toolStructureTest(id string) {
	if !m.requireEmptyBody("工具结构测试不接受参数。") {
		return
	}
	route, e := m.store.ModelRoute(id)
	if e != nil {
		m.app.fail(m.w, e)
		return
	}
	result, e := m.app.upstreams().ToolStructureTest(m.r.Context(), route)
	if e != nil {
		m.app.fail(m.w, e)
	} else {
		m.app.json(m.w, 200, result)
	}
}

func (m *managementReq) setDefault(string) {
	var v struct {
		ModelID json.RawMessage `json:"modelId"`
	}
	if !m.decode(64<<10, &v) {
		return
	}
	id := ""
	if len(v.ModelID) == 0 {
		m.app.fail(m.w, appError("必须提供 modelId；清空默认请显式传 null 或空字符串。", 400, "INVALID_REQUEST"))
		return
	}
	if !bytes.Equal(bytes.TrimSpace(v.ModelID), []byte("null")) {
		if e := json.Unmarshal(v.ModelID, &id); e != nil {
			m.app.fail(m.w, appError("modelId 必须是字符串或 null。", 400, "INVALID_REQUEST"))
			return
		}
	}
	if e := m.store.SetDefault(id); e != nil {
		m.app.fail(m.w, e)
	} else {
		m.writeState(200)
	}
}

func (m *managementReq) syncOpenCode(string) {
	var v struct {
		ConfigPath string `json:"configPath,omitempty"`
	}
	if !m.decode(64<<10, &v) {
		return
	}
	models := []SyncModel{}
	catalog := m.store.Catalog()
	if entries, ok := catalog["models"].([]map[string]any); ok {
		for _, entry := range entries {
			slug, _ := entry["slug"].(string)
			name, _ := entry["display_name"].(string)
			window, _ := entry["context_window"].(int)
			models = append(models, SyncModel{Slug: slug, DisplayName: name, ContextWindow: window})
		}
	}
	res, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, ConfigPath: v.ConfigPath})
	if e != nil {
		m.app.fail(m.w, e)
	} else {
		m.app.json(m.w, 200, res)
	}
}

func strictManagementJSON(b []byte, v any, nullable ...string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return appError("JSON 请求格式错误。", 400, "INVALID_JSON")
	}
	if fields == nil {
		return appError("请求必须是 JSON 对象。", 400, "INVALID_JSON")
	}
	for name, raw := range fields {
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		allowed := false
		for _, field := range nullable {
			if field == name {
				allowed = true
			}
		}
		if !allowed {
			return appError("请求字段不接受 null。", 400, "INVALID_REQUEST")
		}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return appError("JSON 请求格式错误。", 400, "INVALID_JSON")
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return appError("JSON 请求格式错误。", 400, "INVALID_JSON")
	}
	return nil
}

func (a *HTTPApp) compatibilityAPI(w http.ResponseWriter, r *http.Request, path string, parts []string, decode func(int64, any) bool, writeState func(int)) {
	s := a.opts.Store
	d := a.directory
	diag := a.diagnostics
	switch {
	case path == "api/compatibility" && r.Method == "GET":
		st := d.State()
		b, _ := json.Marshal(st)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		m["engine"] = "responses-tools-v3"
		m["engines"] = []string{"responses-tools-v1", "responses-tools-v2", "responses-tools-v3"}
		m["diagnosis"] = diag.State()
		a.json(w, 200, m)
	case path == "api/compatibility/refresh" && r.Method == "POST":
		st, _ := d.Refresh()
		a.json(w, 200, compatJSON(st, diag.State()))
	case path == "api/compatibility/remove" && r.Method == "POST":
		var v struct {
			Ref  string `json:"ref"`
			File string `json:"file"`
		}
		if !decode(512<<10, &v) {
			return
		}
		if e := d.RemoveCandidate(v.Ref, v.File); e != nil {
			a.fail(w, e)
		} else {
			st := d.State()
			a.json(w, 200, compatJSON(st, diag.State()))
		}
	case path == "api/compatibility/template" && r.Method == "GET":
		var b []byte
		var e error
		if a.opts.Assets != nil {
			b, e = fs.ReadFile(a.opts.Assets, "examples/pixel-swe-v2.adroute-adapter.json")
		} else {
			b, e = os.ReadFile(filepath.Join("examples", "pixel-swe-v2.adroute-adapter.json"))
		}
		if e != nil {
			a.fail(w, appError("template unavailable", 500, "TEMPLATE_UNAVAILABLE"))
		} else {
			var v any
			_ = json.Unmarshal(b, &v)
			a.json(w, 200, v)
		}
	case path == "api/compatibility/verify" && r.Method == "POST":
		var p AdapterPackage
		if !decode(512<<10, &p) {
			return
		}
		v, e := VerifyPackage(p)
		if e != nil {
			a.fail(w, appError(e.Error(), 400, "INVALID_ADAPTER"))
		} else {
			a.json(w, 200, v)
		}
	case path == "api/compatibility/packages" && r.Method == "POST":
		var p AdapterPackage
		if !decode(512<<10, &p) {
			return
		}
		if e := s.ImportAdapter(p); e != nil {
			a.fail(w, e)
		} else {
			v, _ := VerifyPackage(p)
			out := compatJSON(d.State(), diag.State())
			out["verification"] = v
			a.json(w, 201, out)
		}
	case len(parts) == 4 && parts[2] == "compatibility" && parts[3] == "packages" && r.Method == "DELETE":
		// Ref deletion is handled by the dedicated ref-shaped route below.
		a.fail(w, appError("adapter reference required", 400, "INVALID_REQUEST"))
	case strings.HasPrefix(path, "api/compatibility/packages/") && r.Method == "DELETE":
		ref := strings.TrimPrefix(path, "api/compatibility/packages/")
		if e := d.Remove(ref); e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, compatJSON(d.State(), diag.State()))
		}
	case strings.HasPrefix(path, "api/compatibility/packages/") && r.Method == "GET":
		ref := strings.TrimPrefix(path, "api/compatibility/packages/")
		var found any
		for _, raw := range s.Snapshot().Adapters {
			var p AdapterPackage
			if json.Unmarshal(raw, &p) == nil && PackageRef(p) == ref {
				found = p
				break
			}
		}
		if found == nil {
			a.fail(w, appError("adapter not found", 404, "ADAPTER_NOT_FOUND"))
		} else {
			a.json(w, 200, found)
		}
	case path == "api/compatibility/diagnosis" && r.Method == "POST":
		var v struct {
			ModelID string `json:"modelId"`
			Raw     bool   `json:"raw"`
			Consent bool   `json:"consent"`
		}
		if !decode(64<<10, &v) {
			return
		}
		p, e := s.ModelRoute(v.ModelID)
		if e == nil {
			var x DiagnosticState
			x, e = diag.Arm(p, p.Model, v.Raw, v.Consent)
			if e == nil {
				a.json(w, 200, x)
				return
			}
		}
		a.fail(w, e)
	case path == "api/compatibility/diagnosis" && r.Method == "GET":
		a.json(w, 200, diag.Detail())
	case path == "api/compatibility/diagnosis" && r.Method == "DELETE":
		if e := diag.Clear(); e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, diag.State())
		}
	case path == "api/compatibility/prompt" && r.Method == "POST":
		var v struct {
			ModelID          string `json:"modelId"`
			IncludeDiagnosis bool   `json:"includeDiagnosis"`
			DiagnosisID      string `json:"diagnosisId"`
		}
		if !decode(64<<10, &v) {
			return
		}
		var res RepairPromptResult
		var e error
		if len(a.opts.PromptTemplate) > 0 {
			res, e = GenerateRepairPromptFromTemplate(a.opts.PromptTemplate, a.opts.PromptRoot, s, d, diag, v.ModelID, v.IncludeDiagnosis, v.DiagnosisID)
		} else {
			res, e = GenerateRepairPrompt(a.opts.PromptRoot, s, d, diag, v.ModelID, v.IncludeDiagnosis, v.DiagnosisID)
		}
		if e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, res)
		}
	case path == "api/compatibility/replay" && r.Method == "POST":
		a.compatibilityReplay(w, r, decode)
	case len(parts) == 4 && parts[2] == "selection" && r.Method == "PUT":
		var v struct {
			Profile string `json:"profile"`
		}
		if !decode(64<<10, &v) {
			return
		}
		if v.Profile == "" {
			a.fail(w, appError("必须提供修复包版本；关闭修复请显式传 none。", 400, "INVALID_REQUEST"))
			return
		}
		if e := s.SetModelProfile(parts[3], v.Profile); e != nil {
			a.fail(w, e)
		} else {
			writeState(200)
		}
	default:
		a.fail(w, appError("管理接口不存在。", 404, "NOT_FOUND"))
	}
}

func (a *HTTPApp) compatibilityReplay(w http.ResponseWriter, r *http.Request, decode func(int64, any) bool) {
	var v struct {
		Ref    string            `json:"ref"`
		Events []json.RawMessage `json:"events"`
	}
	if !decode(512<<10, &v) {
		return
	}
	if len(v.Events) == 0 || len(v.Events) > 256 {
		a.fail(w, appError("回放需要不超过 256KB / 256 个事件的内容样本。", 400, "INVALID_REQUEST"))
		return
	}
	eventBytes := 2 // JSON array brackets
	validated := make([]sseEvent, 0, len(v.Events))
	for i, raw := range v.Events {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil || m == nil {
			a.fail(w, appError("invalid replay event", 400, "INVALID_REQUEST"))
			return
		}
		typ, ok := m["type"].(string)
		if !ok || typ == "" {
			a.fail(w, appError("invalid replay event", 400, "INVALID_REQUEST"))
			return
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			a.fail(w, appError("invalid replay event", 400, "INVALID_REQUEST"))
			return
		}
		eventBytes += compact.Len()
		if i > 0 {
			eventBytes++
		}
		if eventBytes > AdapterPackageLimit {
			a.fail(w, appError("回放需要不超过 256KB / 256 个事件的内容样本。", 400, "INVALID_REQUEST"))
			return
		}
		validated = append(validated, sseEvent{Data: raw, Event: typ})
	}
	var p *AdapterPackage
	for _, raw := range a.opts.Store.Snapshot().Adapters {
		var x AdapterPackage
		if json.Unmarshal(raw, &x) == nil && PackageRef(x) == v.Ref {
			p = &x
		}
	}
	if p == nil {
		a.fail(w, appError("adapter not found", 404, "ADAPTER_NOT_FOUND"))
		return
	}
	out, e := RepairEvents(*p, validated)
	if e != nil {
		// Incomplete/failed terminals are valid replay samples, but never
		// successful verification. Return the sample unchanged as a failed replay.
		terminalStatus := false
		for _, ev := range validated {
			var m map[string]any
			_ = json.Unmarshal(ev.Data, &m)
			if str(m, "type") == "response.completed" && str(nested(m, "response"), "status") == "completed" {
				terminalStatus = true
			}
		}
		if terminalStatus {
			a.fail(w, e)
			return
		}
		out = validated
	}
	arrOut := []any{}
	for _, ev := range out {
		var m any
		_ = json.Unmarshal(ev.Data, &m)
		arrOut = append(arrOut, m)
	}
	completed := false
	for _, ev := range out {
		var m map[string]any
		_ = json.Unmarshal(ev.Data, &m)
		if str(m, "type") == "response.completed" && str(nested(m, "response"), "status") == "completed" {
			completed = true
		}
	}
	a.json(w, 200, map[string]any{"replayPassed": completed, "liveVerified": false, "events": arrOut})
}

func compatJSON(st AdapterDirectoryState, d DiagnosticState) map[string]any {
	b, _ := json.Marshal(st)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	m["engine"] = "responses-tools-v3"
	m["engines"] = []string{"responses-tools-v1", "responses-tools-v2", "responses-tools-v3"}
	m["diagnosis"] = d
	return m
}
