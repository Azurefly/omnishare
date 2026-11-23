package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicDeliveryListDefaultsToEmptyArray(t *testing.T) {
	secret := strings.Repeat("a", 64)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/deliveries" ||
			r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("X-OmniShare-Publisher") != "test-owner" {
			t.Error("invalid management request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(gateway.Close)
	local := newWorkspaceFixture(t, "local")
	status, _, _ := wsCall(t, local, http.MethodPut, "/api/v1/delivery/config", deliveryConfig{
		PublicOrigin: "https://example.test", Management: gateway.URL, Publisher: "test-owner", Token: secret,
	}, "")
	if status != http.StatusOK {
		t.Fatalf("save delivery configuration: %d", status)
	}
	status, reply, _ := wsCall(t, local, http.MethodGet, "/api/v1/delivery/items", nil, "")
	items, ok := reply["data"].([]interface{})
	if status != http.StatusOK || !ok || len(items) != 0 {
		t.Fatalf("empty delivery list: status %d, data %v", status, reply["data"])
	}
}

func TestPublicDeliveryConfigUsesHTTPSOriginByDefault(t *testing.T) {
	local := newWorkspaceFixture(t, "local")
	input := deliveryConfig{PublicOrigin: "https://example.test:8443/", Publisher: "test-owner", Token: strings.Repeat("b", 64)}
	path := "/api/v1/delivery/config"
	status, _, _ := wsCall(t, local, http.MethodPut, path, input, "")
	if status != http.StatusOK {
		t.Fatalf("save same-origin configuration: %d", status)
	}
	saved, err := local.s.readDeliveryConfig()
	if err != nil || saved.PublicOrigin != "https://example.test:8443" || saved.Management != saved.PublicOrigin {
		t.Fatal("the management endpoint must use the normalized HTTPS origin")
	}
	input.Token = ""
	status, response, _ := wsCall(t, local, http.MethodPut, path, input, "")
	if status != http.StatusOK {
		t.Fatalf("unchanged settings must reuse the credential: %d", status)
	}
	data := response["data"].(map[string]interface{})
	settings := data["settings"].(map[string]interface{})
	if settings["token"] != nil {
		t.Fatal("the publisher credential must not be returned")
	}
	input.PublicOrigin = "https://another.example.test"
	status, _, _ = wsCall(t, local, http.MethodPut, path, input, "")
	if status != http.StatusBadRequest {
		t.Fatal("changing the server must require explicit re-entry of the credential")
	}
}

func TestPublicDeliveryConfigVerifiesBeforeSaving(t *testing.T) {
	secret := strings.Repeat("c", 64)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/deliveries" {
			t.Error("verification must only read the delivery list")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(gateway.Close)
	local := newWorkspaceFixture(t, "local")
	path := "/api/v1/delivery/config?verify=1"
	input := deliveryConfig{PublicOrigin: "https://example.test", Management: gateway.URL, Publisher: "test-owner", Token: secret}
	status, _, _ := wsCall(t, local, http.MethodPut, path, input, "")
	if status != http.StatusOK {
		t.Fatalf("save verified configuration: %d", status)
	}
	input.Token = strings.Repeat("d", 64)
	status, _, _ = wsCall(t, local, http.MethodPut, path, input, "")
	saved, err := local.s.readDeliveryConfig()
	if status != http.StatusUnprocessableEntity || err != nil || saved.Token != secret {
		t.Fatal("verification failure must preserve the previous credential")
	}
}
