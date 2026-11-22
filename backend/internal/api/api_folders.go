package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"omnishare/internal/model"
	"omnishare/internal/storage"
)

func parseFolderQuery(r *http.Request) (folder string, recursive bool, err error) {
	raw := strings.TrimSpace(r.URL.Query().Get("folder"))
	recRaw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("recursive")))
	recursive = recRaw != "0" && recRaw != "false"
	if raw == "" || raw == storage.FolderRootFilter {
		return raw, recursive, nil
	}
	norm, normErr := storage.NormalizeFolderPath(raw)
	if normErr != nil {
		return "", false, normErr
	}
	return norm, recursive, nil
}

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request) {
	kind, err := storage.ValidFolderKind(r.URL.Query().Get("kind"))
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	items, err := s.store.ListFolders(kind)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(items)))
	respond(w, http.StatusOK, 0, "success", items)
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		NodeID string `json:"node_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	kind, err := storage.ValidFolderKind(req.Kind)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	path, err := storage.NormalizeFolderPath(req.Path)
	if err != nil || path == "" {
		if err == nil {
			err = errors.New("folder path is required")
		}
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	nodeID := strings.TrimSpace(req.NodeID)
	localID := s.cfg.Get().NodeID
	if nodeID != "" && nodeID != localID {
		payload, _ := json.Marshal(map[string]string{"kind": kind, "path": path})
		s.proxyObject(w, r, nodeID, "/api/v1/folders", payload)
		return
	}
	if err := s.store.EnsureFolder(kind, path); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	s.invalidateWorkspaceCache(localID)
	respond(w, http.StatusCreated, 0, "created", model.FolderNode{Kind: kind, Path: path})
}

func (s *Server) renameFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
		Scope   string `json:"scope"`
		NodeID  string `json:"node_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	kind, err := storage.ValidFolderKind(req.Kind)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	oldPath, err := storage.NormalizeFolderPath(req.OldPath)
	if err != nil || oldPath == "" {
		respond(w, http.StatusBadRequest, 400, "valid source folder path is required", nil)
		return
	}
	newPath, err := storage.NormalizeFolderPath(req.NewPath)
	if err != nil || newPath == "" {
		respond(w, http.StatusBadRequest, 400, "valid target folder path is required", nil)
		return
	}

	localID := s.cfg.Get().NodeID
	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID != "" && nodeID != localID {
		payload, _ := json.Marshal(map[string]string{"kind": kind, "old_path": oldPath, "new_path": newPath, "scope": "local"})
		s.proxyObject(w, r, nodeID, "/api/v1/folders/rename", payload)
		return
	}

	moved, err := s.store.RenameFolder(kind, oldPath, newPath)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	s.invalidateWorkspaceCache(localID)

	peerErrors := []string{}
	if req.Scope == "all" {
		payload, _ := json.Marshal(map[string]string{"kind": kind, "old_path": oldPath, "new_path": newPath, "scope": "local"})
		extraMoved, errs := s.fanoutPeerMutation(r.Context(), http.MethodPut, "/api/v1/folders/rename", payload)
		moved += extraMoved
		peerErrors = errs
	}
	respond(w, http.StatusOK, 0, "renamed", map[string]interface{}{
		"kind":        kind,
		"old_path":    oldPath,
		"new_path":    newPath,
		"moved_items": moved,
		"partial":     len(peerErrors) > 0,
		"errors":      peerErrors,
	})
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		Path   string `json:"path"`
		Mode   string `json:"mode"`
		Scope  string `json:"scope"`
		NodeID string `json:"node_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	kind, err := storage.ValidFolderKind(req.Kind)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	path, err := storage.NormalizeFolderPath(req.Path)
	if err != nil || path == "" {
		respond(w, http.StatusBadRequest, 400, "valid folder path is required", nil)
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "move_to_root"
	}

	localID := s.cfg.Get().NodeID
	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID != "" && nodeID != localID {
		payload, _ := json.Marshal(map[string]string{"kind": kind, "path": path, "mode": mode, "scope": "local"})
		s.proxyObject(w, r, nodeID, "/api/v1/folders/delete", payload)
		return
	}

	if kind == "files" && mode == "trash_items" {
		s.fileStorageMu.Lock()
		defer s.fileStorageMu.Unlock()
		for _, file := range s.store.ListFilesInFolder("", "", path, true) {
			if _, busy := s.activeFileTransfers.Load(file.ID); busy {
				respond(w, 409, 409, "目录中有正在转移的文件，请稍后再删除", nil)
				return
			}
		}
	}
	affected, err := s.store.DeleteFolder(kind, path, mode)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	s.invalidateWorkspaceCache(localID)

	peerErrors := []string{}
	if req.Scope == "all" {
		payload, _ := json.Marshal(map[string]string{"kind": kind, "path": path, "mode": mode, "scope": "local"})
		extraAffected, errs := s.fanoutPeerMutation(r.Context(), http.MethodPost, "/api/v1/folders/delete", payload)
		affected += extraAffected
		peerErrors = errs
	}
	respond(w, http.StatusOK, 0, "deleted", map[string]interface{}{
		"kind":           kind,
		"path":           path,
		"mode":           mode,
		"affected_items": affected,
		"partial":        len(peerErrors) > 0,
		"errors":         peerErrors,
	})
}

func (s *Server) fanoutPeerMutation(parent context.Context, method, path string, payload []byte) (int, []string) {
	peers := s.boundPeers()
	if len(peers) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	total := 0
	errs := []string{}
	for _, p := range peers {
		wg.Add(1)
		go func(peer peerBinding) {
			defer wg.Done()
			s.invalidateWorkspaceCache(peer.Identity.ID)
			resp, err := s.peerRequest(ctx, peer, method, path, payload, http.Header{"Content-Type": []string{"application/json"}})
			s.invalidateWorkspaceCache(peer.Identity.ID)
			if err != nil {
				mu.Lock()
				errs = append(errs, peer.Identity.Name+": "+peerError(err))
				mu.Unlock()
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				// Bound peer runs an older build without folder support; keep partial marker explicit.
				mu.Lock()
				errs = append(errs, peer.Identity.Name+": 对端版本较旧，不支持目录同步")
				mu.Unlock()
				return
			}
			var packet struct {
				Code    int                    `json:"code"`
				Message string                 `json:"message"`
				Data    map[string]interface{} `json:"data"`
			}
			if resp.StatusCode >= 300 || json.NewDecoder(resp.Body).Decode(&packet) != nil || packet.Code != 0 {
				mu.Lock()
				errs = append(errs, peer.Identity.Name+": 目录同步失败")
				mu.Unlock()
				return
			}
			count := 0
			if v, ok := packet.Data["moved_items"].(float64); ok {
				count = int(v)
			} else if v, ok := packet.Data["affected_items"].(float64); ok {
				count = int(v)
			}
			mu.Lock()
			total += count
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return total, errs
}

func (s *Server) bulkMoveNotes(w http.ResponseWriter, r *http.Request) {
	s.handleBulkMove(w, r, storage.FolderKindNotes)
}

func (s *Server) bulkMoveFiles(w http.ResponseWriter, r *http.Request) {
	s.handleBulkMove(w, r, storage.FolderKindFiles)
}

func (s *Server) bulkMovePads(w http.ResponseWriter, r *http.Request) {
	s.handleBulkMove(w, r, storage.FolderKindPads)
}

func (s *Server) handleBulkMove(w http.ResponseWriter, r *http.Request, kind string) {
	var req struct {
		IDs    []string `json:"ids"`
		Folder string   `json:"folder"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 200 {
		respond(w, http.StatusBadRequest, 400, "ids must contain between 1 and 200 entries", nil)
		return
	}
	folder, err := storage.NormalizeFolderPath(req.Folder)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}

	localIDs := []string{}
	byPeer := map[string][]string{}
	localNode := s.cfg.Get().NodeID
	for _, rawID := range req.IDs {
		node, id := splitObject(strings.TrimSpace(rawID))
		if id == "" {
			continue
		}
		if node == "" || node == localNode {
			localIDs = append(localIDs, id)
		} else {
			byPeer[node] = append(byPeer[node], id)
		}
	}

	moved := 0
	if len(localIDs) > 0 {
		n, moveErr := s.store.MoveItems(kind, localIDs, folder)
		if moveErr != nil {
			respond(w, http.StatusBadRequest, 400, moveErr.Error(), nil)
			return
		}
		moved += n
		s.invalidateWorkspaceCache(localNode)
	}

	peerErrors := []string{}
	for peerID, ids := range byPeer {
		peer, ok := s.boundPeer(peerID)
		if !ok {
			peerErrors = append(peerErrors, peerID+": 设备未绑定")
			continue
		}
		s.invalidateWorkspaceCache(peerID)
		payload, _ := json.Marshal(map[string]interface{}{"ids": ids, "folder": folder})
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		resp, reqErr := s.peerRequest(ctx, peer, http.MethodPost, "/api/v1/"+kind+"/bulk-move", payload, http.Header{"Content-Type": []string{"application/json"}})
		cancel()
		s.invalidateWorkspaceCache(peerID)
		if reqErr != nil {
			peerErrors = append(peerErrors, peer.Identity.Name+": "+peerError(reqErr))
			continue
		}
		var packet struct {
			Code int                    `json:"code"`
			Data map[string]interface{} `json:"data"`
		}
		errDecode := json.NewDecoder(resp.Body).Decode(&packet)
		resp.Body.Close()
		if resp.StatusCode >= 300 || errDecode != nil || packet.Code != 0 {
			peerErrors = append(peerErrors, peer.Identity.Name+": 批量移动失败")
			continue
		}
		if v, ok := packet.Data["moved"].(float64); ok {
			moved += int(v)
		}
	}

	respond(w, http.StatusOK, 0, "moved", map[string]interface{}{
		"kind":    kind,
		"folder":  folder,
		"moved":   moved,
		"partial": len(peerErrors) > 0,
		"errors":  peerErrors,
	})
}
