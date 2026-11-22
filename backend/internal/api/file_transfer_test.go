package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"omnishare/internal/model"
	"omnishare/internal/storage"
)

func TestWorkspaceTransferShortcutAndResolveReference(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	a.s.SetStorageState(StorageState{Engine: "folder"})
	b.s.SetStorageState(StorageState{Engine: "folder"})
	wsBind(t, b, a, "")
	path := filepath.Join(t.TempDir(), "项目资料.txt")
	data := []byte("unmodified external content")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	status, packet, _ := wsCall(t, a, "POST", "/api/v1/local-files/shortcuts", map[string]string{"path": path}, "")
	if status != 201 {
		t.Fatalf("add shortcut: %d %v", status, packet)
	}
	object := packet["data"].(map[string]interface{})
	sourceID := object["id"].(string)
	ref := qualifyID(a.cfg.Get().NodeID, sourceID)
	shortcut, err := a.s.store.GetFile(sourceID)
	if err != nil || !shortcutBlob(shortcut.StoragePath) || object["storage_path"] != nil {
		t.Fatalf("bad shortcut or exposed storage location: %v", err)
	}

	finishPath := "/api/v1/files/" + ref + "/finish-transfer"
	status, _, _ = wsCall(t, b, "POST", finishPath, map[string]string{"hash": shortcut.FileHash}, "")
	if status != 400 {
		t.Fatalf("ordinary requests cannot authorize source removal: %d", status)
	}

	move := func() map[string]interface{} {
		t.Helper()
		status, packet, _ := wsCall(t, b, "POST", "/api/v1/file-transfers", map[string]string{"id": ref}, "")
		if status != 200 {
			t.Fatalf("move: %d %v", status, packet)
		}
		return packet["data"].(map[string]interface{})
	}
	for range 2 {
		if result := move(); result["moved"] != true {
			t.Fatalf("transfer did not finalize: %v", result)
		}
	}
	files := b.s.store.AllFiles()
	if len(files) != 1 || files[0].FileName != "项目资料.txt" || shortcutBlob(files[0].StoragePath) {
		t.Fatalf("expected exactly one local managed file: %+v", files)
	}
	if got, err := os.ReadFile(filepath.Join(b.cfg.Get().DataDir, files[0].StoragePath)); err != nil || string(got) != string(data) {
		t.Fatalf("local bytes: %q %v", got, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
		t.Fatalf("external original was removed or modified: %q %v", got, err)
	}
	if _, err := a.s.store.GetFile(sourceID); !errors.Is(err, storage.ErrNotFound) || len(a.s.store.ListTrash("file")) != 0 {
		t.Fatalf("source shortcut is still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.Get().DataDir, shortcut.StoragePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source shortcut artifact still exists: %v", err)
	}
	status, packet, _ = wsCall(t, a, "GET", "/api/v1/file-reference/"+ref, nil, "")
	if status != 200 {
		t.Fatalf("existing reference stopped resolving: %d %v", status, packet)
	}
	resolved := packet["data"].(map[string]interface{})
	if resolved["source_node_id"] != b.cfg.Get().NodeID {
		t.Fatalf("resolved to old device: %v", resolved)
	}
}

func TestWorkspaceDeletedShortcutAndFailedTransfer(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, b, a, "")
	path := filepath.Join(t.TempDir(), "disappears.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, packet, _ := wsCall(t, a, "POST", "/api/v1/local-files/shortcuts", map[string]string{"path": path}, "")
	if status != 201 {
		t.Fatalf("register external file: %d %v", status, packet)
	}
	id := packet["data"].(map[string]interface{})["id"].(string)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	status, packet, _ = wsCall(t, a, "GET", "/api/v1/files/"+id+"/reference", nil, "")
	ref := packet["data"].(map[string]interface{})["file"].(map[string]interface{})
	if status != 200 || ref["source_file_error"] != "源文件已经删除" {
		t.Fatalf("missing source status: %d %v", status, packet)
	}
	status, _, _ = wsCall(t, b, "POST", "/api/v1/file-transfers", map[string]string{"id": qualifyID(a.cfg.Get().NodeID, id)}, "")
	if status != http.StatusConflict || len(a.s.store.AllFiles()) != 1 || len(b.s.store.AllFiles()) != 0 {
		t.Fatal("missing source modified either node")
	}
}

func TestWorkspaceTransferRejectsCorruptedSourceBytes(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, b, a, "")
	asset := model.FileAsset{
		ID: "f_source", FileName: "file.txt", FileSize: 4,
		StoragePath: filepath.Join("uploads", "corrupted.txt"), FileHash: hexDigest([]byte("good")),
	}
	if err := a.s.store.CreateFile(asset); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.cfg.Get().DataDir, asset.StoragePath)
	if err := os.WriteFile(path, []byte("bad!"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, packet, _ := wsCall(t, b, "POST", "/api/v1/file-transfers",
		map[string]string{"id": qualifyID(a.cfg.Get().NodeID, asset.ID)}, "")
	if status != 502 || len(b.s.store.AllFiles()) != 0 {
		t.Fatalf("corrupt transfer was accepted: %d %v", status, packet)
	}
	if _, err := a.s.store.GetFile(asset.ID); err != nil {
		t.Fatalf("source was removed after a failed copy: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source physical file was removed: %v", err)
	}
}
