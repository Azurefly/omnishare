package aiquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func document(t *testing.T, raw string) object {
	t.Helper()
	v, e := readObject(strings.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func assertReading(t *testing.T, r Reading, label, unit string, value float64) {
	t.Helper()
	for _, m := range r.Meters {
		if m.Label == label && m.Unit == unit && m.Value != nil && *m.Value == value {
			return
		}
	}
	t.Fatalf("missing %s %v %s in %+v", label, value, unit, r.Meters)
}
func TestProviderResponses(t *testing.T) {
	for _, c := range []struct {
		id, raw, label, unit string
		value                float64
	}{
		{"deepseek", `{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0.00","topped_up_balance":"4","granted_balance":"1"}]}`, "可用余额", "CNY", 0},
		{"moonshot-cn", `{"code":0,"status":true,"data":{"available_balance":5,"cash_balance":-1}}`, "现金余额", "CNY", -1},
		{"moonshot-global", `{"data":{"available_balance":3}}`, "可用余额", "USD", 3},
		{"openrouter", `{"data":{"limit":100,"limit_remaining":0,"usage_monthly":1.25}}`, "密钥剩余额度", "USD", 0},
		{"openrouter", `{"data":{"limit":100,"usage":10}}`, "密钥支出上限", "USD", 100},
		{"opencode", `{"usage":{"rolling":{"percent":5.5},"weekly":{"percent":100,"resetsAt":"2032-01-01T12:00:00Z"}}}`, "本周", "%", 0},
		{"glm-cn", `{"code":200,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":24,"nextResetTime":1900000000000},{"type":"TIME_LIMIT","percentage":0}]}}`, "5 小时", "%", 76},
		{"glm-global", `{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":"15"}]}`, "7 天", "%", 85},
		{"minimax-cn", `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"other","current_interval_remaining_percent":12},{"model_name":"general","current_interval_total_count":200,"current_interval_usage_count":40}]}`, "当前周期", "%", 20},
		{"minimax-global", `{"model_remains":[{"model_name":"general","current_weekly_remaining_percent":40,"weekly_boost_permille":1500}]}`, "本周", "%", 60},
		{"kimi", `{"usages":{"limit_5h":{"used_ratio":0.25},"limit_7d":{"used_ratio":0}}}`, "5 小时", "%", 75},
		{"kimi", `{"usage":{"limit":"100","remaining":"50"},"limits":[{"window":{"timeUnit":"TIME_UNIT_MINUTE","duration":300},"detail":{"limit":"20","used":"5"}}]}`, "5 小时", "%", 75},
		{"newapi", `{"code":true,"data":{"object":"token_usage","unit":"quota","unlimited_quota":false,"total_available":50}}`, "令牌余额", "站点配额", 50},
		{"oneapi", `{"object":"billing_subscription","hard_limit_usd":5}`, "计费余额", "站点单位", 3.75},
		{"sub2api", `{"mode":"unrestricted","unit":"USD","subscription":{"weekly_limit_usd":20,"weekly_usage_usd":12}}`, "每周", "USD", 8},
		{"sub2api", `{"mode":"quota_limited","quota":{"unit":"USD","remaining":12},"rate_limits":[{"window":"5h","remaining":9,"unit":"USD"}]}`, "5 小时", "USD", 9},
		{"sub2api", `{"isValid":true,"unit":"USD","remaining":3}`, "令牌余额", "USD", 3},
		{"balance", `{"balance":"0.001","unit":"EUR"}`, "账户余额", "EUR", 0.001},
		{"balance", `{"balance":15}`, "账户余额", "站点单位", 15},
	} {
		t.Run(c.id, func(t *testing.T) {
			r, e := parseHTTP(c.id, document(t, c.raw), document(t, `{"object":"list","total_usage":125}`))
			if e != nil {
				t.Fatal(e)
			}
			assertReading(t, r, c.label, c.unit, c.value)
		})
	}
	for _, c := range [][2]string{{"deepseek", `{"balance_infos":[{"currency":"USD"}]}`}, {"minimax-cn", `{"model_remains":[{"model_name":"general","current_interval_total_count":0}]}`}, {"kimi", "{}"}, {"glm-cn", `{"code":401,"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":5}]}}`}, {"balance", `{"error":{},"balance":0}`}, {"opencode", `{"usage":{"weekly":{"percent":-1}}}`}} {
		if _, e := parseHTTP(c[0], document(t, c[1]), nil); e == nil {
			t.Errorf("accepted unknown or invalid %s", c[0])
		}
	}
	r, e := parseHTTP("newapi", document(t, `{"object":"token_usage","unlimited_quota":true}`), nil)
	if e != nil || !r.Meters[0].Unlimited || r.Meters[0].Value != nil {
		t.Fatal(r, e)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOfficialEndpointsAndPrivacy(t *testing.T) {
	p, _ := findProvider("glm-cn")
	for _, id := range []string{"glm-cn", "deepseek", "kimi", "opencode", "moonshot-global", "minimax-cn", "openrouter"} {
		p, _ = findProvider(id)
		called := false
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			called = true
			auth := "Bearer fixture-key"
			if id == "glm-cn" {
				auth = "fixture-key"
			}
			if r.Method != "GET" || r.URL.String() != p.endpoint || r.URL.Scheme != "https" || r.Header.Get("Authorization") != auth {
				t.Fatal("unexpected direct request", id)
			}
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private response"))}, nil
		})}
		_, e := query(context.Background(), client, p, "", "fixture-key")
		if !called || e == nil || strings.Contains(e.Error(), "private") || strings.Contains(e.Error(), "fixture-key") {
			t.Fatal(id, e)
		}
	}
	for _, bad := range []string{"http://lan.example", "https://user:pwd@site.invalid", "https://example.org/?key=s", "file:///etc/config"} {
		if _, e := normalizeSite(bad); e == nil {
			t.Fatal(bad)
		}
	}
	for _, good := range []string{"http://127.0.0.1:8100", "http://[::1]:8100", "https://example.org/api"} {
		if _, e := normalizeSite(good); e != nil {
			t.Fatal(good)
		}
	}
	dst := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dst.URL, http.StatusFound) }))
	defer src.Close()
	manager := Open(t.TempDir(), "node")
	gw, _ := findProvider("balance")
	_, e := query(context.Background(), manager.client, gw, src.URL, "test-only")
	var pe *ProviderError
	if !errors.As(e, &pe) || pe.Status != 302 {
		t.Fatal(e)
	}
	for _, input := range []string{"null", "{} {}", "[1]", "{\"x\": " + strings.Repeat("0", maxResponse) + "}"} {
		if _, e := readObject(strings.NewReader(input)); e == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}
func TestAccountLifecycleAndCredentialScope(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	m := Open(dir, "node-A")
	var available atomic.Bool
	available.Store(true)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" || r.Header.Get("Authorization") != "Bearer test-private-value" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !available.Load() {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "sensitive body", http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"balance":7,"unit":"CNY"}`)
	}))
	defer gateway.Close()
	in := Input{Provider: "balance", Alias: "工作", Site: gateway.URL, Key: "test-private-value"}
	a, e := m.Create(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Create(context.Background(), in); e == nil {
		t.Fatal("duplicate")
	}
	if x, _ := m.List(true); len(x) != 0 {
		t.Fatal("default share")
	}
	if e = m.SetShared(a.ID, true); e != nil {
		t.Fatal(e)
	}
	saved, e := os.ReadFile(m.path)
	if e != nil || bytes.Contains(saved, []byte(in.Key)) {
		t.Fatal("cleartext key")
	}
	m = Open(dir, "node-A")
	accounts, e := m.List(true)
	if e != nil || len(accounts) != 1 {
		t.Fatal(accounts, e)
	}
	v, _ := json.Marshal(accounts)
	if bytes.Contains(v, []byte(gateway.URL)) || bytes.Contains(v, []byte(in.Key)) {
		t.Fatal("private configuration in shared reading")
	}
	available.Store(false)
	last, e := m.Refresh(context.Background(), a.ID)
	if e == nil || last.Reading == nil || last.Reading.At != a.Reading.At || last.Problem == "" {
		t.Fatal(last, e)
	}
	if _, e = m.Refresh(context.Background(), a.ID); e == nil || !strings.Contains(e.Error(), "冷却") {
		t.Fatal(e)
	}
	available.Store(true)
	in.SessionOnly = true
	in.Alias = "临时"
	temp, e := m.Create(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if v, _ := Open(dir, "node-A").List(false); len(v) != 1 {
		t.Fatal("session stored")
	}
	originalPath := m.path
	m.path = filepath.Join(originalPath, "unwritable")
	if e = m.Remove(a.ID); e == nil {
		t.Fatal("expected saving the removal intent to fail")
	}
	m.path = originalPath
	if key, err := keyring.Get(m.service, secretRef(m.accounts[a.ID])); err != nil || key != in.Key {
		t.Fatal("failed metadata write lost the credential")
	}
	if current := m.accounts[a.ID]; current.Removing || !current.Shared {
		t.Fatal("failed metadata write changed the account")
	}
	for _, id := range []string{temp.ID, a.ID} {
		if e = m.Remove(id); e != nil {
			t.Fatal(e)
		}
		if e = m.Remove(id); e != nil {
			t.Fatal(e)
		}
	}
	if x, _ := m.List(false); len(x) != 0 {
		t.Fatal("not removed")
	}
	if _, e = keyring.Get(m.service, secretRef(savedAccount{Account: a, Site: gateway.URL})); !errors.Is(e, keyring.ErrNotFound) {
		t.Fatal("key not forgotten")
	}
}
func TestLocalSourcesAndChangedAccount(t *testing.T) {
	if !agySupportsReport("Antigravity CLI 1.1.11") || agySupportsReport("1.1.10") || agySupportsReport("1.2.0-beta") {
		t.Fatal("CLI gate")
	}
	report := document(t, `{"status":"SUCCESS","num_turns":0,"usage":{"total_tokens":0},"command":{"name":"credits","data":{"remaining_credits":0}}}`)
	if agyData(report, "credits") == nil || agyData(report, "usage") != nil {
		t.Fatal("command mismatch")
	}
	report["num_turns"] = float64(1)
	if agyData(report, "credits") != nil {
		t.Fatal("model response")
	}
	r, e := parseKiroReport("Monthly credits used: 20 of 100\nCredits remaining: 80\nSession credits used: 1.25\nBonus credits left: -2")
	if e != nil {
		t.Fatal(e)
	}
	assertReading(t, r, "账户剩余", "积分", 80)
	assertReading(t, r, "奖励剩余", "积分", -2)
	var c Reading
	codexGroup(&c, document(t, `{"primary":{"usedPercent":0,"windowDurationMins":300,"resetsAt":2000000000},"secondary":{"usedPercent":100,"windowDurationMins":10080}}`), "")
	assertReading(t, c, "5 小时", "%", 100)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if e = os.WriteFile(cfg, []byte(`{"lastKnownAccountUuid":"fixture-id"}`), 0600); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	if e = os.Chtimes(cfg, now.Add(-time.Hour), now.Add(-time.Hour)); e != nil {
		t.Fatal(e)
	}
	raw := fmt.Sprintf(`{"version":2,"samples":[{"t":%d,"org":"x","u":{"fh":20,"sd":5}}]}`, now.UnixMilli())
	if e = os.WriteFile(filepath.Join(dir, "plan-usage-history.json"), []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	r, e = claudeRecord(dir)
	if e != nil {
		t.Fatal(e)
	}
	assertReading(t, r, "5 小时", "%", 80)
	if e = os.Chtimes(cfg, now.Add(time.Second), now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, e = claudeRecord(dir); e == nil {
		t.Fatal("previous login accepted")
	}
	manager := Open(dir, "n")
	reading := Reading{At: now.Unix(), Meters: c.Meters, owner: "first"}
	manager.read = func(context.Context, *http.Client, Provider, string, string) (Reading, error) { return reading, nil }
	a, e := manager.Create(context.Background(), Input{Provider: "codex"})
	if e != nil {
		t.Fatal(e)
	}
	if e = manager.SetShared(a.ID, true); e != nil {
		t.Fatal(e)
	}
	reading.owner = "second"
	a, e = manager.Refresh(context.Background(), a.ID)
	if !errors.Is(e, errChanged) || a.Shared || a.Reading != nil {
		t.Fatal("CLI account change must disconnect sharing")
	}
}
