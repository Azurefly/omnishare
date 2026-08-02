package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnishare/internal/model"
)

func TestSecureDefaultsAndHashedSecretPersistence(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, 0, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.Get()
	if cfg.Port != 8081 || cfg.ListenAddress != "127.0.0.1" || cfg.AllowLAN || m.HasAccessKey() {
		t.Fatalf("defaults=%+v", cfg)
	}
	secret := "0123456789abcdef-strong"
	req := cfg
	req.AccessKey = secret
	req.AllowLAN = true
	req.ListenAddress = "0.0.0.0"
	req.Peers = []model.PeerConfig{{Name: "peer", URL: "http://100.64.0.2:8081/"}, {Name: "dup", URL: "http://100.64.0.2:8081"}}
	if err := m.Update(req); err != nil {
		t.Fatal(err)
	}
	if !m.VerifyAccessKey(secret) || m.VerifyAccessKey("wrong-wrong-wrong") {
		t.Fatal("secret verification failed")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), `"access_key":`) {
		t.Fatalf("plaintext secret leaked: %s", data)
	}
	var persisted model.AppConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.AccessKeyHash == "" || persisted.AccessKeySalt == "" || len(persisted.Peers) != 1 {
		t.Fatalf("persisted=%+v", persisted)
	}

	reloaded, err := New(dir, 9090, "override")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Get().Port != 9090 || reloaded.Get().NodeName != "override" || !reloaded.VerifyAccessKey(secret) {
		t.Fatalf("reload=%+v", reloaded.Get())
	}
}

func TestLANRequiresStrongKeyAndPeerValidation(t *testing.T) {
	m, _ := New(t.TempDir(), 0, "node")
	cfg := m.Get()
	cfg.AllowLAN = true
	cfg.ListenAddress = "0.0.0.0"
	cfg.AccessKey = "short"
	if err := m.Update(cfg); err == nil {
		t.Fatal("expected weak key rejection")
	}
	cfg = m.Get()
	cfg.AccessKey = "0123456789abcdef"
	cfg.AllowLAN = true
	cfg.ListenAddress = "0.0.0.0"
	cfg.Peers = []model.PeerConfig{{URL: "file:///etc/passwd"}}
	if err := m.Update(cfg); err == nil {
		t.Fatal("expected unsafe peer rejection")
	}
}

func TestCorruptPrimaryRecoversFromBackup(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, 0, "node")
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.Get()
	cfg.NodeName = "generation-two"
	cfg.AccessKey = "__KEEP__"
	if err := m.Update(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(dir, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Get().NodeName != "node" {
		t.Fatalf("expected backup generation, got %s", recovered.Get().NodeName)
	}
}

func TestLegacyPlaintextMigrationAndRestoreJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"schema_version":1,"data_dir":"` + dir + `","node_name":"legacy","port":8081,"listen_address":"127.0.0.1","max_upload_mb":64,"trash_retention_days":7,"access_key":"0123456789abcdef-legacy"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(dir, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.VerifyAccessKey("0123456789abcdef-legacy") {
		t.Fatal("legacy key was not migrated")
	}
	persisted, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "0123456789abcdef-legacy") || strings.Contains(string(persisted), `"access_key"`) {
		t.Fatalf("legacy plaintext remained: %s", persisted)
	}

	backup, err := m.BackupJSON()
	if err != nil {
		t.Fatal(err)
	}
	var candidate model.AppConfig
	if err := json.Unmarshal(backup, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.NodeName = "restored-node"
	candidate.DataDir = "/must/not/replace/current"
	backup, _ = json.Marshal(candidate)
	if err := m.RestoreJSON(backup); err != nil {
		t.Fatal(err)
	}
	if m.Get().NodeName != "restored-node" || m.Get().DataDir != dir {
		t.Fatalf("restore=%+v", m.Get())
	}
}

func TestSuccessfulKeyCacheIsInvalidatedOnRotation(t *testing.T) {
	m, err := New(t.TempDir(), 0, "node")
	if err != nil {
		t.Fatal(err)
	}
	first := m.Get()
	first.AccessKey = "0123456789abcdef-first"
	if err := m.Update(first); err != nil {
		t.Fatal(err)
	}
	if !m.VerifyAccessKey("0123456789abcdef-first") || !m.VerifyAccessKey("0123456789abcdef-first") {
		t.Fatal("initial key verification failed")
	}
	rotated := m.Get()
	rotated.AccessKey = "0123456789abcdef-second"
	if err := m.Update(rotated); err != nil {
		t.Fatal(err)
	}
	if m.VerifyAccessKey("0123456789abcdef-first") {
		t.Fatal("cached old key remained valid after rotation")
	}
	if !m.VerifyAccessKey("0123456789abcdef-second") {
		t.Fatal("rotated key rejected")
	}
}
