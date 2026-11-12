package main

import (
	root "adroute"
	a "adroute/internal/adroute"
	"adroute/internal/platform"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const version = a.ServiceVersion

var requestedExitCode int

type serviceState struct {
	PID, Port                           int
	InstanceID, InstallationID, Version string
	URL                                 string
}
type statusResult struct {
	Running        bool   `json:"running"`
	Version        string `json:"version,omitempty"`
	PID            int    `json:"pid,omitempty"`
	Port           int    `json:"port,omitempty"`
	URL            string `json:"url,omitempty"`
	Uncertain      bool   `json:"uncertain,omitempty"`
	Reason         string `json:"reason,omitempty"`
	InstallationID string `json:"installationId,omitempty"`
	InstanceID     string `json:"instanceId,omitempty"`
}
type codexResult struct {
	Configured       bool   `json:"configured"`
	Restored         bool   `json:"restored,omitempty"`
	AlreadyRestored  bool   `json:"alreadyRestored,omitempty"`
	PreservedChanges bool   `json:"preservedChanges,omitempty"`
	ConfigPath       string `json:"configPath,omitempty"`
	Message          string `json:"message,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, safeError(err))
		os.Exit(1)
	}
	if requestedExitCode != 0 {
		os.Exit(requestedExitCode)
	}
}
func safeError(err error) string {
	var x interface{ Error() string }
	if errors.As(err, &x) {
		return x.Error()
	}
	return "操作失败，请检查运行环境、文件权限或服务日志。"
}
func homePath() string {
	h, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	return homePathFor(os.Getenv("ADROUTE_HOME"), os.Getenv("XDG_CONFIG_HOME"), h, cwd)
}
func homePathFor(adrouteHome, xdgConfigHome, userHome, workingDirectory string) string {
	home := adrouteHome
	if home == "" {
		if xdgConfigHome != "" {
			home = filepath.Join(xdgConfigHome, "adroute")
		} else {
			home = filepath.Join(userHome, ".config", "adroute")
		}
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(workingDirectory, home)
	}
	return filepath.Clean(home)
}
func cliHelp() string {
	return fmt.Sprintf("adroute %s — Codex 本地代理与 Web 管理\n\n用法：\n  adroute start [--port N] [--foreground]\n  adroute restart [--port N] [--foreground]\n  adroute status [--json]\n  adroute stop\n  adroute codex restore\n  adroute --version\n\nrestart 会按当前端口停止并用本可执行文件重新启动，模型配置与令牌不变。\n默认仅监听 127.0.0.1。数据目录依次使用 ADROUTE_HOME、XDG_CONFIG_HOME/adroute 或 HOME/.config/adroute；CODEX_HOME 指定 Codex 目录。\n", version)
}
func run(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Print(cliHelp())
		return nil
	}
	if args[0] == "--version" || args[0] == "-v" {
		fmt.Println(version)
		return nil
	}
	switch args[0] {
	case "internal":
		if len(args) > 1 && args[1] == "serve" {
			return serve()
		}
		return fmt.Errorf("未知内部命令")
	case "start", "restart":
		name := args[0]
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		port := fs.String("port", "", "port")
		foreground := fs.Bool("foreground", false, "foreground")
		if e := fs.Parse(args[1:]); e != nil || fs.NArg() != 0 {
			return fmt.Errorf("无法识别 %s 参数", name)
		}
		if *port != "" {
			if _, e := parsePort(*port); e != nil {
				return e
			}
		}
		var foregroundSignals chan os.Signal
		if *foreground {
			foregroundSignals = make(chan os.Signal, 1)
			signal.Notify(foregroundSignals, platform.Signals()...)
			defer signal.Stop(foregroundSignals)
		}
		if name == "restart" {
			return runRestart(*port, *foreground, foregroundSignals)
		}
		return runStart(*port, *foreground, foregroundSignals)
	case "status":
		fs := flag.NewFlagSet("status", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		j := fs.Bool("json", false, "json")
		if e := fs.Parse(args[1:]); e != nil || fs.NArg() != 0 {
			return fmt.Errorf("无法识别 status 参数")
		}
		s := getStatus()
		if *j {
			_ = json.NewEncoder(os.Stdout).Encode(s)
		} else if s.Running {
			fmt.Printf("adroute 正在运行\n管理页面：%s\n代理地址：%s/v1\n进程：%d\n版本：%s\n需要管理登录链接时，再运行 adroute start。\n", s.URL, s.URL, s.PID, s.Version)
		} else if s.Uncertain {
			fmt.Println("adroute 状态无法确认：" + s.Reason)
		} else {
			fmt.Println("adroute 未运行。")
		}
		if !s.Running {
			if s.Uncertain {
				return errors.New(s.Reason)
			}
			requestedExitCode = 3
		}
		return nil
	case "stop":
		if len(args) != 1 {
			return fmt.Errorf("无法识别 stop 参数")
		}
		stopped, e := stop()
		if e != nil {
			return e
		}
		if stopped {
			fmt.Println("adroute 已停止。模型配置已保留。")
		} else {
			fmt.Println("adroute 已处于停止状态。")
		}
		c, e := a.RestoreCodex(homePath())
		if e != nil {
			fmt.Fprintf(os.Stderr, "Codex 配置恢复失败：%s 可运行 adroute codex restore 重试。\n", safeError(e))
			return errors.New("Codex restore failed")
		}
		reportRestore(c)
		return nil
	case "codex":
		if len(args) != 2 || args[1] != "restore" {
			return fmt.Errorf("请使用 adroute codex restore 恢复 Codex 配置")
		}
		c, e := a.RestoreCodex(homePath())
		if e != nil {
			return e
		}
		reportRestore(c)
		return nil
	default:
		return fmt.Errorf("未知命令，请使用 adroute --help。")
	}
}

// runStart starts (or reuses) the daemon, connects Codex and reports the same
// outcome for `start` and for the start half of `restart`.
func runStart(portArg string, foreground bool, foregroundSignals chan os.Signal) error {
	res, e := start(portArg, foreground)
	if e != nil {
		return e
	}
	startCodex(&res)
	printStarted(res)
	if !res.Codex.Configured {
		fmt.Fprintf(os.Stderr, "Codex 自动接入失败：%s\nadroute 服务仍在运行；修复配置问题后重新执行 adroute start。\n", res.Codex.Message)
	}
	if foreground && res.Service != nil {
		if e := waitForeground(res, foregroundSignals); e != nil {
			return e
		}
		if !res.Codex.Configured {
			return errors.New("Codex 自动接入失败")
		}
		return nil
	}
	if !res.Codex.Configured {
		return errors.New("Codex 自动接入失败")
	}
	fmt.Println("服务在后台运行，可以关闭当前终端。管理链接和 API Key 含本地访问凭据，请勿分享。")
	return nil
}

// runRestart stops the running instance, restores the Codex baseline it managed
// and starts the binary that is running this command. The model configuration,
// tokens and default port stay untouched.
func runRestart(portArg string, foreground bool, foregroundSignals chan os.Signal) error {
	target := portArg
	if target == "" {
		// Reuse the port the service was already serving on, so restart does not
		// silently move the proxy or break configured client base URLs.
		if cur := getStatus(); cur.Running && cur.Port > 0 {
			target = strconv.Itoa(cur.Port)
		}
	}
	stopped, e := stop()
	if e != nil {
		return e
	}
	if !stopped {
		fmt.Println("adroute 未运行，直接启动。")
		return runStart(target, foreground, foregroundSignals)
	}
	fmt.Println("adroute 已停止，正在用当前可执行文件重新启动…")
	// A broken Codex record must not leave the service down: starting again
	// re-plans the config, so report the restore problem and continue.
	if c, ce := a.RestoreCodex(homePath()); ce != nil {
		fmt.Fprintf(os.Stderr, "Codex 配置恢复失败：%s 继续启动；可运行 adroute codex restore 重试。\n", safeError(ce))
	} else {
		reportRestore(c)
	}
	return runStart(target, foreground, foregroundSignals)
}

func reportRestore(v map[string]any) {
	if v["alreadyRestored"] == true {
		fmt.Println("Codex 无需恢复。")
		return
	}
	if v["restored"] == true {
		if v["preservedChanges"] == true {
			fmt.Println("Codex 原配置已恢复，已保留你后来修改的字段")
		} else {
			fmt.Println("Codex 原配置已恢复")
		}
		return
	}
	fmt.Fprintln(os.Stderr, "Codex 配置恢复失败；可运行 adroute codex restore 重试。")
}
func parsePort(raw string) (int, error) {
	n, e := strconv.Atoi(raw)
	if e != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("端口必须是 0 到 65535 的整数；0 表示自动选择空闲端口")
	}
	return n, nil
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func paths() a.Paths { return a.PathsFor(homePath()) }
func withControl(fn func() error) error {
	p := paths()
	if e := os.MkdirAll(p.Run, 0700); e != nil {
		return e
	}
	if e := platform.Chmod(p.Run, 0700); e != nil {
		return e
	}
	f, e := platform.Lock(filepath.Join(p.Run, "control.guard"))
	if e != nil {
		return e
	}
	defer f.Close()
	return fn()
}
func readState() (serviceState, error) {
	var s serviceState
	path := paths().State
	info, e := os.Lstat(path)
	if e != nil {
		return s, e
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !platform.PrivateFile(path, info) {
		return s, fmt.Errorf("unsafe state file")
	}
	if sys, err := platform.Lstat(path); err != nil || sys.Nlink != 1 {
		return s, fmt.Errorf("unsafe state file")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func writePrivate(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	id, e := randomID()
	if e != nil {
		return e
	}
	tmp := path + "." + id + ".tmp"
	f, e := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if e = platform.ChmodFile(f, 0600); e != nil {
		f.Close()
		os.Remove(tmp)
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(tmp)
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		_ = os.Remove(tmp)
		return e
	}
	return platform.SyncDir(path)
}
func localHealth(s serviceState, tokens a.Tokens) (statusResult, bool) {
	var out statusResult
	client := http.Client{Timeout: 700 * time.Millisecond}
	req, e := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/health", s.Port), nil)
	if e != nil {
		return out, false
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AdminToken)
	r, e := client.Do(req)
	if e != nil {
		return out, false
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return out, false
	}
	var h struct {
		Service, Version                string
		PID, Port                       int
		InstanceID, InstallationID, URL string
	}
	if json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&h) != nil || h.Service != "adroute" || h.PID != s.PID || h.Port != s.Port || h.InstanceID != s.InstanceID || h.InstallationID != tokens.InstallationID {
		return out, false
	}
	out = statusResult{Running: true, Version: h.Version, PID: h.PID, Port: h.Port, URL: h.URL, InstallationID: h.InstallationID, InstanceID: h.InstanceID}
	return out, true
}
func getStatus() statusResult {
	s, e := readState()
	if os.IsNotExist(e) {
		return statusResult{}
	}
	if e != nil || s.PID <= 0 || s.Port < 1 || s.Port > 65535 || s.InstanceID == "" {
		return statusResult{Uncertain: true, Reason: "运行状态文件无效或不可读取，拒绝猜测服务进程。"}
	}
	tokens, e := a.LoadTokens(homePath())
	if e != nil {
		return statusResult{Uncertain: true, Reason: "无法读取本地访问令牌，拒绝猜测服务进程。"}
	}
	if result, ok := localHealth(s, tokens); ok {
		return result
	}
	if platform.ProcessAlive(s.PID) {
		return statusResult{Uncertain: true, Reason: "记录中的进程仍存在，但服务身份或健康状态无法确认；为避免误操作，未发送停止信号。"}
	}
	return statusResult{}
}
func start(portArg string, foreground bool) (started, error) {
	var out started
	e := withControl(func() error {
		cur := getStatus()
		if cur.Running {
			store, e := a.OpenStore(homePath())
			if e != nil {
				return e
			}
			if portArg != "" {
				n, _ := parsePort(portArg)
				if n != 0 && n != cur.Port {
					return fmt.Errorf("adroute 已在其他端口运行，请先停止后再修改端口")
				}
			}
			out = started{Status: cur, Store: store, Already: true}
			return nil
		}
		if cur.Uncertain {
			return fmt.Errorf("%s", cur.Reason)
		}
		store, e := a.OpenStore(homePath())
		if e != nil {
			return e
		}
		port := portArg
		if port == "" {
			port = os.Getenv("ADROUTE_PORT")
		}
		if port == "" {
			port = "3001"
		}
		n, e := parsePort(port)
		if e != nil {
			return e
		}
		id, e := randomID()
		if e != nil {
			return fmt.Errorf("无法生成服务实例标识：%w", e)
		}
		if foreground {
			svc, e := runService(n, id)
			if e != nil {
				return e
			}
			s := getStatus()
			out = started{Status: s, Store: store, Service: svc}
			return nil
		}
		p := paths()
		if e = os.MkdirAll(p.Logs, 0700); e != nil {
			return e
		}
		if info, e := os.Lstat(p.Log); e == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !platform.PrivateFile(p.Log, info) {
				return fmt.Errorf("日志路径必须是普通文件")
			}
			if sys, err := platform.Lstat(p.Log); err != nil || sys.Nlink != 1 {
				return fmt.Errorf("日志路径必须是独占普通文件")
			}
			if info.Size() > 5<<20 {
				_ = os.Rename(p.Log, p.Log+".1")
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		log, e := os.OpenFile(p.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		if e = platform.ChmodFile(log, 0600); e != nil {
			log.Close()
			return e
		}
		exe, e := os.Executable()
		if e != nil {
			log.Close()
			return e
		}
		cmd := exec.Command(exe, "internal", "serve")
		cmd.Env = append(os.Environ(), "ADROUTE_HOME="+homePath(), "ADROUTE_PORT="+strconv.Itoa(n), "ADROUTE_INSTANCE_ID="+id)
		cmd.Stdin = nil
		cmd.Stdout = log
		cmd.Stderr = log
		platform.Detach(cmd)
		e = cmd.Start()
		_ = log.Close()
		if e != nil {
			return e
		}
		childDone := make(chan struct{})
		go func() { _ = cmd.Wait(); close(childDone) }()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			s := getStatus()
			if s.Running && s.InstanceID == id {
				out = started{Status: s, Store: store}
				return nil
			}
			select {
			case <-childDone:
				return fmt.Errorf("服务启动失败；请检查日志：%s", p.Log)
			default:
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = platform.StopChild(cmd.Process)
		return fmt.Errorf("服务启动失败或启动超时；可能是端口占用，请检查日志：%s", p.Log)
	})
	return out, e
}

type runningService struct {
	server   *http.Server
	listener net.Listener
	done     chan struct{}
	state    serviceState
}

func runService(port int, id string) (*runningService, error) {
	p := paths()
	store, e := a.OpenStore(homePath())
	if e != nil {
		return nil, e
	}
	ln, e := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if e != nil {
		return nil, fmt.Errorf("服务启动失败：端口可能已被占用")
	}
	actual := ln.Addr().(*net.TCPAddr).Port
	done := make(chan struct{})
	var srv *http.Server
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if e := srv.Shutdown(ctx); e != nil {
				_ = srv.Close()
			}
			_ = ln.Close()
			if current, er := readState(); er == nil && current.InstanceID == id && current.PID == os.Getpid() {
				_ = os.Remove(p.State)
			}
			close(done)
		})
	}
	promptTemplate, e := fs.ReadFile(root.EmbeddedFiles(), "docs/repair-agent-prompt.md")
	if e != nil {
		ln.Close()
		return nil, fmt.Errorf("repair prompt template unavailable")
	}
	app, e := a.NewHTTPApp(a.HTTPOptions{Store: store, Port: actual, InstanceID: id, Assets: root.EmbeddedFiles(), PromptRoot: homePath(), PromptTemplate: promptTemplate, Shutdown: func() { go shutdown() }, Log: func(v map[string]any) { b, _ := json.Marshal(v); fmt.Fprintln(os.Stderr, string(b)) }})
	if e != nil {
		_ = ln.Close()
		return nil, e
	}
	srv = &http.Server{Handler: app, ReadHeaderTimeout: 10 * time.Second}
	state := serviceState{PID: os.Getpid(), Port: actual, InstanceID: id, InstallationID: store.TokenPair().InstallationID, Version: version, URL: fmt.Sprintf("http://127.0.0.1:%d", actual)}
	if e = writePrivate(p.State, state); e != nil {
		ln.Close()
		return nil, e
	}
	svc := &runningService{srv, ln, done, state}
	go func() { _ = srv.Serve(ln) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := localHealth(state, store.TokenPair()); ok {
			return svc, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = srv.Close()
	_ = ln.Close()
	if current, er := readState(); er == nil && current.InstanceID == id {
		_ = os.Remove(p.State)
	}
	return nil, fmt.Errorf("服务监听就绪检查失败")
}
func serve() error {
	if os.Getenv("ADROUTE_INSTANCE_ID") == "" {
		return fmt.Errorf("missing daemon instance")
	}
	n, e := strconv.Atoi(os.Getenv("ADROUTE_PORT"))
	if e != nil {
		return e
	}
	svc, e := runService(n, os.Getenv("ADROUTE_INSTANCE_ID"))
	if e != nil {
		return e
	}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, platform.Signals()...)
	select {
	case <-sig:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if e := svc.server.Shutdown(ctx); e != nil {
			_ = svc.server.Close()
		}
		cancel()
		_ = svc.listener.Close()
		if s, e := readState(); e == nil && s.InstanceID == svc.state.InstanceID && s.PID == os.Getpid() {
			_ = os.Remove(paths().State)
		}
		return nil
	case <-svc.done:
		return nil
	}
}

type started struct {
	Status  statusResult
	Store   *a.Store
	Already bool
	Service *runningService
	Codex   codexResult
}

func startCodex(s *started) {
	st := s.Status
	tokens := s.Store.TokenPair()
	p := s.Store.Paths()
	v, e := a.ConnectCodex(a.CodexConfigOptions{Home: homePath(), URL: st.URL, ProxyToken: tokens.ProxyToken, InstallationID: tokens.InstallationID, Model: "adroute/default", CatalogPath: &p.Catalog, Env: envMap()})
	if e != nil {
		s.Codex = codexResult{Message: safeError(e)}
		return
	}
	s.Codex = codexResult{Configured: true, ConfigPath: v.ConfigPath}
}
func envMap() map[string]string {
	m := map[string]string{}
	for _, v := range os.Environ() {
		if i := strings.IndexByte(v, '='); i > 0 {
			m[v[:i]] = v[i+1:]
		}
	}
	return m
}
func printStarted(s started) {
	label := "adroute 已启动。"
	if s.Already {
		label = "adroute 已在运行。"
	}
	fmt.Println(label)
	if s.Status.Version != "" && s.Status.Version != version {
		fmt.Printf("后台版本为 %s，当前安装版本为 %s。请执行 adroute stop && adroute start，使新版本生效。\n", s.Status.Version, version)
	}
	store := s.Store
	fmt.Printf("\n管理页面：http://127.0.0.1:%d/#token=%s\n代理地址：%s/v1\nAPI Key：%s\n进程：%d\n日志：%s\n", s.Status.Port, store.TokenPair().AdminToken, s.Status.URL, store.TokenPair().ProxyToken, s.Status.PID, store.Paths().Log)
	if s.Codex.Configured {
		fmt.Printf("Codex 已自动接入：%s\n", s.Codex.ConfigPath)
	}
}
func waitForeground(s started, sig chan os.Signal) error {
	fmt.Println("Codex 已自动接入：" + s.Codex.ConfigPath)
	fmt.Println("正在前台运行，按 Ctrl+C 停止并恢复 Codex 配置。管理链接和 API Key 含本地访问凭据，请勿分享。")
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = s.Service.server.Shutdown(ctx)
	cancel()
	_ = s.Service.listener.Close()
	if st, e := readState(); e == nil && st.InstanceID == s.Service.state.InstanceID {
		_ = os.Remove(paths().State)
	}
	c, e := a.RestoreCodex(homePath())
	if e != nil {
		return e
	}
	reportRestore(c)
	return nil
}
func stop() (bool, error) {
	stopped := false
	e := withControl(func() error {
		s := getStatus()
		if s.Uncertain {
			return fmt.Errorf("%s", s.Reason)
		}
		if !s.Running {
			return nil
		}
		tokens, e := a.LoadTokens(homePath())
		if e != nil {
			return fmt.Errorf("无法读取本地访问令牌，拒绝停止服务：%w", e)
		}
		req, e := http.NewRequest("POST", fmt.Sprintf("%s/internal/shutdown", s.URL), nil)
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+tokens.AdminToken)
		req.Header.Set("X-Adroute-Instance", s.InstanceID)
		client := http.Client{Timeout: 2 * time.Second}
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		resp.Body.Close()
		if resp.StatusCode != 202 {
			return fmt.Errorf("服务未确认停止请求，未发送进程信号")
		}
		until := time.Now().Add(4 * time.Second)
		for time.Now().Before(until) {
			time.Sleep(80 * time.Millisecond)
			cur := getStatus()
			if !cur.Running && !cur.Uncertain {
				stopped = true
				return nil
			}
		}
		return fmt.Errorf("服务仍在停止中，请稍后查看 adroute status")
	})
	return stopped, e
}
