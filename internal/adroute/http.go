package adroute

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"github.com/andybalholm/brotli"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const ServiceVersion = "0.7.1"

type HTTPOptions struct {
	Store                      *Store
	Port                       int
	InstanceID                 string
	Client                     *http.Client
	HeaderTimeout, IdleTimeout time.Duration
	Assets                     fs.FS
	PromptRoot                 string
	PromptTemplate             []byte
	Shutdown                   func()
	Log                        func(map[string]any)
}
type HTTPApp struct {
	opts        HTTPOptions
	diagnostics *Diagnostics
	directory   *AdapterDirectory
	requestMu   sync.Mutex
	requests    []map[string]any
}

func NewHTTPApp(o HTTPOptions) (*HTTPApp, error) {
	if o.Port == 0 {
		o.Port = 3001
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	}
	clientCopy := *o.Client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	o.Client = &clientCopy
	if o.HeaderTimeout == 0 {
		o.HeaderTimeout = 30 * time.Second
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = 5 * time.Minute
	}
	if o.Store != nil && o.PromptRoot == "" {
		o.PromptRoot = "."
	}
	a := &HTTPApp{opts: o, requests: []map[string]any{}}
	if o.Store != nil {
		a.diagnostics = NewDiagnostics(o.Store.Paths().Home, DiagnosticLimits{})
		a.directory = NewAdapterDirectory(o.Store)
		if err := o.Store.EnableCatalog(); err != nil {
			return nil, fmt.Errorf("initialize model catalog: %w", err)
		}
	}
	return a, nil
}
func (a *HTTPApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for k, v := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY", "Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"} {
		w.Header().Set(k, v)
	}
	if !a.validHost(r) {
		a.fail(w, appError("仅支持本机地址访问。", 403, "HOST_FORBIDDEN"))
		return
	}
	if o := r.Header.Get("Origin"); o != "" && !a.validOrigin(o) {
		a.fail(w, appError("不允许其他网站访问本地管理或代理接口。", 403, "ORIGIN_FORBIDDEN"))
		return
	}
	if r.URL.Path == "/internal/shutdown" && r.Method == "POST" {
		if !a.authorized(r, true) {
			a.fail(w, appError("访问令牌无效。", 401, "UNAUTHORIZED"))
			return
		}
		if a.opts.InstanceID == "" || r.Header.Get("X-Adroute-Instance") != a.opts.InstanceID {
			a.fail(w, appError("服务实例不匹配，已拒绝停止。", 409, "INSTANCE_MISMATCH"))
			return
		}
		a.json(w, 202, map[string]bool{"stopping": true})
		// Force the acknowledgment onto the connection before initiating the
		// server drain; Shutdown then waits for this handler to return.
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if a.opts.Shutdown != nil {
			go a.opts.Shutdown()
		}
		return
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		if !a.authorized(r, true) {
			a.fail(w, appError("访问令牌无效。", 401, "UNAUTHORIZED"))
			return
		}
		a.api(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		if !a.authorized(r, false) {
			a.fail(w, appError("访问令牌无效。", 401, "UNAUTHORIZED"))
			return
		}
		if r.URL.Path == "/v1/models" && r.Method == "GET" {
			a.json(w, 200, a.opts.Store.OpenAIModelList())
			return
		}
		a.proxyRoute(w, r)
		return
	}
	if a.opts.Assets != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name == "app.js" {
			name = "controller.js"
		}
		if f, e := fs.ReadFile(a.opts.Assets, "public/"+name); e == nil {
			ct := "text/html; charset=utf-8"
			if strings.HasSuffix(name, ".js") {
				ct = "text/javascript; charset=utf-8"
			}
			if strings.HasSuffix(name, ".css") {
				ct = "text/css; charset=utf-8"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(200)
			if r.Method != http.MethodHead {
				_, _ = w.Write(f)
			}
			return
		}
	}
	if r.URL.Path == "/health" {
		if !a.authorized(r, true) {
			a.fail(w, appError("访问令牌无效。", 401, "UNAUTHORIZED"))
			return
		}
		a.json(w, 200, map[string]any{"service": "adroute", "version": ServiceVersion, "pid": os.Getpid(), "instanceId": a.opts.InstanceID, "installationId": a.opts.Store.TokenPair().InstallationID, "port": a.opts.Port, "url": fmt.Sprintf("http://127.0.0.1:%d", a.opts.Port)})
		return
	}
	if r.URL.Path == "/v1/models" {
		a.json(w, 200, a.opts.Store.OpenAIModelList())
		return
	}
	if r.URL.Path == "/v1/responses" {
		a.fail(w, appError("proxy route unavailable", 503, "UPSTREAM_UNAVAILABLE"))
		return
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		a.fail(w, appError("管理接口不存在。", 404, "NOT_FOUND"))
		return
	}
	a.fail(w, appError("页面不存在。", 404, "NOT_FOUND"))
}
func (a *HTTPApp) validHost(r *http.Request) bool {
	h := r.Host
	return h == fmt.Sprintf("127.0.0.1:%d", a.opts.Port) || h == fmt.Sprintf("localhost:%d", a.opts.Port) || h == fmt.Sprintf("[::1]:%d", a.opts.Port)
}
func (a *HTTPApp) validOrigin(s string) bool {
	return s == fmt.Sprintf("http://127.0.0.1:%d", a.opts.Port) || s == fmt.Sprintf("http://localhost:%d", a.opts.Port) || s == fmt.Sprintf("http://[::1]:%d", a.opts.Port)
}
func (a *HTTPApp) authorized(r *http.Request, admin bool) bool {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return false
	}
	v = strings.TrimPrefix(v, "Bearer ")
	if admin {
		return a.opts.Store.AuthorizeAdmin(v)
	}
	return a.opts.Store.AuthorizeProxy(v)
}
func (a *HTTPApp) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (a *HTTPApp) fail(w http.ResponseWriter, e error) {
	a.json(w, ErrorStatus(e), ErrorPayload(e, a.opts.Store.Secrets()))
}
func decodeBody(r *http.Request, limit int64) ([]byte, error) {
	var rd io.Reader = r.Body
	switch strings.ToLower(r.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		g, e := gzip.NewReader(rd)
		if e != nil {
			return nil, appError("JSON 请求格式错误。", 400, "INVALID_JSON")
		}
		defer g.Close()
		rd = g
	case "deflate":
		br := bufio.NewReader(rd)
		head, _ := br.Peek(2)
		if len(head) == 2 && head[0]&0x0f == 8 && ((int(head[0])<<8)|int(head[1]))%31 == 0 {
			z, err := zlib.NewReader(br)
			if err != nil {
				return nil, appError("JSON 请求格式错误。", 400, "INVALID_JSON")
			}
			defer z.Close()
			rd = z
		} else {
			rd = flate.NewReader(br)
		}
	case "br":
		rd = brotli.NewReader(rd)
	default:
		return nil, appError("暂不支持此请求压缩格式。", 415, "UNSUPPORTED_ENCODING")
	}
	b, e := io.ReadAll(io.LimitReader(rd, limit+1))
	if e != nil {
		return nil, appError("JSON 请求格式错误。", 400, "INVALID_JSON")
	}
	if int64(len(b)) > limit {
		return nil, appError("请求内容过大。", 413, "BODY_TOO_LARGE")
	}
	return b, nil
}
func (a *HTTPApp) proxyRoute(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, r)
}

func (a *HTTPApp) api(w http.ResponseWriter, r *http.Request) {
	if a.managementAPI(w, r) {
		return
	}
	a.fail(w, appError("管理接口不存在。", 404, "NOT_FOUND"))
}
