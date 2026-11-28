package aiquota

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errLogin = errors.New("请在本机官方客户端登录具有额度信息的账号，再刷新")
var errChanged = errors.New("本机客户端登录账号已变化，请重新添加额度账号")

func identity(s string) string { hash := sha256.Sum256([]byte(s)); return hex.EncodeToString(hash[:]) }
func readFileObject(path string) (object, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return readObject(f)
}

func executable(name string) (string, error) {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if path, e := exec.LookPath(exe); e == nil && filepath.IsAbs(path) {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"), filepath.Join(home, ".antigravity", "bin"))
	}
	if d := os.Getenv("LOCALAPPDATA"); runtime.GOOS == "windows" && filepath.IsAbs(d) {
		dirs = append(dirs, filepath.Join(d, "Programs", "OpenAI", "Codex", "bin"), filepath.Join(d, "agy", "bin"))
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, exe)
		if filepath.IsAbs(p) {
			if found, e := exec.LookPath(p); e == nil {
				return found, nil
			}
		}
	}
	return "", fmt.Errorf("未找到本机 %s 客户端，请先安装官方 CLI", name)
}
func command(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = os.TempDir()
	cmd.WaitDelay = 2 * time.Second
	hideQuotaWindow(cmd)
	return cmd
}
func queryLocal(ctx context.Context, id string) (Reading, error) {
	switch id {
	case "codex":
		return queryCodex(ctx)
	case "claude":
		return queryClaude()
	case "antigravity":
		return queryAntigravity(ctx)
	case "kiro":
		return queryKiro(ctx)
	}
	return Reading{}, errFormat
}
func queryCodex(ctx context.Context) (Reading, error) {
	path, e := executable("codex")
	if e != nil {
		return Reading{}, e
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := command(ctx, path, "app-server", "--listen", "stdio://", "-c", "analytics.enabled=false")
	in, e := cmd.StdinPipe()
	if e != nil {
		return Reading{}, e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		return Reading{}, e
	}
	if cmd.Start() != nil {
		return Reading{}, errors.New("无法启动 Codex 官方客户端")
	}
	defer func() { _ = in.Close(); cancel(); _ = cmd.Wait() }()
	enc := json.NewEncoder(in)
	dec := json.NewDecoder(io.LimitReader(out, maxResponse))
	dec.UseNumber()
	call := func(id int, method string, params any) (object, error) {
		req := object{"id": id, "method": method, "jsonrpc": "2.0"}
		if params != nil {
			req["params"] = params
		}
		if enc.Encode(req) != nil {
			return nil, errFormat
		}
		for {
			var msg object
			if dec.Decode(&msg) != nil {
				return nil, errors.New("无法完成 Codex 额度查询，请确认 CLI 版本和连接")
			}
			n, ok := num(msg["id"])
			if !ok || n != float64(id) {
				continue
			}
			if err := obj(msg["error"]); err != nil {
				return nil, classifyCLIError(str(err["message"]))
			}
			return obj(msg["result"]), nil
		}
	}
	if _, e = call(1, "initialize", object{"clientInfo": object{"name": "omnishare_quota", "version": "1"}}); e != nil {
		return Reading{}, e
	}
	if enc.Encode(object{"jsonrpc": "2.0", "method": "initialized"}) != nil {
		return Reading{}, errFormat
	}
	readAccount := func(id int) (string, error) {
		result, err := call(id, "account/read", object{"refreshToken": false})
		if err != nil {
			return "", err
		}
		account := obj(result["account"])
		email := strings.ToLower(strings.TrimSpace(str(account["email"])))
		if !strings.HasPrefix(str(account["type"]), "chatgpt") || email == "" {
			return "", errLogin
		}
		return identity(email), nil
	}
	owner, e := readAccount(2)
	if e != nil {
		return Reading{}, e
	}
	limits, e := call(3, "account/rateLimits/read", nil)
	if e != nil {
		return Reading{}, e
	}
	again, e := readAccount(4)
	if e != nil {
		return Reading{}, e
	}
	if owner != again {
		return Reading{}, errChanged
	}
	result := Reading{At: time.Now().Unix(), owner: owner}
	groups := obj(limits["rateLimitsByLimitId"])
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		codexGroup(&result, obj(limits["rateLimits"]), "")
	} else {
		for _, id := range ids {
			group := obj(groups[id])
			name := str(group["limitName"])
			if !shortText(name, 64) {
				name = id
			}
			if !shortText(name, 64) {
				continue
			}
			codexGroup(&result, group, name+" · ")
		}
	}
	if len(result.Meters) == 0 {
		return Reading{}, errFormat
	}
	return result, nil
}
func codexGroup(r *Reading, g object, prefix string) {
	for _, key := range []string{"primary", "secondary"} {
		w := obj(g[key])
		minutes, _ := num(w["windowDurationMins"])
		r.used(prefix+cadence(int64(minutes)), w["usedPercent"], stamp(w["resetsAt"]))
	}
	c := obj(g["credits"])
	if c["hasCredits"] == true {
		if c["unlimited"] == true {
			r.unlimited(prefix + "积分")
		} else {
			r.field(prefix+"积分", "积分", c["balance"])
		}
	}
}

func queryClaude() (Reading, error) {
	base, e := os.UserConfigDir()
	if e != nil {
		return Reading{}, errors.New("找不到 Claude 的配置目录")
	}
	dirs := []string{filepath.Join(base, "Claude")}
	if local := os.Getenv("LOCALAPPDATA"); runtime.GOOS == "windows" && filepath.IsAbs(local) {
		dirs = append(dirs, filepath.Join(local, "Claude"))
		for _, pattern := range []string{filepath.Join(local, "Claude-Data", "claude_*"), filepath.Join(local, "Packages", "Claude_*", "LocalCache", "Roaming", "Claude")} {
			found, _ := filepath.Glob(pattern)
			dirs = append(dirs, found...)
		}
	}
	var latest Reading
	for _, dir := range dirs {
		r, err := claudeRecord(dir)
		if err == nil && r.At > latest.At {
			latest = r
		}
	}
	if len(latest.Meters) == 0 {
		return Reading{}, errors.New("暂无可核实的 Claude 桌面版用量。请在本机 Claude 打开设置中的用量页面后刷新")
	}
	return latest, nil
}
func claudeRecord(dir string) (Reading, error) {
	config := filepath.Join(dir, "config.json")
	before, e := os.Stat(config)
	if e != nil {
		return Reading{}, e
	}
	current, e := readFileObject(config)
	if e != nil {
		return Reading{}, e
	}
	account := str(current["lastKnownAccountUuid"])
	if account == "" {
		return Reading{}, errLogin
	}
	d, e := readFileObject(filepath.Join(dir, "plan-usage-history.json"))
	if e != nil {
		return Reading{}, e
	}
	version, _ := num(d["version"])
	if version != 2 {
		return Reading{}, errFormat
	}
	var latest object
	var at int64
	now := time.Now().UnixMilli()
	for _, row := range rows(d["samples"]) {
		sample := obj(row)
		t, ok := num(sample["t"])
		if ok && int64(t) > before.ModTime().UnixMilli() && int64(t) <= now && int64(t) > at && str(sample["org"]) != "" {
			latest = sample
			at = int64(t)
		}
	}
	if latest == nil {
		return Reading{}, errFormat
	}
	after, e := os.Stat(config)
	if e != nil || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		return Reading{}, errChanged
	}
	r := Reading{At: at / 1000, owner: identity(account + "|" + str(latest["org"]))}
	u := obj(latest["u"])
	r.used("5 小时", u["fh"], 0)
	r.used("7 天", u["sd"], 0)
	if len(r.Meters) == 0 {
		return Reading{}, errFormat
	}
	return r, nil
}

// Older agy releases can interpret slash commands as prompts. Only the
// versions with the structured, zero-turn reporting contract are eligible.
var agyVersion = regexp.MustCompile(`^(?:Antigravity CLI version |Antigravity CLI |agy version |agy )?v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\+[A-Za-z0-9.-]+)?$`)

func agySupportsReport(line string) bool {
	m := agyVersion.FindStringSubmatch(strings.TrimSpace(strings.SplitN(line, "\n", 2)[0]))
	if m == nil {
		return false
	}
	version := [3]int{}
	for i := range version {
		n, e := strconv.Atoi(m[i+1])
		if e != nil {
			return false
		}
		version[i] = n
	}
	return version[0] > 1 || version[0] == 1 && (version[1] > 1 || version[1] == 1 && version[2] >= 11)
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxResponse {
		return 0, errFormat
	}
	return b.Buffer.Write(p)
}
func agyOutput(ctx context.Context, path string, args ...string) ([]byte, error) {
	c := command(ctx, path, args...)
	c.Env = append(os.Environ(), "AGY_CLI_DISABLE_AUTO_UPDATE=1")
	var out cappedBuffer
	c.Stdout = &out
	if c.Run() != nil {
		return nil, errors.New("无法读取 Antigravity 额度，请确认客户端已登录且支持额度报告")
	}
	return out.Bytes(), nil
}
func queryAntigravity(ctx context.Context) (Reading, error) {
	path, e := executable("agy")
	if e != nil {
		return Reading{}, e
	}
	ver, e := agyOutput(ctx, path, "--version")
	if e != nil {
		return Reading{}, e
	}
	if !agySupportsReport(string(ver)) {
		return Reading{}, errors.New("请将 Antigravity CLI 更新到支持只读额度报告的版本（1.1.11 或更高）")
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return Reading{}, e
	}
	paths := []string{filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")}
	if root := os.Getenv("ANTIGRAVITY_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return Reading{}, errFormat
		}
		paths = append(paths, filepath.Join(root, "settings.json"), filepath.Join(root, "antigravity-cli", "settings.json"))
	}
	for _, path := range paths {
		o, err := readFileObject(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Reading{}, errors.New("无法检查 Antigravity 斜杠命令设置")
		}
		if v := o["disableSlashCommands"]; v != nil && v != false {
			return Reading{}, errors.New("请在 Antigravity CLI 启用斜杠命令后重试")
		}
	}
	b, e := agyOutput(ctx, path, "--disable-slash-commands=false", "-p", "/usage", "--output-format", "json")
	if e != nil {
		return Reading{}, e
	}
	report, e := readObject(bytes.NewReader(b))
	if e != nil {
		return Reading{}, e
	}
	data := agyData(report, "usage")
	if data == nil {
		return Reading{}, errFormat
	}
	r := Reading{At: time.Now().Unix()}
	for _, v := range rows(data["groups"]) {
		g := obj(v)
		for _, item := range rows(g["buckets"]) {
			bucket := obj(item)
			f, ok := num(bucket["remaining_fraction"])
			if !ok || f < 0 || f > 1 || bucket["disabled"] == true {
				continue
			}
			label := strings.TrimSpace(str(g["name"]) + " " + str(bucket["window"]))
			if !shortText(label, 100) {
				label = str(bucket["id"])
			}
			r.value(label, "%", f*100, stamp(bucket["reset_time"]))
		}
	}
	// Credits and model windows are independent. A missing credits entitlement
	// must not discard a successful usage report.
	if b, err := agyOutput(ctx, path, "--disable-slash-commands=false", "-p", "/credits", "--output-format", "json"); err == nil {
		if report, err := readObject(bytes.NewReader(b)); err == nil {
			credits := agyData(report, "credits")
			if n, ok := num(credits["remaining_credits"]); ok && n >= 0 {
				r.value("可用积分", "积分", n, 0)
			}
		}
	}
	if len(r.Meters) == 0 {
		return Reading{}, errFormat
	}
	return r, nil
}
func agyData(report object, name string) object {
	turns, ok := num(report["num_turns"])
	tokens := obj(report["usage"])["total_tokens"]
	if tokens != nil {
		n, valid := num(tokens)
		if !valid || n != 0 {
			return nil
		}
	}
	c := obj(report["command"])
	if report["status"] != "SUCCESS" || !ok || turns != 0 || c["name"] != name {
		return nil
	}
	return obj(c["data"])
}
