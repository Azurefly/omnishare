package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omnishare/internal/durable"
	"omnishare/internal/model"
	"omnishare/internal/storage"
)

var errBlobIntegrity = errors.New("existing blob failed integrity validation")

type fileMove struct {
	Node string `json:"node"`
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

type fileReference struct {
	File *model.FileAsset `json:"file,omitempty"`
	Move *fileMove        `json:"move,omitempty"`
	Node string           `json:"source_node_id,omitempty"`
	Name string           `json:"source_node_name,omitempty"`
}

func transferredFileID(node, id string) string {
	digest := sha256.Sum256([]byte(qualifyID(node, id)))
	return "f_move_" + hex.EncodeToString(digest[:])
}

func (s *Server) fileMovePath(id string) string {
	return filepath.Join(s.dataDir, "file-moves", id+".json")
}

func (s *Server) readFileMove(id string) (*fileMove, error) {
	if !qualifiedObject.MatchString(qualifyID(s.cfg.Get().NodeID, id)) {
		return nil, storage.ErrNotFound
	}
	data, err := os.ReadFile(s.fileMovePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var move fileMove
	if err := json.Unmarshal(data, &move); err != nil {
		return nil, err
	}
	if !qualifiedObject.MatchString(qualifyID(move.Node, move.ID)) || !validTransferHash(move.Hash) {
		return nil, errors.New("invalid file move record")
	}
	return &move, nil
}

func validTransferHash(hash string) bool {
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == sha256.Size && hash == strings.ToLower(hash)
}

func (s *Server) localFileReference(id string) (fileReference, error) {
	if file, err := s.store.GetFile(id); err == nil {
		file = s.presentFile(file)
		file.StoragePath = ""
		return fileReference{File: &file}, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fileReference{}, err
	}
	move, err := s.readFileMove(id)
	return fileReference{Move: move}, err
}

// This route is also served to directly bound peers. It returns data, never
// redirects a signed request or discloses a stored path or peer address.
func (s *Server) describeFileReference(w http.ResponseWriter, r *http.Request) {
	result, err := s.localFileReference(r.PathValue("id"))
	if errors.Is(err, storage.ErrNotFound) {
		respond(w, 404, 404, "文件已删除或不存在", nil)
	} else if err != nil {
		respond(w, 500, 500, "无法读取文件位置", nil)
	} else {
		respond(w, 200, 0, "file location", result)
	}
}

func (s *Server) peerFileReference(ctx context.Context, peer peerBinding, id string) (fileReference, int, error) {
	path := "/api/v1/files/" + id + "/reference"
	resp, err := s.peerRequest(ctx, peer, http.MethodGet, path, nil, nil)
	if err != nil {
		return fileReference{}, 502, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fileReference{}, resp.StatusCode, errors.New("无法从来源设备读取文件位置")
	}
	var packet struct {
		Code int           `json:"code"`
		Data fileReference `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&packet); err != nil || packet.Code != 0 {
		return fileReference{}, 502, errors.New("来源设备返回了无效的文件位置")
	}
	ref := packet.Data
	if ref.File != nil && ref.File.ID != id {
		return fileReference{}, 502, errors.New("来源设备返回了其他文件")
	}
	if ref.File == nil && (ref.Move == nil || !qualifiedObject.MatchString(qualifyID(ref.Move.Node, ref.Move.ID))) {
		return fileReference{}, 502, errors.New("来源设备返回了无效的转移位置")
	}
	return ref, 200, nil
}

func (s *Server) resolveWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	node, id := splitObject(r.PathValue("reference"))
	if node == "" {
		respond(w, 400, 400, "无效的文件引用", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()
	cfg := s.cfg.Get()
	visited := map[string]bool{}
	for len(visited) < 16 && !visited[qualifyID(node, id)] {
		visited[qualifyID(node, id)] = true
		var ref fileReference
		var err error
		var name string
		if node == cfg.NodeID {
			ref, err = s.localFileReference(id)
			name = cfg.NodeName
			if err != nil {
				respond(w, 404, 404, "文件已删除或不存在", nil)
				return
			}
		} else {
			peer, ok := s.boundPeer(node)
			if !ok {
				respond(w, 404, 404, "文件所在设备未绑定或已解绑", nil)
				return
			}
			name = peerDisplayName(peer)
			var status int
			ref, status, err = s.peerFileReference(ctx, peer, id)
			if err != nil {
				if status == 502 || status == 404 {
					// The receiving device can still open its own copy when
					// the original device is offline or has cleared its index.
					if local, localErr := s.store.GetFile(transferredFileID(node, id)); localErr == nil {
						local.StoragePath = ""
						respond(w, 200, 0, "file location", fileReference{File: &local, Node: cfg.NodeID, Name: cfg.NodeName})
						return
					}
				}
				message := "设备「" + name + "」暂不可达，稍后可重试"
				if status == 404 {
					message = "文件已删除，或来源设备版本不支持文件引用"
				} else if status == 401 || status == 403 {
					message = "来源设备已撤销访问授权"
				}
				respond(w, 502, 502, message, nil)
				return
			}
		}
		if ref.File != nil {
			ref.File.StoragePath = ""
			if node != cfg.NodeID {
				ref.File.ID = qualifyID(node, id)
			}
			ref.Node, ref.Name = node, name
			respond(w, 200, 0, "file location", ref)
			return
		}
		node, id = ref.Move.Node, ref.Move.ID
	}
	respond(w, 409, 409, "无法确认文件的转移位置", nil)
}

func (s *Server) transferFileToLocal(w http.ResponseWriter, r *http.Request) {
	allowLongTransfer(w)
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, 400, 400, "无效的文件转移请求", nil)
		return
	}
	node, id := splitObject(req.ID)
	if node == "" || node == s.cfg.Get().NodeID {
		respond(w, 400, 400, "请选择其他设备上的文件", nil)
		return
	}
	peer, ok := s.boundPeer(node)
	if !ok {
		respond(w, 404, 404, "来源设备未绑定或已解绑", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	targetID := transferredFileID(node, id)
	if _, busy := s.activeFileTransfers.LoadOrStore(targetID, struct{}{}); busy {
		respond(w, 409, 409, "此文件正在转移", nil)
		return
	}
	defer s.activeFileTransfers.Delete(targetID)
	lookup, stopLookup := context.WithTimeout(ctx, 18*time.Second)
	ref, _, err := s.peerFileReference(lookup, peer, id)
	stopLookup()
	if err != nil {
		respond(w, 502, 502, "无法读取来源文件，请确认来源设备在线且已升级", nil)
		return
	}
	if ref.File == nil {
		local, err := s.store.GetFile(targetID)
		if ref.Move.Node == s.cfg.Get().NodeID && ref.Move.ID == targetID && err == nil &&
			local.FileHash == ref.Move.Hash && fileHashMatches(filepath.Join(s.dataDir, local.StoragePath), local.FileHash) {
			s.finishIncomingFile(w, ctx, peer, id, local)
			return
		}
		respond(w, 409, 409, "文件已转移，请刷新文件列表", nil)
		return
	}
	asset := *ref.File
	if asset.SourceFileError != "" {
		respond(w, 409, 409, asset.SourceFileError, nil)
		return
	}
	limit := s.cfg.Get().MaxUploadMB * 1024 * 1024
	if asset.FileSize < 0 || asset.FileSize > limit {
		respond(w, 413, 413, "文件超过本机接收大小限制", nil)
		return
	}
	if !validTransferHash(asset.FileHash) || sanitizeFilename(asset.FileName) != asset.FileName || asset.FileName == "" {
		respond(w, 409, 409, "来源文件缺少有效的名称或校验值", nil)
		return
	}

	local, err := s.store.GetFile(targetID)
	if errors.Is(err, storage.ErrNotFound) {
		local, err = s.receiveTransferredFile(ctx, peer, id, targetID, asset)
	}
	if err != nil || local.FileHash != asset.FileHash || local.FileSize != asset.FileSize ||
		!fileHashMatches(filepath.Join(s.dataDir, local.StoragePath), local.FileHash) {
		respond(w, 502, 502, "文件未能在本机通过保存和校验，来源文件未移除", nil)
		return
	}
	s.finishIncomingFile(w, ctx, peer, id, local)
}

func (s *Server) receiveTransferredFile(ctx context.Context, peer peerBinding, id, targetID string, asset model.FileAsset) (model.FileAsset, error) {
	resp, err := s.peerRequest(ctx, peer, http.MethodGet, "/api/v1/files/"+id+"/download", nil, nil)
	if err != nil {
		return model.FileAsset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || (resp.ContentLength >= 0 && resp.ContentLength != asset.FileSize) {
		return model.FileAsset{}, errors.New("source did not supply the expected file")
	}
	dir := filepath.Join(s.dataDir, "uploads", ".staging")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return model.FileAsset{}, err
	}
	temp, err := os.CreateTemp(dir, "transfer-*")
	if err != nil {
		return model.FileAsset{}, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(resp.Body, asset.FileSize+1))
	if err != nil || size != asset.FileSize || hex.EncodeToString(hash.Sum(nil)) != asset.FileHash {
		return model.FileAsset{}, errors.New("received file failed integrity validation")
	}
	if err := temp.Sync(); err != nil {
		return model.FileAsset{}, err
	}
	if err := temp.Close(); err != nil {
		return model.FileAsset{}, err
	}

	asset.ID = targetID
	asset.DownloadCount = 0
	asset.DownloadURL = "/api/v1/files/" + targetID + "/download"
	asset.StreamURL = ""
	if asset.IsVideo {
		asset.StreamURL = "/api/v1/files/" + targetID + "/stream"
	}
	asset.StoragePath = s.managedFilePath(asset)
	asset.SourceShortcut = false
	asset.SourceFileError = ""
	asset.DeletedAt = nil
	asset.CreatedAt = time.Now().UTC()
	s.fileStorageMu.Lock()
	defer s.fileStorageMu.Unlock()
	if _, err := s.store.GetTrashedFile(targetID); !errors.Is(err, storage.ErrNotFound) {
		return model.FileAsset{}, errors.New("the local destination is in trash or unavailable")
	}
	if asset.ExpiresAt != nil && !asset.ExpiresAt.After(time.Now()) {
		return model.FileAsset{}, errors.New("the source file has expired")
	}
	if err := s.commitFileBlob(temp.Name(), asset, true); err != nil {
		return model.FileAsset{}, err
	}
	return asset, nil
}

func (s *Server) finishIncomingFile(w http.ResponseWriter, ctx context.Context, peer peerBinding, id string, local model.FileAsset) {
	path := "/api/v1/files/" + id + "/finish-transfer"
	body, _ := json.Marshal(map[string]string{"hash": local.FileHash})
	h := http.Header{"Content-Type": {"application/json"}}
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	s.invalidateWorkspaceCache(peer.Identity.ID)
	defer s.invalidateWorkspaceCache(peer.Identity.ID)
	resp, err := s.peerRequest(ctx, peer, http.MethodPost, path, body, h)
	var packet struct {
		Code int `json:"code"`
	}
	complete := false
	if err == nil {
		defer resp.Body.Close()
		complete = resp.StatusCode == http.StatusOK &&
			json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&packet) == nil && packet.Code == 0
	}
	local.StoragePath = ""
	if !complete {
		respond(w, 200, 0, "本机副本已保存，来源清理未确认，可重试转移", map[string]interface{}{"file": local, "moved": false})
		return
	}
	respond(w, 200, 0, "转移完成", map[string]interface{}{"file": local, "moved": true})
}

func (s *Server) finalizeOutgoingFile(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-OmniShare-Peer")
	id := r.PathValue("id")
	var req struct {
		Hash string `json:"hash"`
	}
	if target == "" {
		respond(w, 403, 403, "只接受已绑定设备的转移确认", nil)
		return
	}
	if !qualifiedObject.MatchString(qualifyID(s.cfg.Get().NodeID, id)) ||
		decodeJSON(r, &req) != nil || !validTransferHash(req.Hash) {
		respond(w, 400, 400, "无效的来源清理确认", nil)
		return
	}
	s.fileStorageMu.Lock()
	defer s.fileStorageMu.Unlock()
	move := fileMove{Node: target, ID: transferredFileID(s.cfg.Get().NodeID, id), Hash: req.Hash}
	prior, err := s.readFileMove(id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		respond(w, 500, 500, "无法读取之前的转移记录", nil)
		return
	}
	if prior != nil && *prior != move {
		respond(w, 409, 409, "已有其他设备发起转移", nil)
		return
	}
	source, err := s.store.GetFile(id)
	if errors.Is(err, storage.ErrNotFound) && prior != nil {
		s.completeTransferredTrash(w, id, req.Hash)
		return
	}
	if err != nil || source.FileHash != req.Hash {
		respond(w, 409, 409, "来源文件不存在或校验值已变化", nil)
		return
	}
	if _, busy := s.activeFileTransfers.Load(id); busy {
		respond(w, 409, 409, "此文件正在接收，请稍后再转移", nil)
		return
	}
	if prior == nil {
		data, _ := json.Marshal(move)
		if err := durable.WriteFile(s.fileMovePath(id), data, 0o600); err != nil {
			respond(w, 500, 500, "无法保存文件转移记录", nil)
			return
		}
	}
	if _, err := s.store.DeleteFiles([]string{id}); err != nil {
		respond(w, 500, 500, "无法移除来源文件", nil)
		return
	}
	s.completeTransferredTrash(w, id, req.Hash)
}

func (s *Server) completeTransferredTrash(w http.ResponseWriter, id, hash string) {
	file, err := s.store.GetTrashedFile(id)
	if errors.Is(err, storage.ErrNotFound) {
		err = os.Remove(filepath.Join(s.dataDir, "uploads", ".purging", id))
		if err == nil || errors.Is(err, os.ErrNotExist) {
			respond(w, 200, 0, "来源已清理", nil)
			return
		}
	} else if err == nil && file.FileHash == hash {
		warning, purgeErr := s.purgeTrashItem("file", id)
		if purgeErr == nil && warning == "" {
			respond(w, 200, 0, "来源已清理", nil)
			return
		}
		if purgeErr != nil {
			_ = s.store.RestoreTrash("file", id)
		}
	}
	respond(w, 500, 500, "来源文件清理未完成", nil)
}

func (s *Server) commitFileBlob(stagingPath string, asset model.FileAsset, syncDirectory bool) error {
	blobPath := filepath.Join(s.dataDir, asset.StoragePath)
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o700); err != nil {
		return err
	}
	info, err := os.Stat(blobPath)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Link(stagingPath, blobPath); err != nil {
			return err
		}
		created = true
	} else if err != nil {
		return err
	} else if info.Size() != asset.FileSize || !fileHashMatches(blobPath, asset.FileHash) {
		return errBlobIntegrity
	}
	if syncDirectory {
		if err := durable.SyncDir(filepath.Dir(blobPath)); err != nil {
			if created {
				_ = os.Remove(blobPath)
			}
			return err
		}
	}
	if err := s.store.CreateFile(asset); err != nil {
		if created {
			_ = os.Remove(blobPath)
		}
		return err
	}
	return nil
}
