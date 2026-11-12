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
			if !m.Disabled {
				open++
			}
		}
		groups = append(groups, map[string]any{"id": g.ID, "name": g.Name, "modelCount": total, "openCount": open})
	}
	models := []map[string]any{}
	for _, m := range st.Models {
		r := routeFor(st, m)
		models = append(models, map[string]any{"id": m.ID, "groupId": m.GroupID, "groupName": r.GroupName, "name": m.Name, "model": m.Model, "baseUrl": m.BaseURL, "apiKey": m.APIKey, "website": m.Website, "disabled": m.Disabled, "open": !m.Disabled, "profile": m.Profile, "routeId": r.RouteID})
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
		aggregates = append(aggregates, map[string]any{"id": agg.ID, "name": agg.Name, "routeId": AggregateRouteID(agg.Name), "members": members, "memberCount": len(agg.Members), "openMemberCount": open, "maxRounds": agg.MaxRounds, "disabled": agg.Disabled, "open": !agg.Disabled})
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
			"modelCopy":          true,
			"configTransfer":     true,
			"quickTest":          true,
			"quickTestTimeoutMs": QuickTestTimeout.Milliseconds(),
			"compatibility":      true,
			"adapterDirectory":   true,
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

func (a *HTTPApp) managementAPI(w http.ResponseWriter, r *http.Request) bool {
	path := strings.Trim(r.URL.Path, "/")
	s := a.opts.Store
	parts := strings.Split(path, "/")
	writeState := func(status int) { a.json(w, status, a.stateJSON()) }
	decode := func(limit int64, v any) bool {
		b, e := decodeBody(r, limit)
		if e == nil {
			if path == "api/default" {
				e = strictManagementJSON(b, v, "modelId")
			} else {
				e = strictManagementJSON(b, v)
			}
		}
		if e != nil {
			a.fail(w, e)
			return false
		}
		return true
	}
	if path == "api/state" && r.Method == "GET" {
		a.json(w, 200, a.stateJSON())
		return true
	}
	if path == "api/config/export" && r.Method == http.MethodGet {
		a.json(w, http.StatusOK, configBackup{Format: "adroute-config", Version: 1, Config: s.Snapshot()})
		return true
	}
	if path == "api/config/import" && r.Method == http.MethodPost {
		body, err := decodeBody(r, 128<<20)
		var input configImport
		if err == nil && decodeStrict(body, &input) != nil {
			err = appError("配置文件格式错误，现有配置未改变。", 400, "INVALID_BACKUP")
		}
		if err == nil {
			err = s.importConfig(input)
		}
		if err != nil {
			a.fail(w, err)
		} else {
			writeState(http.StatusOK)
		}
		return true
	}
	if path == "api/catalog/retry" && r.Method == http.MethodPost {
		body, e := decodeBody(r, 64<<10)
		if e != nil {
			a.fail(w, e)
		} else if len(bytes.TrimSpace(body)) != 0 {
			a.fail(w, appError("目录重试请求不接受参数。", 400, "INVALID_REQUEST"))
		} else if e := s.RetryCatalogPublication(); e != nil {
			a.fail(w, appError("配置仍已保存，但客户端目录发布失败。请检查目录文件及权限后重试。", 503, "CATALOG_PUBLICATION_FAILED"))
		} else {
			writeState(200)
		}
		return true
	}
	if path == "api/setup" && r.Method == "GET" {
		tok := s.TokenPair().ProxyToken
		base := fmt.Sprintf("http://127.0.0.1:%d/v1", a.opts.Port)
		config := fmt.Sprintf("# adroute start 自动合并到 Codex 配置；此处为配置预览\nmodel_provider = \"adroute\"\nmodel = %q\nmodel_catalog_json = %q\n\n[model_providers.adroute]\nname = \"adroute\"\nbase_url = %q\nwire_api = \"responses\"\nexperimental_bearer_token = %q\nrequires_openai_auth = false\nsupports_websockets = false\n", DefaultRouteID, s.Paths().Catalog, base, tok)
		a.json(w, 200, map[string]any{"proxyUrl": base, "proxyToken": tok, "codexConfig": config, "shellSetup": "adroute start\ncodex"})
		return true
	}
	if path == "api/groups" && r.Method == "POST" {
		var v GroupInput
		if !decode(64<<10, &v) {
			return true
		}
		if _, e := s.CreateGroup(v); e != nil {
			a.fail(w, e)
		} else {
			writeState(201)
		}
		return true
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "groups" {
		id := parts[2]
		switch r.Method {
		case http.MethodPut:
			var v GroupInput
			if !decode(64<<10, &v) {
				return true
			}
			if e := s.RenameGroup(id, v); e != nil {
				a.fail(w, e)
			} else {
				writeState(200)
			}
			return true
		case http.MethodDelete:
			if e := s.DeleteGroup(id); e != nil {
				a.fail(w, e)
			} else {
				writeState(200)
			}
			return true
		}
	}
	if path == "api/aggregates" && r.Method == "POST" {
		var v AggregateInput
		if !decode(64<<10, &v) {
			return true
		}
		if _, e := s.CreateAggregate(v); e != nil {
			a.fail(w, e)
		} else {
			writeState(201)
		}
		return true
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "aggregates" {
		id := parts[2]
		switch r.Method {
		case http.MethodPut:
			var v AggregateInput
			if !decode(64<<10, &v) {
				return true
			}
			if e := s.UpdateAggregate(id, v); e != nil {
				a.fail(w, e)
			} else {
				writeState(200)
			}
			return true
		case http.MethodDelete:
			if e := s.DeleteAggregate(id); e != nil {
				a.fail(w, e)
			} else {
				writeState(200)
			}
			return true
		}
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "aggregates" && parts[3] == "availability" && r.Method == http.MethodPut {
		var v struct {
			Enabled *bool `json:"enabled"`
		}
		if !decode(64<<10, &v) {
			return true
		}
		if v.Enabled == nil {
			a.fail(w, appError("enabled must be a boolean", 400, "INVALID_REQUEST"))
			return true
		}
		if e := s.SetAggregateEnabled(parts[2], *v.Enabled); e != nil {
			a.fail(w, e)
		} else {
			writeState(200)
		}
		return true
	}
	if path == "api/models/discover" && r.Method == "POST" {
		var v DiscoverInput
		if !decode(64<<10, &v) {
			return true
		}
		result, e := a.upstreams().Discover(r.Context(), v.BaseURL, v.APIKey, 30*time.Second)
		if e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, result)
		}
		return true
	}
	if path == "api/models" && r.Method == "POST" {
		var v ModelInput
		if !decode(64<<10, &v) {
			return true
		}
		if _, e := s.CreateModel(v); e != nil {
			a.fail(w, e)
		} else {
			writeState(201)
		}
		return true
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "models" && parts[3] == "copy" && r.Method == http.MethodPost {
		body, e := decodeBody(r, 64<<10)
		if e != nil {
			a.fail(w, e)
		} else if len(bytes.TrimSpace(body)) != 0 {
			a.fail(w, appError("复制请求不接受参数。", 400, "INVALID_REQUEST"))
		} else if _, e := s.CopyModel(parts[2]); e != nil {
			a.fail(w, e)
		} else {
			writeState(201)
		}
		return true
	}
	// Discovery for an existing model may reuse its saved credential while the
	// form still allows a new (unsaved) base URL or credential to win.
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "models" && parts[3] == "discover" && r.Method == http.MethodPost {
		var v struct {
			BaseURL string `json:"baseUrl,omitempty"`
			APIKey  string `json:"apiKey,omitempty"`
		}
		if !decode(64<<10, &v) {
			return true
		}
		saved, e := s.ModelRoute(parts[2])
		if e != nil {
			a.fail(w, e)
			return true
		}
		base := strings.TrimSpace(v.BaseURL)
		if base == "" {
			base = saved.BaseURL
		}
		key := strings.TrimSpace(v.APIKey)
		if key == "" {
			key = saved.APIKey
		}
		result, e := a.upstreams().Discover(r.Context(), base, key, 30*time.Second)
		if e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, result)
		}
		return true
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "models" {
		id := parts[2]
		switch r.Method {
		case http.MethodPut:
			var v ModelInput
			if !decode(64<<10, &v) {
				return true
			}
			if e := s.UpdateModel(id, v); e != nil {
				a.fail(w, e)
			} else {
				writeState(200)
			}
			return true
		case http.MethodDelete:
			if e := s.DeleteModel(id); e != nil {
				a.fail(w, e)
			} else {
				writeState(200)
			}
			return true
		}
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "models" && parts[3] == "availability" && r.Method == http.MethodPut {
		var v struct {
			Enabled *bool `json:"enabled"`
		}
		if !decode(64<<10, &v) {
			return true
		}
		if v.Enabled == nil {
			a.fail(w, appError("enabled must be a boolean", 400, "INVALID_REQUEST"))
			return true
		}
		if e := s.SetModelEnabled(parts[2], *v.Enabled); e != nil {
			a.fail(w, e)
		} else {
			writeState(200)
		}
		return true
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "models" && parts[3] == "quick-test" && r.Method == http.MethodPost {
		if body, e := decodeBody(r, 64<<10); e != nil {
			a.fail(w, e)
			return true
		} else if len(bytes.TrimSpace(body)) > 0 {
			a.fail(w, appError("测速请求不接受参数。", 400, "INVALID_REQUEST"))
			return true
		}
		route, e := s.ModelRoute(parts[2])
		if e != nil {
			a.fail(w, e)
			return true
		}
		result, e := a.upstreams().QuickTest(r.Context(), route)
		if e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, result)
		}
		return true
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "models" && parts[3] == "tool-structure-test" && r.Method == http.MethodPost {
		if body, e := decodeBody(r, 64<<10); e != nil {
			a.fail(w, e)
			return true
		} else if len(bytes.TrimSpace(body)) > 0 {
			a.fail(w, appError("工具结构测试不接受参数。", 400, "INVALID_REQUEST"))
			return true
		}
		route, e := s.ModelRoute(parts[2])
		if e != nil {
			a.fail(w, e)
			return true
		}
		result, e := a.upstreams().ToolStructureTest(r.Context(), route)
		if e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, result)
		}
		return true
	}
	if path == "api/default" && r.Method == "POST" {
		var v struct {
			ModelID json.RawMessage `json:"modelId"`
		}
		if !decode(64<<10, &v) {
			return true
		}
		id := ""
		if len(v.ModelID) == 0 {
			a.fail(w, appError("必须提供 modelId；清空默认请显式传 null 或空字符串。", 400, "INVALID_REQUEST"))
			return true
		}
		if !bytes.Equal(bytes.TrimSpace(v.ModelID), []byte("null")) {
			if e := json.Unmarshal(v.ModelID, &id); e != nil {
				a.fail(w, appError("modelId 必须是字符串或 null。", 400, "INVALID_REQUEST"))
				return true
			}
		}
		if e := s.SetDefault(id); e != nil {
			a.fail(w, e)
		} else {
			writeState(200)
		}
		return true
	}
	if path == "api/opencode/sync" && r.Method == "POST" {
		var v struct {
			ConfigPath string `json:"configPath,omitempty"`
		}
		if !decode(64<<10, &v) {
			return true
		}
		models := []SyncModel{}
		catalog := s.Catalog()
		if entries, ok := catalog["models"].([]map[string]any); ok {
			for _, entry := range entries {
				slug, _ := entry["slug"].(string)
				name, _ := entry["display_name"].(string)
				models = append(models, SyncModel{Slug: slug, DisplayName: name})
			}
		}
		res, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, ConfigPath: v.ConfigPath})
		if e != nil {
			a.fail(w, e)
		} else {
			a.json(w, 200, res)
		}
		return true
	}
	if strings.HasPrefix(path, "api/compatibility") {
		a.compatibilityAPI(w, r, path, parts, decode, writeState)
		return true
	}
	return false
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
