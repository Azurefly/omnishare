package aiquota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Copies of the test executable implement only the three quota protocols.
// The tests never invoke an installed client or open a real auth profile.
func TestMain(m *testing.M) {
	if os.Getenv("OMNISHARE_FAKE_QUOTA_CLIENT") == "1" {
		switch strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") {
		case "codex":
			os.Exit(fakeCodex())
		case "agy":
			if len(os.Args) == 2 && os.Args[1] == "--version" {
				fmt.Println("1.1.19")
				os.Exit(0)
			}
			args := strings.Join(os.Args[1:], " ")
			switch args {
			case "--disable-slash-commands=false -p /usage --output-format json":
				fmt.Println(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"name":"Gemini","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.2}]}]}}}`)
			case "--disable-slash-commands=false -p /credits --output-format json":
				fmt.Println(`{"status":"SUCCESS","num_turns":0,"command":{"name":"credits","data":{"remaining_credits":15}}}`)
			default:
				os.Exit(3)
			}
			os.Exit(0)
		case "kiro-cli":
			switch strings.Join(os.Args[1:], " ") {
			case "--version":
				fmt.Println("kiro-cli 3.3.0")
			case "chat --no-interactive /usage":
				fmt.Println("Monthly credits used: 25 / 100\nCredits remaining: 75")
			default:
				os.Exit(3)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
func fakeCodex() int {
	d := json.NewDecoder(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	if strings.Join(os.Args[1:], " ") != "app-server --listen stdio:// -c analytics.enabled=false" {
		return 4
	}
	for {
		var req object
		if e := d.Decode(&req); e == io.EOF {
			return 0
		} else if e != nil {
			return 2
		}
		var result any
		switch req["method"] {
		case "initialize":
			result = object{"userAgent": "fixture"}
		case "initialized":
			continue
		case "account/read":
			result = object{"account": object{"type": "chatgpt", "email": "fixture@example.invalid"}}
		case "account/rateLimits/read":
			result = object{"rateLimitsByLimitId": object{"codex": object{"limitName": "Codex", "primary": object{"usedPercent": 40, "windowDurationMins": 300}}}}
		default:
			return 3
		}
		if out.Encode(object{"id": req["id"], "result": result}) != nil {
			return 2
		}
	}
}
func TestDirectNativeClientProtocols(t *testing.T) {
	dir := t.TempDir()
	bin, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	body, e := os.ReadFile(bin)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"codex", "agy", "kiro-cli"} {
		target := filepath.Join(dir, name+ext)
		if e = os.WriteFile(target, body, 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("ANTIGRAVITY_HOME", dir)
	t.Setenv("OMNISHARE_FAKE_QUOTA_CLIENT", "1")
	for _, c := range []struct {
		id, label, unit string
		value           float64
	}{{"codex", "Codex · 5 小时", "%", 60}, {"antigravity", "可用积分", "积分", 15}, {"kiro", "账户剩余", "积分", 75}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		r, e := queryLocal(ctx, c.id)
		cancel()
		if e != nil {
			t.Fatal(c.id, e)
		}
		assertReading(t, r, c.label, c.unit, c.value)
	}
	if e = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"disableSlashCommands":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = queryLocal(context.Background(), "antigravity"); e == nil {
		t.Fatal("disabled slash commands ignored")
	}
}
