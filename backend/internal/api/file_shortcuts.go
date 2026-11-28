package api

import (
	"crypto/ed25519"
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

var errShortcutMissing = errors.New("源文件已经删除")
var errShortcutUnavailable = errors.New("源文件或快捷方式不可访问")
var errShortcutChanged = errors.New("源文件已修改，请重新添加快捷方式")

type localShortcut struct {
	Path      string    `json:"path"`
	Modified  time.Time `json:"modified"`
	Signature []byte    `json:"signature"`
}

func shortcutBlob(path string) bool {
	return storage.FileStorageKind(path) == "shortcut"
}

func shortcutPayload(asset model.FileAsset, path string, modified time.Time) []byte {
	return []byte(strings.Join([]string{asset.ID, asset.FileHash, path, modified.UTC().Format(time.RFC3339Nano)}, "\n"))
}

func (s *Server) readShortcut(asset model.FileAsset) (localShortcut, error) {
	var link localShortcut
	if !shortcutBlob(asset.StoragePath) || storage.ValidateStoragePath(asset.StoragePath) != nil {
		return link, errShortcutUnavailable
	}
	file, err := os.Open(filepath.Join(s.dataDir, asset.StoragePath))
	if err != nil {
		return link, errShortcutUnavailable
	}
	defer file.Close()
	if err := json.NewDecoder(io.LimitReader(file, 16<<10)).Decode(&link); err != nil {
		return link, errShortcutUnavailable
	}
	_, key, err := s.cfg.DiscoveryIdentity()
	if err != nil || !filepath.IsAbs(link.Path) ||
		!ed25519.Verify(key.Public().(ed25519.PublicKey), shortcutPayload(asset, link.Path, link.Modified), link.Signature) {
		return link, errShortcutUnavailable
	}
	return link, nil
}

func (s *Server) readableFile(asset model.FileAsset) (string, error) {
	if err := storage.ValidateStoragePath(asset.StoragePath); err != nil {
		return "", err
	}
	if !shortcutBlob(asset.StoragePath) {
		return filepath.Join(s.dataDir, filepath.FromSlash(asset.StoragePath)), nil
	}
	link, err := s.readShortcut(asset)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(link.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errShortcutMissing
	}
	if err != nil || path != link.Path {
		return "", errShortcutUnavailable
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errShortcutMissing
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", errShortcutUnavailable
	}
	if info.Size() != asset.FileSize || !info.ModTime().Equal(link.Modified) {
		return "", errShortcutChanged
	}
	return path, nil
}

func (s *Server) addFileShortcut(w http.ResponseWriter, r *http.Request) {
	// This endpoint may register an existing path on the server PC. The same
	// browser connected over LAN or a bound peer can only use existing links.
	if !localWorkbench(r) {
		respond(w, 403, 403, "请在源文件所在设备的本机工作台添加快捷方式", nil)
		return
	}
	var req struct {
		Path   string `json:"path"`
		Folder string `json:"folder"`
		Pick   bool   `json:"pick"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, 400, 400, "无效的快捷方式请求", nil)
		return
	}
	if req.Pick {
		picked, err := pickExistingFile(r.Context())
		if err != nil {
			respond(w, 400, 400, "无法打开本机文件选择器，请粘贴完整路径", nil)
			return
		}
		if picked == "" {
			respond(w, 200, 0, "cancelled", nil)
			return
		}
		req.Path = picked
	}
	path, info, err := s.externalPath(req.Path)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errShortcutMissing) {
			status = http.StatusNotFound
		}
		respond(w, status, status, err.Error(), nil)
		return
	}
	if !info.Mode().IsRegular() {
		respond(w, 400, 400, "请选择单个文件；文件夹可通过“导入副本”添加", nil)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		respond(w, 404, 404, errShortcutMissing.Error(), nil)
		return
	}
	defer file.Close()
	before, err := file.Stat()
	name := sanitizeFilename(filepath.Base(path))
	folder, folderErr := storage.NormalizeFolderPath(req.Folder)
	if err != nil || !os.SameFile(info, before) || name == "" || folderErr != nil {
		respond(w, 400, 400, "请选择常规文件和有效的应用目录", nil)
		return
	}
	prefix := make([]byte, 512)
	n, readErr := io.ReadFull(file, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		respond(w, 422, 422, "无法读取源文件", nil)
		return
	}
	mimeType := http.DetectContentType(prefix[:n])
	hash := sha256.New()
	_, _ = hash.Write(prefix[:n])
	copied, copyErr := io.Copy(hash, file)
	after, statErr := file.Stat()
	if copyErr != nil || statErr != nil || copied+int64(n) != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) {
		respond(w, 409, 409, errShortcutChanged.Error(), nil)
		return
	}
	id := storage.NewID("f")
	asset := model.FileAsset{
		ID: id, FileName: name, FileSize: before.Size(), MIMEType: mimeType, Folder: folder,
		StoragePath: filepath.Join("uploads", "shortcuts", id, visibleFileName(name)+".omnishare-link.json"),
		FileHash:    hex.EncodeToString(hash.Sum(nil)), CreatedAt: time.Now().UTC(),
		IsVideo: strings.HasPrefix(mimeType, "video/"), DownloadURL: "/api/v1/files/" + id + "/download",
	}
	if asset.IsVideo {
		asset.StreamURL = "/api/v1/files/" + id + "/stream"
	}
	_, key, err := s.cfg.DiscoveryIdentity()
	if err != nil {
		respond(w, 500, 500, "无法签发本机快捷方式", nil)
		return
	}
	link := localShortcut{Path: path, Modified: before.ModTime(), Signature: ed25519.Sign(key, shortcutPayload(asset, path, before.ModTime()))}
	data, _ := json.MarshalIndent(link, "", "  ")
	full := filepath.Join(s.dataDir, asset.StoragePath)
	s.fileStorageMu.Lock()
	defer s.fileStorageMu.Unlock()
	if err := durable.WriteFile(full, data, 0o600); err != nil {
		respond(w, 500, 500, "无法写入快捷方式", nil)
		return
	}
	if err := s.store.CreateFile(asset); err != nil {
		_ = os.Remove(full)
		respond(w, 500, 500, "无法登记快捷方式", nil)
		return
	}
	respond(w, 201, 0, "已添加本机快捷方式", s.presentFile(asset))
}

func (s *Server) openAsset(asset model.FileAsset) (*os.File, error) {
	path, err := s.readableFile(asset)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != asset.FileSize {
		_ = file.Close()
		return nil, errBlobIntegrity
	}
	if shortcutBlob(asset.StoragePath) {
		hash := sha256.New()
		size, err := io.Copy(hash, io.LimitReader(file, asset.FileSize+1))
		if err != nil || size != asset.FileSize || hex.EncodeToString(hash.Sum(nil)) != asset.FileHash {
			_ = file.Close()
			return nil, errShortcutChanged
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

func (s *Server) presentFile(asset model.FileAsset) model.FileAsset {
	asset.StorageKind = storage.FileStorageKind(asset.StoragePath)
	if !shortcutBlob(asset.StoragePath) {
		return asset
	}
	asset.SourceShortcut = true
	if _, err := s.readableFile(asset); err != nil {
		asset.SourceFileError = err.Error()
	}
	asset.StoragePath = ""
	return asset
}

func visibleFileName(name string) string {
	replacer := strings.NewReplacer("<", "_", ">", "_", ":", "_", "\"", "_", "/", "_", "\\", "_", "|", "_", "?", "_", "*", "_")
	// The prefix sidesteps Windows device names (CON, AUX, NUL, COM1, etc.).
	visible := []rune(strings.TrimRight(replacer.Replace(name), ". "))
	if len(visible) > 90 {
		visible = visible[:90]
	}
	return "文件-" + string(visible)
}

func (s *Server) managedFilePath(asset model.FileAsset) string {
	s.storageMu.RLock()
	engine := s.storageState.Engine
	s.storageMu.RUnlock()
	if engine == "folder" {
		return filepath.Join("uploads", "files", asset.ID, visibleFileName(asset.FileName))
	}
	return filepath.Join("uploads", "blobs", asset.FileHash[:2], asset.FileHash)
}
