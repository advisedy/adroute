package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func buildCLI(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "adroute")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/adroute")
	cmd.Dir = filepath.Clean(filepath.Join("..", ".."))
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build CLI: %v\n%s", e, b)
	}
	return out
}
func cliEnv(t *testing.T, bin string) (string, []string) {
	t.Helper()
	home := t.TempDir()
	env := append(os.Environ(), "ADROUTE_HOME="+filepath.Join(home, "adroute"), "ADROUTE_PORT=0", "CODEX_HOME="+filepath.Join(home, "codex"), "ADROUTE_CODEX_BIN="+filepath.Join(home, "no-codex"))
	return home, env
}
func runCLI(t *testing.T, bin string, env []string, args ...string) (int, string, string) {
	return runCLIInDir(t, bin, env, t.TempDir(), args...)
}
func runCLIInDir(t *testing.T, bin string, env []string, dir string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir = dir
	c.Env = env
	out, e := c.Output()
	stderr := ""
	if x, ok := e.(*exec.ExitError); ok {
		stderr = string(x.Stderr)
		return x.ExitCode(), string(out), stderr
	}
	if e != nil {
		t.Fatal(e)
	}
	return 0, string(out), stderr
}

func TestBuiltCLICodexRestoreUsesOriginalAdrouteHomePrecedence(t *testing.T) {
	bin := buildCLI(t)
	root := t.TempDir()
	cases := []struct {
		name, adroute, xdg, home string
		wantHome                 string
	}{
		{name: "explicit ADROUTE_HOME wins", adroute: filepath.Join(root, "explicit"), xdg: filepath.Join(root, "xdg"), home: filepath.Join(root, "user"), wantHome: filepath.Join(root, "explicit")},
		{name: "XDG_CONFIG_HOME fallback", xdg: filepath.Join(root, "xdg"), home: filepath.Join(root, "user"), wantHome: filepath.Join(root, "xdg", "adroute")},
		{name: "relative XDG_CONFIG_HOME resolves from working directory", xdg: "relative-xdg", home: filepath.Join(root, "user"), wantHome: filepath.Join("relative-xdg", "adroute")},
		{name: "HOME config fallback", home: filepath.Join(root, "user"), wantHome: filepath.Join(root, "user", ".config", "adroute")},
		{name: "relative ADROUTE_HOME resolves from working directory", adroute: "relative/state", xdg: filepath.Join(root, "xdg"), home: filepath.Join(root, "user"), wantHome: filepath.Join("relative", "state")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := filepath.Join(root, "work")
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			wantPath := tc.wantHome
			if !filepath.IsAbs(wantPath) {
				wantPath = filepath.Join(work, wantPath)
			}
			wantHome, err := filepath.Abs(wantPath)
			if err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(wantHome, "codex", "connection.json")
			if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journal, []byte("not-json"), 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tc.home, "CODEX_HOME=" + filepath.Join(root, "codex")}
			if tc.adroute != "" {
				env = append(env, "ADROUTE_HOME="+tc.adroute)
			}
			if tc.xdg != "" {
				env = append(env, "XDG_CONFIG_HOME="+tc.xdg)
			}
			code, stdout, stderr := runCLIInDir(t, bin, env, work, "codex", "restore")
			if code != 1 || !strings.Contains(stdout+stderr, "Codex connection record is invalid") {
				t.Fatalf("CLI did not read expected isolated journal at %s: code=%d stdout=%q stderr=%q", journal, code, stdout, stderr)
			}
		})
	}
}

func TestBuiltCLIHelpVersionAndNoMigrate(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	code, out, _ := runCLI(t, bin, env, "--help")
	if code != 0 || !strings.Contains(out, "adroute start") || strings.Contains(out, "adroute migrate") {
		t.Fatalf("help %d %q", code, out)
	}
	code, out, _ = runCLI(t, bin, env, "--version")
	if code != 0 || strings.TrimSpace(out) != version {
		t.Fatalf("version %d %q", code, out)
	}
	code, _, _ = runCLI(t, bin, env, "migrate")
	if code != 1 {
		t.Fatalf("migrate exit %d", code)
	}
}

func TestBuiltCLIBasicBackgroundLifecycleAndEmbeddedHTTP(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	t.Cleanup(func() { _, _, _ = runCLI(t, bin, env, "stop") })
	code, out, errout := runCLI(t, bin, env, "start", "--port", "0")
	if code != 0 {
		t.Fatalf("start: %d %s %s", code, out, errout)
	}
	var status struct {
		Running bool   `json:"running"`
		Port    int    `json:"port"`
		URL     string `json:"url"`
		PID     int    `json:"pid"`
	}
	code, sout, _ := runCLI(t, bin, env, "status", "--json")
	if code != 0 || json.Unmarshal([]byte(sout), &status) != nil || !status.Running || status.Port == 0 {
		t.Fatalf("status: %d %s", code, sout)
	}
	resp, e := http.Get(status.URL + "/api/state")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API unexpectedly unauthenticated: %d", resp.StatusCode)
	}
	var token struct {
		AdminToken string `json:"adminToken"`
	}
	tokensPath := ""
	for _, v := range env {
		if strings.HasPrefix(v, "ADROUTE_HOME=") {
			tokensPath = filepath.Join(strings.TrimPrefix(v, "ADROUTE_HOME="), "tokens.json")
		}
	}
	tb, e := os.ReadFile(tokensPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(tb, &token); e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequest("GET", status.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+token.AdminToken)
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("authenticated API state %d", resp.StatusCode)
	}
	resp, e = http.Get(status.URL + "/")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("embedded index status=%d", resp.StatusCode)
	}
	code, dup, _ := runCLI(t, bin, env, "start")
	if code != 0 || !strings.Contains(dup, "已在运行") {
		t.Fatalf("duplicate start %d %s", code, dup)
	}
	if !strings.Contains(dup, "API Key：") {
		t.Fatalf("duplicate start omitted proxy key: %s", dup)
	}
	code, _, errout = runCLI(t, bin, env, "stop")
	if code != 0 {
		t.Fatalf("stop: %d %s", code, errout)
	}
	code, sout, _ = runCLI(t, bin, env, "status", "--json")
	if code != 3 {
		t.Fatalf("stopped status %d %s", code, sout)
	}
}

func TestBuiltCLIStopAcknowledgesUnderHTTPLoadAndRepeatedStop(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	code, _, errout := runCLI(t, bin, env, "start", "--port", "0")
	if code != 0 {
		t.Fatalf("start: %d %s", code, errout)
	}
	code, sout, _ := runCLI(t, bin, env, "status", "--json")
	if code != 0 {
		t.Fatal(sout)
	}
	var s struct {
		URL string `json:"url"`
	}
	if e := json.Unmarshal([]byte(sout), &s); e != nil {
		t.Fatal(e)
	}
	stopLoad := make(chan struct{})
	var wg sync.WaitGroup
	client := http.Client{Timeout: 400 * time.Millisecond}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopLoad:
					return
				default:
				}
				r, e := client.Get(s.URL + "/")
				if e == nil {
					r.Body.Close()
				}
			}
		}()
	}
	code, _, errout = runCLI(t, bin, env, "stop")
	close(stopLoad)
	wg.Wait()
	if code != 0 {
		t.Fatalf("stop under load: %d %s", code, errout)
	}
	code, out, errout := runCLI(t, bin, env, "stop")
	if code != 0 || !strings.Contains(out, "已处于停止状态") {
		t.Fatalf("repeated stop: %d %s %s", code, out, errout)
	}
}

func TestBuiltCLIPortCollisionDoesNotReportSuccess(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	code, out, errout := runCLI(t, bin, env, "start", "--port", strconv.Itoa(port))
	if code != 1 || strings.Contains(out, "已启动") || !strings.Contains(errout, "启动失败") {
		t.Fatalf("collision %d %q %q", code, out, errout)
	}
}

func TestBuiltCLIStaleLivePIDIsNeverSignaled(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	home := ""
	for _, v := range env {
		if strings.HasPrefix(v, "ADROUTE_HOME=") {
			home = strings.TrimPrefix(v, "ADROUTE_HOME=")
		}
	}
	if e := os.MkdirAll(filepath.Join(home, "run"), 0700); e != nil {
		t.Fatal(e)
	}
	state := map[string]any{"PID": os.Getpid(), "Port": 12345, "InstanceID": "not-this-service", "InstallationID": "wrong", "Version": "old", "URL": "http://127.0.0.1:12345"}
	b, _ := json.Marshal(state)
	if e := os.WriteFile(filepath.Join(home, "run", "service.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	code, _, errout := runCLI(t, bin, env, "stop")
	if code != 1 || !strings.Contains(errout, "拒绝") {
		t.Fatalf("stale state stop %d %s", code, errout)
	}
	code, out, _ := runCLI(t, bin, env, "status", "--json")
	if code != 1 || !strings.Contains(out, "\"uncertain\":true") {
		t.Fatalf("stale state status %d %s", code, out)
	}
	if e := syscall.Kill(os.Getpid(), 0); e != nil {
		t.Fatal("test process was signaled", e)
	}
}

func TestBuiltCLIStatusAndStopIgnoreCorruptProviderConfigButRequireDaemonIdentity(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	home := ""
	for _, v := range env {
		if strings.HasPrefix(v, "ADROUTE_HOME=") {
			home = strings.TrimPrefix(v, "ADROUTE_HOME=")
		}
	}
	code, _, errout := runCLI(t, bin, env, "start", "--port", "0")
	if code != 0 {
		t.Fatalf("start: %d %s", code, errout)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	code, status, errout := runCLI(t, bin, env, "status", "--json")
	if code != 0 || !strings.Contains(status, `"running":true`) {
		t.Fatalf("status with corrupt config: %d %s %s", code, status, errout)
	}
	code, _, errout = runCLI(t, bin, env, "stop")
	if code != 0 {
		t.Fatalf("stop with corrupt config: %d %s", code, errout)
	}
}

func TestBuiltCLIForegroundSignalAndRecordedCodexRestore(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	codex := ""
	home := ""
	for _, v := range env {
		if strings.HasPrefix(v, "CODEX_HOME=") {
			codex = strings.TrimPrefix(v, "CODEX_HOME=")
		}
		if strings.HasPrefix(v, "ADROUTE_HOME=") {
			home = strings.TrimPrefix(v, "ADROUTE_HOME=")
		}
	}
	if e := os.MkdirAll(codex, 0700); e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(codex, "config.toml")
	original := "# preserve me\nmodel = \"original\"\n"
	if e := os.WriteFile(config, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "start", "--foreground")
	cmd.Env = env
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	output := make(chan string, 1)
	go func() { b, _ := io.ReadAll(stdout); output <- string(b) }()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		b, e := os.ReadFile(config)
		if e == nil && bytes.Contains(b, []byte("experimental_bearer_token")) {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	configured, e := os.ReadFile(config)
	if e != nil || !bytes.Contains(configured, []byte("experimental_bearer_token")) {
		_ = cmd.Process.Kill()
		t.Fatalf("foreground did not connect Codex: %v %s", e, stderr.String())
	}
	if e = os.WriteFile(config, append(configured, []byte("# later user edit\n")...), 0600); e != nil {
		t.Fatal(e)
	}
	if e = cmd.Process.Signal(syscall.SIGINT); e != nil {
		t.Fatal(e)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case e := <-wait:
		if e != nil {
			t.Fatalf("foreground exit: %v stderr=%s", e, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("foreground shutdown timed out")
	}
	restored, e := os.ReadFile(config)
	if e != nil || !bytes.Contains(restored, []byte(original)) || !bytes.Contains(restored, []byte("later user edit")) {
		t.Fatalf("restore did not preserve user edit: %v %q", e, restored)
	}
	if _, e = os.Stat(filepath.Join(home, "run", "service.json")); !os.IsNotExist(e) {
		t.Fatalf("own service state remains: %v", e)
	}
	_ = output
}

func TestBuiltCLIStandaloneRestoreUsesRecordedCODEXHome(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	target := ""
	home := ""
	for _, v := range env {
		if strings.HasPrefix(v, "CODEX_HOME=") {
			target = strings.TrimPrefix(v, "CODEX_HOME=")
		}
		if strings.HasPrefix(v, "ADROUTE_HOME=") {
			home = strings.TrimPrefix(v, "ADROUTE_HOME=")
		}
	}
	if e := os.MkdirAll(target, 0700); e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(target, "config.toml")
	_ = os.WriteFile(config, []byte("# exact baseline\nmodel = \"before\"\n"), 0600)
	code, _, errout := runCLI(t, bin, env, "start")
	if code != 0 {
		t.Fatalf("start %d %s", code, errout)
	}
	connected, _ := os.ReadFile(config)
	_ = os.WriteFile(config, append(connected, []byte("# later setting\n")...), 0600)
	changedEnv := append([]string{}, env...)
	for i, v := range changedEnv {
		if strings.HasPrefix(v, "CODEX_HOME=") {
			changedEnv[i] = "CODEX_HOME=" + filepath.Join(home, "different")
		}
	}
	code, out, errout := runCLI(t, bin, changedEnv, "codex", "restore")
	if code != 0 || !strings.Contains(out, "已恢复") {
		t.Fatalf("restore %d %s %s", code, out, errout)
	}
	restored, e := os.ReadFile(config)
	if e != nil || !strings.Contains(string(restored), "# exact baseline") || !strings.Contains(string(restored), "# later setting") {
		t.Fatalf("restore content %v %q", e, restored)
	}
	_, _, _ = runCLI(t, bin, env, "stop")
}

func TestBuiltCLISetupFailureLeavesDaemonRunningAndStandaloneRestoreWorks(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	codex := ""
	for _, v := range env {
		if strings.HasPrefix(v, "CODEX_HOME=") {
			codex = strings.TrimPrefix(v, "CODEX_HOME=")
		}
	}
	if e := os.MkdirAll(codex, 0700); e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(codex, "config.toml")
	invalid := "bad = \"synthetic-private\n"
	if e := os.WriteFile(config, []byte(invalid), 0600); e != nil {
		t.Fatal(e)
	}
	code, _, stderr := runCLI(t, bin, env, "start")
	if code != 1 || !strings.Contains(stderr, "Codex 自动接入失败") || strings.Contains(stderr, "synthetic-private") {
		t.Fatalf("setup failure result %d %s", code, stderr)
	}
	code, out, _ := runCLI(t, bin, env, "status", "--json")
	if code != 0 || !strings.Contains(out, "\"running\":true") {
		t.Fatalf("daemon should remain running %d %s", code, out)
	}
	if b, e := os.ReadFile(config); e != nil || string(b) != invalid {
		t.Fatalf("invalid config overwritten: %v %q", e, b)
	}
	_ = os.WriteFile(config, []byte("# fixed\n"), 0600)
	_, _, _ = runCLI(t, bin, env, "stop")
}

func cliEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, v := range env {
		if strings.HasPrefix(v, prefix) {
			return strings.TrimPrefix(v, prefix)
		}
	}
	return ""
}

func cliStatus(t *testing.T, bin string, env []string) statusResult {
	t.Helper()
	code, out, errout := runCLI(t, bin, env, "status", "--json")
	if code != 0 {
		t.Fatalf("status: %d %s %s", code, out, errout)
	}
	var s statusResult
	if e := json.Unmarshal([]byte(out), &s); e != nil || !s.Running || s.Port == 0 {
		t.Fatalf("status json: %v %s", e, out)
	}
	return s
}

func cliAdminToken(t *testing.T, env []string) string {
	t.Helper()
	home := cliEnvValue(env, "ADROUTE_HOME")
	b, e := os.ReadFile(filepath.Join(home, "tokens.json"))
	if e != nil {
		t.Fatal(e)
	}
	var tokens struct {
		AdminToken string `json:"adminToken"`
	}
	if e = json.Unmarshal(b, &tokens); e != nil || tokens.AdminToken == "" {
		t.Fatalf("tokens: %v", e)
	}
	return tokens.AdminToken
}

func cliAdminJSON(t *testing.T, method, url, token string, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, e := http.NewRequest(method, url, reader)
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func cliAdminJSONBody(t *testing.T, url, token string) []byte {
	t.Helper()
	status, raw := cliAdminJSON(t, "GET", url, token, "")
	if status != 200 {
		t.Fatalf("GET %s: %d %s", url, status, raw)
	}
	return raw
}

// restart must keep the port, keep the model configuration and re-apply the
// Codex config from the binary that runs the command.
func TestBuiltCLIRestartKeepsPortAndConfiguration(t *testing.T) {
	bin := buildCLI(t)
	_, env := cliEnv(t, bin)
	t.Cleanup(func() { _, _, _ = runCLI(t, bin, env, "stop") })

	// Restarting a stopped service just starts it.
	code, out, errout := runCLI(t, bin, env, "restart", "--port", "0")
	if code != 0 || !strings.Contains(out, "未运行，直接启动") || !strings.Contains(out, "已启动") {
		t.Fatalf("restart from stopped: %d %s %s", code, out, errout)
	}
	first := cliStatus(t, bin, env)
	token := cliAdminToken(t, env)
	if status, raw := cliAdminJSON(t, "POST", first.URL+"/api/groups", token, `{"name":"work"}`); status != 201 {
		t.Fatalf("create group: %d %s", status, raw)
	}

	code, out, errout = runCLI(t, bin, env, "restart")
	if code != 0 || !strings.Contains(out, "已停止，正在用当前可执行文件重新启动") || !strings.Contains(out, "已启动") {
		t.Fatalf("restart: %d %s %s", code, out, errout)
	}
	second := cliStatus(t, bin, env)
	if second.Port != first.Port {
		t.Fatalf("restart changed port %d -> %d", first.Port, second.Port)
	}
	if second.PID == first.PID {
		t.Fatalf("restart did not replace the process (pid %d)", second.PID)
	}
	list := string(cliAdminJSONBody(t, second.URL+"/api/state", token))
	if !strings.Contains(list, `"work"`) {
		t.Fatalf("restart lost model groups: %s", list)
	}

	codexHome := cliEnvValue(env, "CODEX_HOME")
	config, e := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(config), `model = "adroute/default"`) || !strings.Contains(string(config), `base_url = "`+second.URL+`/v1"`) {
		t.Fatalf("restart did not re-apply the Codex config: %s", config)
	}

	// An explicit --port still applies when it matches the recorded port, and a
	// different port is rejected while the service is running.
	code, _, errout = runCLI(t, bin, env, "restart", "--port", strconv.Itoa(second.Port))
	if code != 0 || strings.Contains(errout, "已在其他端口运行") {
		t.Fatalf("restart with same port: %d %s", code, errout)
	}
}
