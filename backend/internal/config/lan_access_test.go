package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptionalProtectionAcceptsExactArbitraryKeys(t *testing.T) {
	for _, key := range []string{"1", "a", "中", "🔑", " ", " x ", "CLEAR", "__KEEP__", strings.Repeat("长", 1024)} {
		t.Run(key[:1], func(t *testing.T) {
			m, err := New(t.TempDir(), 0, "LAN")
			if err != nil {
				t.Fatal(err)
			}
			req := m.Get()
			req.AccessKeyEnabled = boolValue(true)
			req.AccessKey = key
			if err := m.Update(req); err != nil {
				t.Fatalf("arbitrary key rejected: %v", err)
			}
			if !m.AccessKeyEnabled() || !m.VerifyAccessKey(key) || m.VerifyAccessKey(key+"wrong") {
				t.Fatal("exact key matching failed")
			}
			req = m.Get()
			req.AccessKeyEnabled = boolValue(false)
			req.KeepAccessKey = true
			if err := m.Update(req); err != nil {
				t.Fatal(err)
			}
			if !m.VerifyAccessKey("") || !m.VerifyAccessKey("stale-key") || !m.HasAccessKey() {
				t.Fatal("disabled key still required or stored key lost")
			}
			m, err = New(m.Get().DataDir, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			if m.AccessKeyEnabled() || !m.Get().AllowLAN {
				t.Fatal("switches not persisted")
			}
			req = m.Get()
			req.AccessKeyEnabled = boolValue(true)
			req.KeepAccessKey = true
			if err := m.Update(req); err != nil {
				t.Fatal(err)
			}
			if !m.VerifyAccessKey(key) || m.VerifyAccessKey("wrong") {
				t.Fatal("re-enabled key not preserved")
			}
		})
	}
}

func TestEmptyEnabledKeyRejectedAtomicallyAndFlagCannotMutateManager(t *testing.T) {
	m, _ := New(t.TempDir(), 0, "LAN")
	req := m.Get()
	req.AccessKeyEnabled = boolValue(true)
	if err := m.Update(req); err == nil {
		t.Fatal("enabled protection without any key accepted")
	}
	if m.AccessKeyEnabled() {
		t.Fatal("failed update changed configuration")
	}
	req = m.Get()
	*req.AccessKeyEnabled = true
	if m.AccessKeyEnabled() {
		t.Fatal("caller modified manager through a pointer")
	}
}

func TestLegacyProtectedConfigurationMigratesWithoutDisablingIt(t *testing.T) {
	dir := t.TempDir()
	m, _ := New(dir, 0, "legacy")
	req := m.Get()
	req.AccessKey = "old"
	req.AccessKeyEnabled = boolValue(true)
	if err := m.Update(req); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	var old map[string]any
	_ = json.Unmarshal(raw, &old)
	delete(old, "access_key_enabled")
	old["schema_version"] = 2
	raw, _ = json.Marshal(old)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), raw, 0600)
	restored, err := New(dir, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !restored.AccessKeyEnabled() || !restored.VerifyAccessKey("old") {
		t.Fatal("legacy protection not retained")
	}
}

func TestLANCanBeToggledWithoutSettingAnyKey(t *testing.T) {
	m, _ := New(t.TempDir(), 0, "LAN")
	req := m.Get()
	req.AllowLAN = false
	req.KeepAccessKey = true
	if err := m.Update(req); err != nil {
		t.Fatal(err)
	}
	if m.Get().ListenAddress != "127.0.0.1" {
		t.Fatal("local mode did not select loopback")
	}
	req = m.Get()
	req.AllowLAN = true
	req.KeepAccessKey = true
	if err := m.Update(req); err != nil {
		t.Fatal(err)
	}
	if m.Get().ListenAddress != "0.0.0.0" || m.AccessKeyEnabled() {
		t.Fatal("LAN mode still requires a key")
	}
}
