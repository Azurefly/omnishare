package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAIQuotaRequiresLocalManagementAndExplicitPeerSharing(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer example-quota-key" {
			http.Error(w, "wrong key", 401)
			return
		}
		fmt.Fprint(w, `{"balance":14.5,"unit":"CNY"}`)
	}))
	defer provider.Close()
	status, packet, _ := wsCall(t, b, "POST", quotaPath+"/accounts", map[string]any{"provider": "balance", "key": "example-quota-key", "alias": "远程演示", "site": provider.URL, "session_only": true}, "")
	if status != 200 {
		t.Fatal(packet)
	}
	id := packet["data"].(map[string]any)["id"].(string)
	url := quotaPath + "?device=" + b.cfg.Get().NodeID
	check := func(want int) {
		t.Helper()
		status, packet, _ := wsCall(t, a, "GET", url, nil, "")
		if status != 200 {
			t.Fatal(packet)
		}
		accounts := packet["data"].(map[string]any)["accounts"].([]any)
		body, _ := json.Marshal(accounts)
		if len(accounts) != want || strings.Contains(string(body), "example-quota-key") || strings.Contains(string(body), provider.URL) {
			t.Fatalf("shared payload count or fields: %d", len(accounts))
		}
	}
	check(0)
	status, _, _ = wsCall(t, b, "PUT", quotaPath+"/accounts/"+id+"/sharing", map[string]bool{"shared": true}, "")
	if status != 200 {
		t.Fatal(status)
	}
	check(1)
	status, _, _ = wsCall(t, b, "GET", quotaPath+"/shared", nil, "")
	if status != 403 {
		t.Fatalf("unsigned read: %d", status)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", quotaPath+"/accounts/"+id+"/refresh", nil)
	req.Host = "127.0.0.1"
	req.RemoteAddr = "192.168.30.20:41000"
	b.s.Handler().ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("remote management: %d", rr.Code)
	}
	status, _, _ = wsCall(t, b, "PUT", quotaPath+"/accounts/"+id+"/sharing", map[string]bool{"shared": false}, "")
	if status != 200 {
		t.Fatal(status)
	}
	check(0)
	status, _, _ = wsCall(t, b, "DELETE", quotaPath+"/accounts/"+id, nil, "")
	if status != 200 {
		t.Fatal(status)
	}
}
