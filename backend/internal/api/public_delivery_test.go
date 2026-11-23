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
