// Package aiquota reads account limits without issuing model requests.
package aiquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Provider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Hint     string `json:"hint"`
	Local    bool   `json:"local"`
	Site     bool   `json:"site"`
	endpoint string
}

var providers = []Provider{
	{"codex", "Codex", "本机客户端", "使用本机 Codex CLI 已登录的 ChatGPT 订阅。", true, false, ""},
	{"claude", "Claude 桌面版", "本机客户端", "读取官方桌面版的用量记录。请先在 Claude 中打开用量页面。", true, false, ""},
	{"antigravity", "Antigravity CLI", "本机客户端", "使用已登录的 agy 1.1.11 或更高版本查询模型组额度。", true, false, ""},
	{"kiro", "Kiro CLI", "本机客户端", "使用 Kiro CLI 3.1 或更高版本的 /usage 查询积分。", true, false, ""},
	{"deepseek", "DeepSeek", "账户余额", "使用 DeepSeek 开放平台 API Key。", false, false, "https://api.deepseek.com/user/balance"},
	{"openrouter", "OpenRouter", "令牌额度", "使用普通 API Key 查询此密钥的限额和已用金额。", false, false, "https://openrouter.ai/api/v1/key"},
	{"gemini", "Google Gemini Code Assist", "编程订阅", "使用有权查看该 Code Assist 项目的 Google OAuth 访问令牌。令牌仅在本次会话内使用。", false, false, "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"},
	{"xai", "xAI API", "账户余额", "使用具有团队账单读取权限的 Management Key 查询预付账本。", false, false, "https://management-api.x.ai/v1/billing/teams/"},
	{"moonshot-cn", "Moonshot 开放平台 · 中国", "账户余额", "使用中国站开放平台 API Key 查询账户余额。", false, false, "https://api.moonshot.cn/v1/users/me/balance"},
	{"moonshot-global", "Moonshot 开放平台 · 国际", "账户余额", "使用国际站开放平台 API Key 查询账户余额。", false, false, "https://api.moonshot.ai/v1/users/me/balance"},
	{"kimi", "Kimi Code", "编程订阅", "使用具有 Kimi Code 订阅额度查询权限的凭据。", false, false, "https://api.kimi.com/coding/v1/usages"},
	{"glm-cn", "智谱 GLM Coding Plan · 中国", "编程订阅", "使用中国站 Coding Plan 套餐 Key。", false, false, "https://open.bigmodel.cn/api/monitor/usage/quota/limit"},
	{"glm-global", "Z.AI GLM Coding Plan · 国际", "编程订阅", "使用 Z.AI Coding Plan 套餐 Key。", false, false, "https://api.z.ai/api/monitor/usage/quota/limit"},
	{"minimax-cn", "MiniMax Coding Plan · 中国", "编程订阅", "使用中国站编程套餐 Key。", false, false, "https://api.minimax.cn/v1/token_plan/remains"},
	{"minimax-global", "MiniMax Coding Plan · 国际", "编程订阅", "使用国际站编程套餐 Key。", false, false, "https://api.minimax.io/v1/token_plan/remains"},
	{"opencode", "OpenCode Go", "编程订阅", "使用 OpenCode Go 套餐 API Key。", false, false, "https://opencode.ai/zen/go/v1/usage"},
	{"newapi", "New API", "自托管网关", "使用此站点的 API 令牌查询令牌配额。", false, true, "/api/usage/token/"},
	{"oneapi", "One API", "自托管网关", "使用此站点的 API 令牌查询兼容计费接口。", false, true, "/dashboard/billing/subscription"},
	{"sub2api", "Sub2API", "自托管网关", "使用此站点的令牌查询 /v1/usage。", false, true, "/v1/usage"},
	{"balance", "通用余额接口", "自托管网关", "站点需提供 /user/balance 和明确的余额字段。", false, true, "/user/balance"},
}

func Providers() []Provider { return append([]Provider(nil), providers...) }

func findProvider(id string) (Provider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

type Meter struct {
	Label     string   `json:"label"`
	Value     *float64 `json:"value,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	Unlimited bool     `json:"unlimited,omitempty"`
	ResetAt   int64    `json:"reset_at,omitempty"`
}
type Reading struct {
	At     int64   `json:"at"`
	Meters []Meter `json:"meters"`
	owner  string
}
type object = map[string]any

func obj(v any) object { o, _ := v.(map[string]any); return o }
func rows(v any) []any { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func shortText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len([]rune(s)) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}
func num(v any) (float64, bool) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case string:
		s = strings.TrimSpace(x)
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	default:
		return 0, false
	}
	f, e := strconv.ParseFloat(s, 64)
	return f, e == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}
func stamp(v any) int64 {
	if f, ok := num(v); ok {
		if f >= 1e11 {
			f /= 1000
		}
		if f > 0 && f <= 253402300799 {
			return int64(f)
		}
	}
	if t, e := time.Parse(time.RFC3339, str(v)); e == nil && t.Unix() > 0 {
		return t.Unix()
	}
	return 0
}
func (r *Reading) value(label, unit string, n float64, reset int64) {
	if len(r.Meters) < 40 && shortText(label, 120) && shortText(unit, 24) && !math.IsNaN(n) && !math.IsInf(n, 0) {
		r.Meters = append(r.Meters, Meter{Label: label, Unit: unit, Value: &n, ResetAt: reset})
	}
}
func (r *Reading) field(label, unit string, v any) {
	if n, ok := num(v); ok {
		r.value(label, unit, n, 0)
	}
}
func (r *Reading) used(label string, v any, reset int64) {
	if n, ok := num(v); ok && n >= 0 && n <= 100 {
		r.value(label, "%", 100-n, reset)
	}
}
func (r *Reading) unlimited(label string) {
	if len(r.Meters) < 40 {
		r.Meters = append(r.Meters, Meter{Label: label, Unlimited: true})
	}
}
func cadence(minutes int64) string {
	switch {
	case minutes <= 0:
		return "当前周期"
	case minutes%1440 == 0:
		return fmt.Sprintf("%d 天", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%d 小时", minutes/60)
	default:
		return fmt.Sprintf("%d 分钟", minutes)
	}
}

var errFormat = errors.New("来源未返回可识别的额度，请核对套餐、接口及客户端版本")

const maxResponse = 2 << 20

func readObject(in io.Reader) (object, error) {
	b, e := io.ReadAll(io.LimitReader(in, maxResponse+1))
	if e != nil || len(b) > maxResponse {
		return nil, errFormat
	}
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})))
	d.UseNumber()
	var o object
	if d.Decode(&o) != nil || o == nil || d.Decode(new(any)) != io.EOF {
		return nil, errFormat
	}
	return o, nil
}

func normalizeSite(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e == nil && u.IsAbs() && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.ContainsAny(u.Path, "\\\x00\r\n") {
		host := u.Hostname()
		if u.Scheme == "https" || (u.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback())) {
			return strings.TrimRight(u.String(), "/"), nil
		}
	}
	return "", errors.New("请输入站点的 HTTPS 地址；本机回环地址也支持 HTTP")
}

type ProviderError struct {
	Status int
	Retry  time.Duration
}

func (e *ProviderError) Error() string {
	hint := "服务暂不可用"
	switch {
	case e.Status == 401 || e.Status == 403:
		hint = "凭据无效、地区不匹配或套餐未授权"
	case e.Status == 404:
		hint = "此站点或版本没有所选额度接口"
	case e.Status == 429:
		hint = "查询过于频繁，请稍后重试"
	case e.Status >= 300 && e.Status < 400:
		hint = "服务要求重定向，请核对地区或填写最终站点地址"
	}
	return fmt.Sprintf("%s（HTTP %d）", hint, e.Status)
}
func fetchJSON(ctx context.Context, client *http.Client, address, key string, rawKey bool, payload []byte) (object, error) {
	method := http.MethodGet
	if payload != nil {
		method = http.MethodPost
	}
	req, e := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(payload))
	if e != nil {
		return nil, errors.New("无效的额度服务地址")
	}
	auth := key
	if !rawKey {
		auth = "Bearer " + key
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "OmniShare/AI-Quota")
	res, e := client.Do(req)
	if e != nil {
		return nil, errors.New("无法连接额度服务，请检查网络、代理和服务地址")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var retry time.Duration
		if res.StatusCode == 429 {
			secs, err := strconv.Atoi(res.Header.Get("Retry-After"))
			if err != nil {
				secs = 60
				if t, err := http.ParseTime(res.Header.Get("Retry-After")); err == nil {
					secs = int(time.Until(t).Seconds())
				}
			}
			retry = time.Duration(min(3600, max(30, secs))) * time.Second
		}
		return nil, &ProviderError{res.StatusCode, retry}
	}
	return readObject(res.Body)
}

var googleProjectID = regexp.MustCompile(`^(?:[a-z][a-z0-9-]{4,28}[a-z0-9]|[0-9]{6,20})$`)
var xaiTeamID = regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

func validateResource(id, value string) error {
	switch id {
	case "gemini":
		if !googleProjectID.MatchString(value) {
			return errors.New("请输入项目 ID 或数字项目编号")
		}
	case "xai":
		if !xaiTeamID.MatchString(value) {
			return errors.New("请输入 xAI 控制台的团队 UUID")
		}
	default:
		if value != "" {
			return errors.New("此来源不使用项目或团队 ID")
		}
	}
	return nil
}
func query(ctx context.Context, client *http.Client, p Provider, target, key string) (Reading, error) {
	if p.Local {
		return queryLocal(ctx, p.ID)
	}
	address := p.endpoint
	var payload []byte
	if p.Site {
		base, err := normalizeSite(target)
		if err != nil {
			return Reading{}, err
		}
		if p.ID == "sub2api" {
			base = strings.TrimSuffix(base, "/v1")
		}
		address = base + p.endpoint
	} else if p.ID == "gemini" || p.ID == "xai" {
		if err := validateResource(p.ID, target); err != nil {
			return Reading{}, err
		}
		if p.ID == "gemini" {
			payload, _ = json.Marshal(map[string]string{"project": target})
		} else {
			address += target + "/prepaid/balance"
		}
	}
	data, e := fetchJSON(ctx, client, address, key, strings.HasPrefix(p.ID, "glm-"), payload)
	if e != nil {
		return Reading{}, e
	}
	var usage object
	if p.ID == "oneapi" {
		if data["object"] != "billing_subscription" {
			return Reading{}, errFormat
		}
		usage, e = fetchJSON(ctx, client, target+"/dashboard/billing/usage", key, false, nil)
		if e != nil {
			return Reading{}, e
		}
	}
	return parseHTTP(p.ID, data, usage)
}
