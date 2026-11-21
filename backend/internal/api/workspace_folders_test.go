package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func uploadMultipartWithFolder(t *testing.T, f *workspaceFixture, name, body, folder string) map[string]interface{} {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	// Put folder after file part to verify order independence.
	if err := mw.WriteField("folder", folder); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, f.http.URL+"/api/v1/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload returned %d", resp.StatusCode)
	}
	var packet map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&packet); err != nil {
		t.Fatal(err)
	}
	return packet["data"].(map[string]interface{})
}

func TestWorkspaceFoldersEndToEndAndLegacyPeerCompatibility(t *testing.T) {
	a, b := newWorkspaceFixture(t, "Mac"), newWorkspaceFixture(t, "Win")
	wsBind(t, a, b, "")

	// 1. Create empty folders on A and B.
	status, pkt, _ := wsCall(t, a, http.MethodPost, "/api/v1/folders", map[string]string{"kind": "notes", "path": "工作/周报"}, "")
	if status != http.StatusCreated {
		t.Fatalf("create folder on A failed: %d %v", status, pkt)
	}
	status, pkt, _ = wsCall(t, b, http.MethodPost, "/api/v1/folders", map[string]string{"kind": "notes", "path": "生活"}, "")
	if status != http.StatusCreated {
		t.Fatalf("create folder on B failed: %d %v", status, pkt)
	}

	// 2. Create notes, files, and pads with folders on both devices.
	_, noteAPkt, _ := wsCall(t, a, http.MethodPost, "/api/v1/notes", map[string]interface{}{"content": "A work", "folder": "工作/周报"}, "")
	noteAID := noteAPkt["data"].(map[string]interface{})["id"].(string)
	_, noteBPkt, _ := wsCall(t, b, http.MethodPost, "/api/v1/notes", map[string]interface{}{"content": "B root"}, "")
	noteBID := qualifyID(b.cfg.Get().NodeID, noteBPkt["data"].(map[string]interface{})["id"].(string))

	fileB := uploadMultipartWithFolder(t, b, "deck.pdf", "pdf-bytes", "工作/汇报")
	fileBID := qualifyID(b.cfg.Get().NodeID, fileB["id"].(string))
	if fileB["folder"] != "工作/汇报" {
		t.Fatalf("expected uploaded file folder 工作/汇报, got %v", fileB["folder"])
	}

	_, padBPkt, _ := wsCall(t, b, http.MethodPost, "/api/v1/pads", map[string]interface{}{"title": "Design", "content": "# Hello", "folder": "文档/设计"}, "")
	padBID := qualifyID(b.cfg.Get().NodeID, padBPkt["data"].(map[string]interface{})["id"].(string))

	// 3. Aggregate merged folders on A (scope=all).
	status, folderPkt, _ := wsCall(t, a, http.MethodGet, "/api/v1/folders?kind=notes&scope=all", nil, "")
	if status != http.StatusOK {
		t.Fatalf("list federated folders failed: %d %v", status, folderPkt)
	}
	folders := folderPkt["data"].([]interface{})
	seenPaths := map[string]bool{}
	for _, entry := range folders {
		seenPaths[entry.(map[string]interface{})["path"].(string)] = true
	}
	for _, want := range []string{"工作", "工作/周报", "生活"} {
		if !seenPaths[want] {
			t.Fatalf("missing federated folder %q in %v", want, folders)
		}
	}

	// 4. Filter notes by folder across workspace.
	_, filteredNotesPkt, _ := wsCall(t, a, http.MethodGet, "/api/v1/notes?scope=all&folder="+url.QueryEscape("工作")+"&recursive=1", nil, "")
	if len(filteredNotesPkt["data"].([]interface{})) != 1 {
		t.Fatalf("expected 1 note in 工作 recursive, got %v", filteredNotesPkt)
	}
	_, exactNotesPkt, _ := wsCall(t, a, http.MethodGet, "/api/v1/notes?scope=all&folder="+url.QueryEscape("工作")+"&recursive=0", nil, "")
	if len(exactNotesPkt["data"].([]interface{})) != 0 {
		t.Fatalf("expected 0 notes directly in 工作, got %v", exactNotesPkt)
	}

	// 5. Rename folder across all bound devices (scope=all).
	status, renamePkt, _ := wsCall(t, a, http.MethodPut, "/api/v1/folders/rename", map[string]string{
		"kind": "notes", "old_path": "工作/周报", "new_path": "工作/2026周报", "scope": "all",
	}, "")
	if status != http.StatusOK || renamePkt["data"].(map[string]interface{})["moved_items"].(float64) != 1 {
		t.Fatalf("federated rename failed: %d %v", status, renamePkt)
	}

	// 6. Bulk move note on A + remote note on B in one call.
	status, bulkPkt, _ := wsCall(t, a, http.MethodPost, "/api/v1/notes/bulk-move", map[string]interface{}{
		"ids":    []string{noteAID, noteBID},
		"folder": "生活",
	}, "")
	if status != http.StatusOK || bulkPkt["data"].(map[string]interface{})["moved"].(float64) != 2 {
		t.Fatalf("bulk-move across devices failed: %d %v", status, bulkPkt)
	}

	// 7. Remote file move + remote pad folder update via existing federated proxy.
	status, fileMovePkt, _ := wsCall(t, a, http.MethodPut, "/api/v1/files/"+fileBID, map[string]string{"folder": "归档"}, "")
	if status != http.StatusOK || fileMovePkt["data"].(map[string]interface{})["folder"] != "归档" {
		t.Fatalf("remote file folder update failed: %d %v", status, fileMovePkt)
	}
	status, padUpdatePkt, _ := wsCall(t, a, http.MethodPut, "/api/v1/pads/"+padBID, map[string]interface{}{
		"title": "Design", "content": "# Hello v2", "folder": "文档/定稿", "version": 1,
	}, "")
	if status != http.StatusOK || padUpdatePkt["data"].(map[string]interface{})["folder"] != "文档/定稿" {
		t.Fatalf("remote pad folder update failed: %d %v", status, padUpdatePkt)
	}

	// 8. Simulate legacy bound peer returning 404 on /api/v1/folders: workspace stays healthy!
	origHandler := b.http.Config.Handler
	b.http.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/folders") {
			http.NotFound(w, r)
			return
		}
		origHandler.ServeHTTP(w, r)
	})
	status, legacyFolderPkt, _ := wsCall(t, a, http.MethodGet, "/api/v1/folders?kind=notes&scope=all", nil, "")
	if status != http.StatusOK || legacyFolderPkt["workspace"].(map[string]interface{})["partial"] != false {
		t.Fatalf("legacy peer 404 on /folders broke workspace: %d %v", status, legacyFolderPkt)
	}
}
