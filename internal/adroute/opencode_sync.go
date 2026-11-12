package adroute

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const openCodeLimit = 2 << 20

type SyncModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}
type OpenCodeSyncOptions struct {
	Models     []SyncModel
	Env        map[string]string
	ConfigPath string
}
type OpenCodeSyncResult struct {
	Changed         bool   `json:"changed"`
	ConfigPath      string `json:"configPath"`
	BackupPath      string `json:"backupPath"`
	ModelCount      int    `json:"modelCount"`
	RestartRequired bool   `json:"restartRequired"`
}

type jsoncNode struct {
	start, end int
	value      any
	props      map[string]*jsoncNode
	order      []string
}
type jsoncParser struct {
	s string
	i int
}

func jsoncInvalid() error {
	return appError("OpenCode config is not safe JSON/JSONC; duplicate keys, invalid UTF-8, or nesting over 100 levels are refused.", 409, "OPENCODE_CONFIG_INVALID")
}
func jsoncTrivia(s string, i int) (int, error) {
	for i < len(s) {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n':
			i++
		case i+1 < len(s) && s[i:i+2] == "//":
			i += 2
			for i < len(s) && s[i] != '\r' && s[i] != '\n' {
				i++
			}
		case i+1 < len(s) && s[i:i+2] == "/*":
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return i, jsoncInvalid()
			}
			i += j + 4
		default:
			return i, nil
		}
	}
	return i, nil
}
func (p *jsoncParser) trivia() error { n, e := jsoncTrivia(p.s, p.i); p.i = n; return e }
func (p *jsoncParser) stringValue() (string, error) {
	start := p.i
	p.i++
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '\\' {
			p.i += 2
			continue
		}
		p.i++
		if c == '"' {
			var v string
			if json.Unmarshal([]byte(p.s[start:p.i]), &v) != nil {
				return "", jsoncInvalid()
			}
			return v, nil
		}
	}
	return "", jsoncInvalid()
}
func (p *jsoncParser) value(depth int) (*jsoncNode, error) {
	if depth > 100 {
		return nil, jsoncInvalid()
	}
	if e := p.trivia(); e != nil {
		return nil, e
	}
	if p.i >= len(p.s) {
		return nil, jsoncInvalid()
	}
	n := &jsoncNode{start: p.i}
	c := p.s[p.i]
	if c == '{' {
		n.props = map[string]*jsoncNode{}
		n.value = map[string]any{}
		p.i++
		if e := p.trivia(); e != nil {
			return nil, e
		}
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			n.end = p.i
			return n, nil
		}
		for {
			if p.i >= len(p.s) || p.s[p.i] != '"' {
				return nil, jsoncInvalid()
			}
			key, e := p.stringValue()
			if e != nil {
				return nil, e
			}
			if _, ok := n.props[key]; ok {
				return nil, jsoncInvalid()
			}
			if e = p.trivia(); e != nil {
				return nil, e
			}
			if p.i >= len(p.s) || p.s[p.i] != ':' {
				return nil, jsoncInvalid()
			}
			p.i++
			child, e := p.value(depth + 1)
			if e != nil {
				return nil, e
			}
			n.props[key] = child
			n.order = append(n.order, key)
			n.value.(map[string]any)[key] = child.value
			if e = p.trivia(); e != nil {
				return nil, e
			}
			if p.i >= len(p.s) {
				return nil, jsoncInvalid()
			}
			if p.s[p.i] == '}' {
				p.i++
				break
			}
			if p.s[p.i] != ',' {
				return nil, jsoncInvalid()
			}
			p.i++
			if e = p.trivia(); e != nil {
				return nil, e
			}
			if p.i < len(p.s) && p.s[p.i] == '}' {
				p.i++
				break
			}
		}
	} else if c == '[' {
		arr := []any{}
		p.i++
		if e := p.trivia(); e != nil {
			return nil, e
		}
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			n.value = arr
			n.end = p.i
			return n, nil
		}
		for {
			child, e := p.value(depth + 1)
			if e != nil {
				return nil, e
			}
			arr = append(arr, child.value)
			if e = p.trivia(); e != nil {
				return nil, e
			}
			if p.i >= len(p.s) {
				return nil, jsoncInvalid()
			}
			if p.s[p.i] == ']' {
				p.i++
				break
			}
			if p.s[p.i] != ',' {
				return nil, jsoncInvalid()
			}
			p.i++
			if e = p.trivia(); e != nil {
				return nil, e
			}
			if p.i < len(p.s) && p.s[p.i] == ']' {
				p.i++
				break
			}
		}
		n.value = arr
	} else if c == '"' {
		v, e := p.stringValue()
		if e != nil {
			return nil, e
		}
		n.value = v
	} else {
		start := p.i
		for p.i < len(p.s) && !strings.ContainsRune(" \t\r\n,]}", rune(p.s[p.i])) {
			p.i++
		}
		token := p.s[start:p.i]
		if token == "true" || token == "false" || token == "null" {
			switch token {
			case "true":
				n.value = true
			case "false":
				n.value = false
			default:
				n.value = nil
			}
		} else {
			number := regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)
			if !number.MatchString(token) || !json.Valid([]byte(token)) {
				return nil, jsoncInvalid()
			}
			n.value = json.Number(token)
		}
	}
	n.end = p.i
	return n, nil
}
func parseJSONC(b []byte) (*jsoncNode, error) {
	if !bytes.Equal([]byte(string(b)), b) {
		return nil, jsoncInvalid()
	}
	if !utf8Valid(b) {
		return nil, jsoncInvalid()
	}
	s := string(b)
	offset := 0
	if strings.HasPrefix(s, "\ufeff") {
		offset = 3
	}
	p := &jsoncParser{s: s, i: offset}
	root, e := p.value(0)
	if e != nil {
		return nil, e
	}
	if e = p.trivia(); e != nil {
		return nil, e
	}
	if p.i != len(s) || root.props == nil {
		return nil, jsoncInvalid()
	}
	return root, nil
}
func utf8Valid(b []byte) bool { return utf8.Valid(b) }

func modelSnapshot(models []SyncModel) (map[string]any, int, error) {
	if models == nil {
		return nil, 0, appError("models snapshot is invalid", 409, "OPENCODE_MODELS_INVALID")
	}
	out := map[string]any{}
	for _, m := range models {
		_, _, publicID := ParsePublicModelID(m.Slug)
		if (!publicID && m.Slug != DefaultRouteID) || strings.TrimSpace(m.DisplayName) == "" || regexp.MustCompile(`[\x00-\x1f\x7f#]`).MatchString(m.DisplayName) {
			return nil, 0, appError("models snapshot is invalid", 409, "OPENCODE_MODELS_INVALID")
		}
		if _, ok := out[m.Slug]; ok {
			return nil, 0, appError("models snapshot is invalid", 409, "OPENCODE_MODELS_INVALID")
		}
		out[m.Slug] = map[string]string{"name": m.DisplayName, "modelID": m.Slug}
	}
	b, _ := json.Marshal(out)
	if len(b) > openCodeLimit {
		return nil, 0, appError("models snapshot is invalid", 409, "OPENCODE_MODELS_INVALID")
	}
	return out, len(out), nil
}
func syncEndpoint(v any) bool {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return false
	}
	if regexp.MustCompile(`^\{(?:env|file):[^{}\r\n]+\}$`).MatchString(s) {
		return true
	}
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func property(n *jsoncNode, k string) any {
	if n == nil || n.props == nil {
		return nil
	}
	if v := n.props[k]; v != nil {
		return v.value
	}
	return nil
}
func mapProperty(m map[string]any, k string) any {
	if m == nil {
		return nil
	}
	return m[k]
}
func renderModelValue(models map[string]any, eol, indent string) string {
	raw, _ := json.MarshalIndent(models, "", "  ")
	return strings.ReplaceAll(string(raw), "\n", eol+indent)
}
func updateOpenCode(source string, models map[string]any) (string, error) {
	root, e := parseJSONC([]byte(source))
	if e != nil {
		return "", e
	}
	providers := root.props["providers"]
	provider := (*jsoncNode)(nil)
	if providers != nil && providers.props != nil {
		provider = providers.props["adroute"]
	}
	config := map[string]any(nil)
	if provider != nil && provider.props != nil {
		config, _ = provider.value.(map[string]any)
	}
	settings, _ := property(provider, "settings").(map[string]any)
	credential := false
	if x, ok := property(provider, "env").([]any); ok && len(x) > 0 {
		credential = true
		for _, v := range x {
			if s, ok := v.(string); !ok || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(s) {
				credential = false
			}
		}
	}
	if k, ok := mapProperty(settings, "apiKey").(string); ok && strings.TrimSpace(k) != "" {
		credential = true
	}
	if headers, ok := property(provider, "headers").(map[string]any); ok {
		for k, v := range headers {
			if strings.EqualFold(k, "authorization") {
				s, ok := v.(string)
				credential = credential || ok && strings.TrimSpace(s) != ""
			}
		}
	}
	pkg, _ := property(provider, "package").(string)
	base := mapProperty(settings, "baseURL")
	if config == nil || (pkg != "@opencode/ai/providers/openai/responses" && pkg != "@opencode/ai/providers/openai-compatible/responses") || !syncEndpoint(base) || !credential {
		return "", appError("Configure compatible providers.adroute package, settings.baseURL and credentials first", 409, "OPENCODE_PROVIDER_INCOMPATIBLE")
	}
	if oldNode, exists := provider.props["models"]; exists {
		if oldNode.props == nil {
			return "", appError("Configure providers.adroute.models as an object before synchronization", 409, "OPENCODE_PROVIDER_INCOMPATIBLE")
		}
	}
	if existing := provider.props["models"]; existing != nil && reflect.DeepEqual(existing.value, models) {
		return source, nil
	}
	eol := "\n"
	if strings.Contains(source, "\r\n") {
		eol = "\r\n"
	}
	keyIndent := ""
	line := strings.LastIndex(source[:provider.start], "\n") + 1
	for line < provider.start && (source[line] == ' ' || source[line] == '\t') {
		keyIndent += source[line : line+1]
		line++
	}
	raw := renderModelValue(models, eol, keyIndent+"  ")
	if existing := provider.props["models"]; existing != nil {
		return source[:existing.start] + raw + source[existing.end:], nil
	}
	last := provider.props[provider.order[len(provider.order)-1]]
	close := provider.end - 1
	insertAt := last.end
	triviaEnd, err := jsoncTrivia(source, insertAt)
	if err != nil {
		return "", err
	}
	comma := "\n" + keyIndent + "  \"models\": " + raw
	if triviaEnd < close && source[triviaEnd] == ',' {
		insertAt = triviaEnd + 1
		comma = eol + keyIndent + "  \"models\": " + raw
	}
	if insertAt == last.end {
		comma = "," + eol + keyIndent + "  \"models\": " + raw
	}
	return source[:insertAt] + comma + source[insertAt:], nil
}

func validSyncPath(p string) bool {
	if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return false
	}
	for _, part := range strings.Split(p, string(os.PathSeparator)) {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
func locateOpenCode(o OpenCodeSyncOptions) (string, error) {
	if o.ConfigPath != "" {
		if !validSyncPath(o.ConfigPath) {
			return "", appError("configPath must be absolute without dot segments", 409, "OPENCODE_CONFIG_PATH")
		}
		return filepath.Clean(o.ConfigPath), nil
	}
	env := o.Env
	if env == nil {
		env = map[string]string{"HOME": os.Getenv("HOME"), "XDG_CONFIG_HOME": os.Getenv("XDG_CONFIG_HOME"), "OPENCODE_CONFIG": os.Getenv("OPENCODE_CONFIG"), "OPENCODE_CONFIG_DIR": os.Getenv("OPENCODE_CONFIG_DIR"), "OPENCODE_CONFIG_CONTENT": os.Getenv("OPENCODE_CONFIG_CONTENT")}
	}
	for _, k := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT"} {
		if env[k] != "" {
			return "", appError("OpenCode config override is set; specify configPath explicitly", 409, "OPENCODE_CONFIG_PATH")
		}
	}
	dir := env["XDG_CONFIG_HOME"]
	if dir == "" && filepath.IsAbs(env["HOME"]) {
		dir = filepath.Join(env["HOME"], ".config")
	}
	if !validSyncPath(dir) {
		return "", appError("Cannot determine OpenCode config path", 409, "OPENCODE_CONFIG_PATH")
	}
	folder := filepath.Join(dir, "opencode")
	a, b := filepath.Join(folder, "opencode.json"), filepath.Join(folder, "opencode.jsonc")
	_, ea := os.Lstat(a)
	_, eb := os.Lstat(b)
	if ea != nil && !os.IsNotExist(ea) || eb != nil && !os.IsNotExist(eb) {
		return "", appError("Cannot safely inspect OpenCode config candidates", 409, "OPENCODE_CONFIG_UNSAFE")
	}
	if ea == nil && eb == nil {
		return "", appError("Both OpenCode config files exist; specify configPath", 409, "OPENCODE_CONFIG_AMBIGUOUS")
	}
	if ea == nil {
		return a, nil
	}
	if eb == nil {
		return b, nil
	}
	return "", appError("No existing OpenCode config", 409, "OPENCODE_CONFIG_MISSING")
}

type syncParent struct {
	path     string
	dev, ino uint64
}

func parentSnapshot(path string) ([]syncParent, error) {
	dir := filepath.Dir(path)
	vol := filepath.VolumeName(dir)
	cur := vol + string(os.PathSeparator)
	rest := strings.TrimPrefix(dir, cur)
	parts := strings.Split(rest, string(os.PathSeparator))
	out := []syncParent{}
	for _, part := range append([]string{""}, parts...) {
		if part != "" {
			cur = filepath.Join(cur, part)
		}
		st, e := os.Lstat(cur)
		if e != nil {
			return nil, appError("OpenCode config parent directory is unavailable", 409, "OPENCODE_CONFIG_UNSAFE")
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, appError("OpenCode config parents must be real directories", 409, "OPENCODE_CONFIG_UNSAFE")
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, appError("Cannot inspect OpenCode directory", 409, "OPENCODE_CONFIG_UNSAFE")
		}
		out = append(out, syncParent{cur, uint64(sys.Dev), sys.Ino})
	}
	return out, nil
}
func checkParents(p []syncParent) error {
	for _, x := range p {
		st, e := os.Lstat(x.path)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return appError("OpenCode config directory changed during sync", 409, "OPENCODE_CONFIG_CHANGED")
		}
		sys := st.Sys().(*syscall.Stat_t)
		if uint64(sys.Dev) != x.dev || sys.Ino != x.ino {
			return appError("OpenCode config directory changed during sync", 409, "OPENCODE_CONFIG_CHANGED")
		}
	}
	return nil
}

type syncFile struct {
	bytes           []byte
	dev, ino, nlink uint64
	size            int64
	mtime, ctime    int64
	mode            os.FileMode
}

func readSyncFile(path string) (syncFile, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		if os.IsNotExist(e) {
			return syncFile{}, appError("OpenCode config is missing", 409, "OPENCODE_CONFIG_MISSING")
		}
		return syncFile{}, appError("OpenCode config cannot be safely opened", 409, "OPENCODE_CONFIG_UNSAFE")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return syncFile{}, appError("OpenCode config cannot be inspected", 409, "OPENCODE_CONFIG_UNSAFE")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || sys.Nlink != 1 {
		return syncFile{}, appError("OpenCode config must be a single-link regular file", 409, "OPENCODE_CONFIG_UNSAFE")
	}
	if st.Size() > openCodeLimit {
		return syncFile{}, appError("OpenCode config exceeds 2 MB", 409, "OPENCODE_CONFIG_INVALID")
	}
	b, e := io.ReadAll(io.LimitReader(f, openCodeLimit+1))
	if e != nil {
		return syncFile{}, appError("OpenCode config cannot be read", 409, "OPENCODE_CONFIG_UNSAFE")
	}
	if len(b) > openCodeLimit {
		return syncFile{}, appError("OpenCode config exceeds 2 MB", 409, "OPENCODE_CONFIG_INVALID")
	}
	after, e := f.Stat()
	if e != nil {
		return syncFile{}, appError("OpenCode config changed during read", 409, "OPENCODE_CONFIG_CHANGED")
	}
	as := after.Sys().(*syscall.Stat_t)
	if sys.Ino != as.Ino || sys.Size != as.Size || sys.Mtim != as.Mtim || sys.Ctim != as.Ctim {
		return syncFile{}, appError("OpenCode config changed during read", 409, "OPENCODE_CONFIG_CHANGED")
	}
	if !utf8Valid(b) {
		return syncFile{}, appError("OpenCode config contains invalid UTF-8", 409, "OPENCODE_CONFIG_INVALID")
	}
	return syncFile{b, uint64(sys.Dev), sys.Ino, sys.Nlink, sys.Size, sys.Mtim.Sec*1e9 + sys.Mtim.Nsec, sys.Ctim.Sec*1e9 + sys.Ctim.Nsec, st.Mode()}, nil
}
func sameSyncFile(a, b syncFile) bool {
	return a.dev == b.dev && a.ino == b.ino && a.nlink == b.nlink && a.size == b.size && a.mtime == b.mtime && a.ctime == b.ctime && a.mode == b.mode && bytes.Equal(a.bytes, b.bytes)
}
func randomSyncID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
func createSyncFile(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
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
	if _, e = f.Write(b); e != nil {
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
func syncParentDir(path string) error {
	f, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func syncIdentity(path string) (uint64, uint64, error) {
	st, e := os.Lstat(path)
	if e != nil {
		return 0, 0, e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return 0, 0, fmt.Errorf("not a regular file")
	}
	return uint64(sys.Dev), sys.Ino, nil
}

func SyncOpenCode(o OpenCodeSyncOptions) (OpenCodeSyncResult, error) {
	return syncOpenCodeWithHook(o, nil)
}

// syncOpenCodeWithHook places tests at the commit boundary. The final check is
// intentionally adjacent to rename. Portable rename is not an inode-CAS: an
// uncooperative writer can still race in the tiny check/rename interval.
func syncOpenCodeWithHook(o OpenCodeSyncOptions, beforeFinalCheck func()) (OpenCodeSyncResult, error) {
	mapping, count, e := modelSnapshot(o.Models)
	if e != nil {
		return OpenCodeSyncResult{}, e
	}
	path, e := locateOpenCode(o)
	if e != nil {
		return OpenCodeSyncResult{}, e
	}
	parents, e := parentSnapshot(path)
	if e != nil {
		return OpenCodeSyncResult{}, e
	}
	lock := path + ".adroute-sync.lock"
	lockID := randomSyncID()
	if e = createSyncFile(lock, []byte(lockID)); e != nil {
		return OpenCodeSyncResult{}, appError("Another OpenCode sync is active or a lock file exists; it will not be reclaimed", 409, "OPENCODE_CONFIG_BUSY")
	}
	lockDev, lockIno, e := syncIdentity(lock)
	if e != nil {
		return OpenCodeSyncResult{}, appError("Cannot verify OpenCode sync lock", 409, "OPENCODE_CONFIG_BUSY")
	}
	lockOwned := true
	var temp string
	var tempDev, tempIno uint64
	defer func() {
		if temp != "" {
			if dev, ino, err := syncIdentity(temp); err == nil && dev == tempDev && ino == tempIno {
				_ = os.Remove(temp)
			}
		}
		if lockOwned {
			if dev, ino, err := syncIdentity(lock); err == nil && dev == lockDev && ino == lockIno {
				_ = os.Remove(lock)
			}
		}
	}()
	if e = checkParents(parents); e != nil {
		return OpenCodeSyncResult{}, e
	}
	before, e := readSyncFile(path)
	if e != nil {
		return OpenCodeSyncResult{}, e
	}
	next, e := updateOpenCode(string(before.bytes), mapping)
	if e != nil {
		return OpenCodeSyncResult{}, e
	}
	if len(next) > openCodeLimit {
		return OpenCodeSyncResult{}, jsoncInvalid()
	}
	result := OpenCodeSyncResult{ConfigPath: path, ModelCount: count, RestartRequired: true}
	if bytes.Equal(before.bytes, []byte(next)) {
		return result, nil
	}
	if e = checkParents(parents); e != nil {
		return OpenCodeSyncResult{}, e
	}
	backup := path + ".adroute-before-" + randomSyncID() + ".bak"
	if e = createSyncFile(backup, before.bytes); e != nil {
		return OpenCodeSyncResult{}, appError("Cannot create durable private OpenCode backup", 409, "OPENCODE_CONFIG_WRITE_FAILED")
	}
	if e = syncParentDir(path); e != nil {
		return OpenCodeSyncResult{}, appError("Cannot durably save OpenCode backup", 409, "OPENCODE_CONFIG_WRITE_FAILED")
	}
	temp = path + ".adroute-" + randomSyncID() + ".tmp"
	if e = createSyncFile(temp, []byte(next)); e != nil {
		return OpenCodeSyncResult{}, appError("Cannot stage OpenCode config privately", 409, "OPENCODE_CONFIG_WRITE_FAILED")
	}
	tempDev, tempIno, e = syncIdentity(temp)
	if e != nil {
		return OpenCodeSyncResult{}, appError("Cannot verify staged OpenCode config", 409, "OPENCODE_CONFIG_WRITE_FAILED")
	}
	if e = checkParents(parents); e != nil {
		return OpenCodeSyncResult{}, e
	}
	li, e := os.Lstat(lock)
	if e != nil {
		return OpenCodeSyncResult{}, appError("OpenCode sync lock changed", 409, "OPENCODE_CONFIG_CHANGED")
	}
	liSys, ok := li.Sys().(*syscall.Stat_t)
	if !ok || !li.Mode().IsRegular() || li.Mode()&os.ModeSymlink != 0 || uint64(liSys.Dev) != lockDev || liSys.Ino != lockIno {
		return OpenCodeSyncResult{}, appError("OpenCode sync lock changed", 409, "OPENCODE_CONFIG_CHANGED")
	}
	if e = checkParents(parents); e != nil {
		return OpenCodeSyncResult{}, e
	}
	if dev, ino, err := syncIdentity(temp); err != nil || dev != tempDev || ino != tempIno {
		return OpenCodeSyncResult{}, appError("OpenCode staging file changed during sync", 409, "OPENCODE_CONFIG_CHANGED")
	}
	if beforeFinalCheck != nil {
		beforeFinalCheck()
	}
	if e = checkParents(parents); e != nil {
		return OpenCodeSyncResult{}, e
	}
	current, e := readSyncFile(path)
	if e != nil || !sameSyncFile(before, current) {
		return OpenCodeSyncResult{}, appError("OpenCode config changed during sync; refusing to overwrite", 409, "OPENCODE_CONFIG_CHANGED")
	}
	if e = os.Rename(temp, path); e != nil {
		return OpenCodeSyncResult{}, appError("Cannot atomically replace OpenCode config; original backup retained", 409, "OPENCODE_CONFIG_WRITE_FAILED")
	}
	temp = ""
	if e = syncParentDir(path); e != nil {
		return OpenCodeSyncResult{}, appError("OpenCode config was replaced but directory durability is uncertain", 409, "OPENCODE_CONFIG_WRITE_FAILED")
	}
	result.Changed = true
	result.BackupPath = backup
	return result, nil
}
