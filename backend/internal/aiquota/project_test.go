package aiquota

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProjectQuotaRequestsAreBoundToTheirProvider(t *testing.T) {
	const team = "00000000-1111-2222-3333-444444444444"
	tests := []struct {
		id, target, method, address, response, label, unit string
		value                                              float64
	}{
		{"gemini", "quota-demo", "POST", "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota", `{"buckets":[{"modelId":"demo","remainingFraction":0,"resetTime":"2030-01-01T01:00:00Z"}]}`, "demo", "%", 0},
		{"xai", team, "GET", "https://management-api.x.ai/v1/billing/teams/" + team + "/prepaid/balance", `{"total":{"val":"-1234"},"changes":[{"private":"not shared"}]}`, "团队预付账面余额", "USD", -12.34},
	}
	for _, c := range tests {
		t.Run(c.id, func(t *testing.T) {
			pr, _ := findProvider(c.id)
			calls := 0
			client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != c.method || req.URL.String() != c.address || req.Header.Get("Authorization") != "Bearer isolated-key" {
					t.Fatal("provider destination")
				}
				if c.id == "gemini" {
					o, e := readObject(req.Body)
					if e != nil || o["project"] != c.target || len(o) != 1 {
						t.Fatal("expected quota-only payload")
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(c.response)), Header: make(http.Header)}, nil
			})}
			r, e := query(context.Background(), client, pr, c.target, "isolated-key")
			if e != nil {
				t.Fatal(e)
			}
			assertReading(t, r, c.label, c.unit, c.value)
			for _, target := range []string{"", "../outside", "https://other.invalid", "abc?token=private"} {
				if _, e := query(context.Background(), client, pr, target, "isolated-key"); e == nil {
					t.Fatalf("accepted target %q", target)
				}
			}
			if calls != 1 {
				t.Fatalf("unexpected external requests: %d", calls)
			}
		})
	}
	p := Open(t.TempDir(), "node")
	p.read = func(_ context.Context, _ *http.Client, provider Provider, target, key string) (Reading, error) {
		if provider.ID != "gemini" || target != "quota-demo" {
			return Reading{}, fmt.Errorf("unexpected target")
		}
		return parseHTTP("gemini", document(t, tests[0].response), nil)
	}
	x, e := p.Create(context.Background(), Input{Provider: "gemini", Resource: "quota-demo", Key: "isolated-key"})
	if e != nil || !x.SessionOnly {
		t.Fatal("short-lived OAuth credentials must remain in the session", e)
	}
}
