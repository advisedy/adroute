package adroute

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Client access is intentionally a two-item list: the clients this build knows
// how to wire. Each entry maps one switch in the UI to one explicit, reversible
// operation on a client configuration file.
const (
	ClientCodex    = "codex"
	ClientOpenCode = "opencode"
)

type ClientState struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Available  bool   `json:"available"`
	Stale      bool   `json:"stale"`
	ConfigPath string `json:"configPath,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func processEnv() map[string]string {
	out := map[string]string{}
	for _, v := range os.Environ() {
		if i := strings.IndexByte(v, '='); i > 0 {
			out[v[:i]] = v[i+1:]
		}
	}
	return out
}

// ClientPreference records an explicit user choice for a client. It exists so
// `adroute start` does not silently re-enable a client the user switched off.
type ClientPreference struct {
	Codex *bool `json:"codex,omitempty"`
}

func clientPreferencePath(home string) string { return filepath.Join(home, "clients.json") }

// ClientEnabledPreference returns the explicit choice and whether one exists.
func ClientEnabledPreference(home, id string) (bool, bool) {
	if id != ClientCodex {
		return false, false
	}
	pref, err := readPrivateJSON[ClientPreference](clientPreferencePath(home))
	if err != nil || pref.Codex == nil {
		return false, false
	}
	return *pref.Codex, true
}

func setClientPreference(home, id string, enabled bool) error {
	if id != ClientCodex {
		return nil
	}
	path := clientPreferencePath(home)
	pref, err := readPrivateJSON[ClientPreference](path)
	if err != nil && !os.IsNotExist(err) {
		pref = ClientPreference{}
	}
	value := enabled
	pref.Codex = &value
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicCodexJSON(path, pref)
}

func (a *HTTPApp) proxyURL() string { return fmt.Sprintf("http://127.0.0.1:%d/v1", a.opts.Port) }

func (a *HTTPApp) codexOptions() CodexConfigOptions {
	tokens := a.opts.Store.TokenPair()
	paths := a.opts.Store.Paths()
	return CodexConfigOptions{Home: paths.Home, URL: fmt.Sprintf("http://127.0.0.1:%d", a.opts.Port), ProxyToken: tokens.ProxyToken, InstallationID: tokens.InstallationID, Model: DefaultRouteID, CatalogPath: &paths.Catalog, Env: a.opts.Env}
}

func (a *HTTPApp) expectedCodexHome() string {
	if v := a.opts.Env["CODEX_HOME"]; v != "" {
		return v
	}
	home := a.opts.Env["HOME"]
	if home == "" {
		home = a.opts.Env["USERPROFILE"]
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".codex")
}

// codexClient reports whether this installation currently owns the Codex
// connection. It only reads files.
func (a *HTTPApp) codexClient() ClientState {
	c := ClientState{ID: ClientCodex, Name: "Codex", ConfigPath: filepath.Join(a.expectedCodexHome(), "config.toml")}
	record, err := readCodexJournal(filepath.Join(a.opts.Store.Paths().Home, "codex", "connection.json"))
	if err != nil {
		c.Detail = "Codex 接入记录无效，需要先修复或恢复。"
		return c
	}
	if record == nil {
		c.Available = true
		c.Detail = "未接入。打开开关会备份并合并 Codex 配置。"
		return c
	}
	c.Enabled = true
	c.Available = true
	c.ConfigPath = filepath.Join(record.CodexHome, "config.toml")
	raw, err := readSafeText(c.ConfigPath)
	if err != nil {
		c.Detail = "Codex 配置无法读取，请检查文件权限。"
		return c
	}
	if !raw.exists {
		c.Stale = true
		c.Detail = "Codex 配置文件已不存在，刷新会重新写入。"
		return c
	}
	doc, err := parseCodexTOML(raw.text)
	if err != nil {
		c.Stale = true
		c.Detail = "Codex 配置不是有效 TOML，刷新会保留原文件并报错。"
		return c
	}
	c.Stale = a.codexConfigDrifted(doc)
	if c.Stale {
		c.Detail = "Codex 配置与当前地址或模型目录不一致，刷新可重新接入。"
	} else {
		c.Detail = "已接入 adroute。运行 codex 即可选择模型。"
	}
	return c
}

func (a *HTTPApp) codexConfigDrifted(doc *tomlDocument) bool {
	o := a.codexOptions()
	text := func(key string) string {
		v, _ := doc.semantic[key].(string)
		return v
	}
	if text("model_provider") != "adroute" || text("model") != DefaultRouteID {
		return true
	}
	if o.CatalogPath != nil && text("model_catalog_json") != *o.CatalogPath {
		return true
	}
	providers, _ := doc.semantic["model_providers"].(map[string]any)
	provider, _ := providers["adroute"].(map[string]any)
	if provider == nil {
		return true
	}
	if provider["base_url"] != a.proxyURL() || provider["experimental_bearer_token"] != o.ProxyToken || provider["wire_api"] != "responses" {
		return true
	}
	return false
}

// openCodeClient locates the OpenCode config and compares its adroute model map
// with the current catalog. It only reads files.
func (a *HTTPApp) openCodeClient() ClientState {
	c := ClientState{ID: ClientOpenCode, Name: "OpenCode"}
	path, err := locateOpenCode(OpenCodeSyncOptions{Env: a.opts.Env})
	if err != nil {
		c.Detail = openCodeDetail(err)
		return c
	}
	c.ConfigPath = path
	c.Available = true
	file, err := readSyncFile(path)
	if err != nil {
		c.Detail = "OpenCode 配置无法安全读取。"
		return c
	}
	root, err := parseJSONC(file.bytes)
	if err != nil {
		c.Detail = "OpenCode 配置不是有效 JSON/JSONC。"
		return c
	}
	providers := root.props["providers"]
	var provider *jsoncNode
	if providers != nil && providers.props != nil {
		provider = providers.props["adroute"]
	}
	if provider == nil || provider.props == nil {
		c.Detail = "尚未配置 providers.adroute，请先按文档添加 Responses provider。"
		return c
	}
	models := provider.props["models"]
	if models == nil || models.props == nil {
		c.Detail = "未接入。打开开关会写入当前开放的模型。"
		return c
	}
	c.Enabled = len(models.props) > 0
	want, _, err := modelSnapshot(a.catalogModels())
	if err != nil {
		c.Detail = "模型目录暂不可用。"
		return c
	}
	c.Stale = c.Enabled && !sameJSON(models.value, want)
	if c.Stale {
		c.Detail = "已接入，但模型列表与当前目录不一致，刷新可更新。"
	} else if c.Enabled {
		c.Detail = "已接入 adroute。请在 OpenCode 中刷新模型目录。"
	} else {
		c.Detail = "已关闭。模型列表为空，provider 配置保留。"
	}
	return c
}

func sameJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func openCodeDetail(err error) string {
	var e *AppError
	if ok := asAppError(err, &e); ok {
		switch e.Code {
		case "OPENCODE_CONFIG_MISSING":
			return "未找到 OpenCode 配置（~/.config/opencode/opencode.json 或 .jsonc）。"
		case "OPENCODE_CONFIG_AMBIGUOUS":
			return "同时存在 opencode.json 和 opencode.jsonc，但两份都没有 providers.adroute；请先在其中一个里配置 provider。"
		case "OPENCODE_CONFIG_PATH":
			return "OpenCode 配置路径被环境变量覆盖，无法自动定位。"
		case "OPENCODE_PROVIDER_INCOMPATIBLE":
			return "providers.adroute 的 package、settings.baseURL 或凭据不兼容 Responses 协议。adroute 只支持 Responses，package 可用 @opencode/ai/providers/openai、@opencode/ai/providers/openai/responses 或 aisdk:@ai-sdk/openai。"
		default:
			return e.Message
		}
	}
	return "无法定位 OpenCode 配置。"
}

func asAppError(err error, target **AppError) bool { return errors.As(err, target) }

func (a *HTTPApp) catalogModels() []SyncModel {
	models := []SyncModel{}
	catalog := a.opts.Store.Catalog()
	if entries, ok := catalog["models"].([]map[string]any); ok {
		for _, entry := range entries {
			slug, _ := entry["slug"].(string)
			name, _ := entry["display_name"].(string)
			window, _ := entry["context_window"].(int)
			models = append(models, SyncModel{Slug: slug, DisplayName: name, ContextWindow: window})
		}
	}
	return models
}

func (a *HTTPApp) clients() []ClientState {
	return []ClientState{a.codexClient(), a.openCodeClient()}
}

func (a *HTTPApp) setClient(id string, enabled bool) error {
	switch id {
	case ClientCodex:
		home := a.opts.Store.Paths().Home
		var err error
		if enabled {
			_, err = ConnectCodex(a.codexOptions())
		} else {
			_, err = RestoreCodex(home)
		}
		if err != nil {
			return err
		}
		return setClientPreference(home, id, enabled)
	case ClientOpenCode:
		models := []SyncModel{}
		if enabled {
			models = a.catalogModels()
		}
		_, err := SyncOpenCode(OpenCodeSyncOptions{Models: models, Env: a.opts.Env})
		return err
	}
	return appError("未知客户端。", 404, "CLIENT_NOT_FOUND")
}

func (a *HTTPApp) refreshClient(id string) error {
	switch id {
	case ClientCodex:
		if !a.codexClient().Enabled {
			return appError("Codex 尚未接入，请先打开开关。", 409, "CLIENT_NOT_ENABLED")
		}
		_, err := ConnectCodex(a.codexOptions())
		return err
	case ClientOpenCode:
		// Refresh must not be a back door around the switch: enabling writes the
		// model map, so a refresh on a disabled client is refused like Codex.
		if !a.openCodeClient().Enabled {
			return appError("OpenCode 尚未接入，请先打开开关。", 409, "CLIENT_NOT_ENABLED")
		}
		_, err := SyncOpenCode(OpenCodeSyncOptions{Models: a.catalogModels(), Env: a.opts.Env})
		return err
	}
	return appError("未知客户端。", 404, "CLIENT_NOT_FOUND")
}

func (a *HTTPApp) clientsJSON() map[string]any {
	return map[string]any{"clients": a.clients(), "proxyUrl": a.proxyURL()}
}

// clientsAPI handles the explicit client switchboard. All operations are
// admin-only and serialized so two clicks cannot interleave file edits.
func (a *HTTPApp) clientsAPI(w http.ResponseWriter, r *http.Request, path string, parts []string) bool {
	switch {
	case path == "api/clients" && r.Method == http.MethodGet:
		a.json(w, 200, a.clientsJSON())
		return true
	case path == "api/clients/refresh" && r.Method == http.MethodPost:
		a.clientMu.Lock()
		defer a.clientMu.Unlock()
		for _, id := range []string{ClientCodex, ClientOpenCode} {
			c := a.clientByID(id)
			if !c.Enabled || !c.Available {
				continue
			}
			if err := a.refreshClient(id); err != nil {
				a.fail(w, err)
				return true
			}
		}
		a.json(w, 200, a.clientsJSON())
		return true
	case len(parts) == 4 && parts[0] == "api" && parts[1] == "clients" && r.Method == http.MethodPost:
		id, action := parts[2], parts[3]
		var v struct {
			Enabled *bool `json:"enabled"`
		}
		if action == "enabled" {
			body, err := decodeBody(r, 64<<10)
			if err != nil {
				a.fail(w, err)
				return true
			}
			if err := strictManagementJSON(body, &v); err != nil {
				a.fail(w, err)
				return true
			}
			if v.Enabled == nil {
				a.fail(w, appError("enabled 必须是布尔值。", 400, "INVALID_REQUEST"))
				return true
			}
		}
		if action != "enabled" && action != "refresh" {
			return false
		}
		a.clientMu.Lock()
		defer a.clientMu.Unlock()
		var err error
		if action == "enabled" {
			err = a.setClient(id, *v.Enabled)
		} else {
			err = a.refreshClient(id)
		}
		if err != nil {
			a.fail(w, err)
			return true
		}
		a.json(w, 200, a.clientsJSON())
		return true
	}
	return false
}

func (a *HTTPApp) clientByID(id string) ClientState {
	for _, c := range a.clients() {
		if c.ID == id {
			return c
		}
	}
	return ClientState{ID: id}
}
