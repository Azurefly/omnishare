package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"omnishare/internal/model"
	"omnishare/internal/storage"
)

func backupWithInvalidConfig(t *testing.T, archive []byte) []byte {
	t.Helper()
	var cfg map[string]interface{}
	if err := json.Unmarshal(zipEntry(t, archive, "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	// This is checked during the final config replacement, after the storage
	// and uploads directory have already been swapped.
	cfg["discovery_private_key"] = "invalid-discovery-key"
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var manifest model.BackupManifest
	if err := json.Unmarshal(zipEntry(t, archive, "backup-manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ConfigSHA256 = hexDigest(configJSON)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archive = rewriteZipEntry(t, archive, "config.json", configJSON)
	return rewriteZipEntry(t, archive, "backup-manifest.json", manifestJSON)
}

func TestRestoreRollbackPreservesWritesBeforeSwap(t *testing.T) {
	for _, engine := range []string{"json", "sqlite"} {
		t.Run(engine, func(t *testing.T) {
			s, h, cfg := newTestServer(t)
			if engine == "sqlite" {
				store, err := storage.NewSQLite(cfg.Get().DataDir)
				if err != nil {
					t.Fatal(err)
				}
				s.store = store
				t.Cleanup(func() { _ = store.Close() })
			}
			if w := upload(t, h, "original.txt", []byte("original"), ""); w.Code != http.StatusCreated {
				t.Fatalf("seed upload: %d %s", w.Code, w.Body.String())
			}
			backup := downloadBackup(t, h, "")
			if backup.Code != http.StatusOK {
				t.Fatalf("backup: %d %s", backup.Code, backup.Body.String())
			}
			archive := backupWithInvalidConfig(t, backup.Body.Bytes())
			var addedFile model.FileAsset
			var addedNote model.QuickNote
			s.restorePreSwapHook = func() {
				w := upload(t, h, "arrived-during-restore.txt", []byte("must survive rollback"), "")
				if w.Code != http.StatusCreated {
					t.Fatalf("concurrent upload: %d %s", w.Code, w.Body.String())
				}
				addedFile = decodeData[model.FileAsset](t, w)
				w = request(t, h, http.MethodPost, "/api/v1/notes", bytes.NewBufferString(`{"content":"new note during restore"}`), "", "application/json")
				if w.Code != http.StatusCreated {
					t.Fatalf("concurrent note: %d %s", w.Code, w.Body.String())
				}
				addedNote = decodeData[model.QuickNote](t, w)
			}
			if w := postRestore(t, h, archive); w.Code != http.StatusConflict {
				t.Fatalf("invalid restore: %d %s", w.Code, w.Body.String())
			}
			w := request(t, h, http.MethodGet, "/api/v1/files/"+addedFile.ID+"/download", nil, "", "")
			if w.Code != http.StatusOK || w.Body.String() != "must survive rollback" {
				t.Errorf("rollback lost successful concurrent upload: %d %s", w.Code, w.Body.String())
			}
			w = request(t, h, http.MethodGet, "/api/v1/notes/"+addedNote.ID+"/manage", nil, "", "")
			if w.Code != http.StatusOK {
				t.Errorf("rollback lost successful concurrent note: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
