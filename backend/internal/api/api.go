package api

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"omnishare/internal/buildinfo"
	"omnishare/internal/config"
	"omnishare/internal/discovery"
	"omnishare/internal/frontend"
	"omnishare/internal/model"
	"omnishare/internal/storage"
)

const (
	Version              = buildinfo.Version
	maxJSONBody          = 1 << 20
	maxTTLSeconds        = 10 * 365 * 24 * 60 * 60
	shareSessionDuration = 30 * time.Minute
)

type Server struct {
	store    *storage.Store
	cfg      *config.Manager
	dataDir  string
	started  time.Time
	client   *http.Client
	registry *discovery.Registry

	ticketKey []byte
	mu        sync.Mutex
	auth      map[string]*authState
	requestIP map[string]int
	shareIP   map[string]int
	counted   map[string]time.Time
}

type authState struct {
	WindowStart time.Time
	Failures    int
	BlockedTill time.Time
}

type mediaTicket struct {
	FileID      string `json:"file_id"`
	Disposition string `json:"disposition"`
	ExpiresUnix int64  `json:"expires_unix"`
	Nonce       string `json:"nonce"`
}

type shareSession struct {
	Token       string `json:"token"`
	ExpiresUnix int64  `json:"expires_unix"`
}

func New(store *storage.Store, cfg *config.Manager, registries ...*discovery.Registry) *Server {
	var registry *discovery.Registry
	if len(registries) > 0 {
		registry = registries[0]
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("secure random source unavailable: %v", err))
	}
	client := &http.Client{
		Timeout: 1500 * time.Millisecond,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Server{
		store: store, cfg: cfg, dataDir: cfg.Get().DataDir, started: time.Now(), client: client, registry: registry,
		ticketKey: key, auth: map[string]*authState{}, requestIP: map[string]int{}, shareIP: map[string]int{}, counted: map[string]time.Time{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("POST /api/v1/auth/verify", s.verifyAuth)
	mux.HandleFunc("GET /api/v1/dashboard", s.protected(s.dashboard))
	mux.HandleFunc("GET /api/v1/config", s.protected(s.getConfig))
	mux.HandleFunc("PUT /api/v1/config", s.protected(s.updateConfig))
	mux.HandleFunc("GET /api/v1/notes", s.protected(s.listNotes))
	mux.HandleFunc("POST /api/v1/notes", s.protected(s.createNote))
	mux.HandleFunc("GET /api/v1/notes/{id}", s.protected(s.getNote))
	mux.HandleFunc("GET /api/v1/notes/{id}/manage", s.protected(s.manageNote))
	mux.HandleFunc("PUT /api/v1/notes/{id}", s.protected(s.updateNote))
	mux.HandleFunc("DELETE /api/v1/notes/{id}", s.protected(s.deleteNote))
	mux.HandleFunc("POST /api/v1/notes/bulk-delete", s.protected(s.bulkDeleteNotes))
	mux.HandleFunc("GET /n/{id}/raw", s.protected(s.rawNote))
	mux.HandleFunc("GET /api/v1/files", s.protected(s.listFiles))
	mux.HandleFunc("POST /api/v1/files/upload", s.protected(s.uploadFile))
	mux.HandleFunc("PUT /api/v1/files/{id}", s.protected(s.renameFile))
	mux.HandleFunc("GET /api/v1/files/{id}/download", s.protected(s.downloadFile))
	mux.HandleFunc("GET /api/v1/files/{id}/stream", s.protected(s.streamFile))
	mux.HandleFunc("POST /api/v1/files/{id}/ticket", s.protected(s.createMediaTicket))
	mux.HandleFunc("DELETE /api/v1/files/{id}", s.protected(s.deleteFile))
	mux.HandleFunc("POST /api/v1/files/bulk-delete", s.protected(s.bulkDeleteFiles))
	mux.HandleFunc("GET /media/{ticket}", s.serveMediaTicket)
	mux.HandleFunc("GET /api/v1/pads", s.protected(s.listPads))
	mux.HandleFunc("POST /api/v1/pads", s.protected(s.createPad))
	mux.HandleFunc("GET /api/v1/pads/{id}", s.protected(s.getPad))
	mux.HandleFunc("PUT /api/v1/pads/{id}", s.protected(s.updatePad))
	mux.HandleFunc("DELETE /api/v1/pads/{id}", s.protected(s.deletePad))
	mux.HandleFunc("GET /api/v1/devices", s.protected(s.listDevices))
	mux.HandleFunc("GET /api/v1/shares", s.protected(s.listShares))
	mux.HandleFunc("POST /api/v1/shares", s.protected(s.createShare))
	mux.HandleFunc("DELETE /api/v1/shares/{id}", s.protected(s.revokeShare))
	mux.HandleFunc("GET /api/v1/trash", s.protected(s.listTrash))
	mux.HandleFunc("POST /api/v1/trash/{type}/{id}/restore", s.protected(s.restoreTrash))
	mux.HandleFunc("DELETE /api/v1/trash/{type}/{id}", s.protected(s.purgeTrash))
	mux.HandleFunc("DELETE /api/v1/trash", s.protected(s.emptyTrash))
	mux.HandleFunc("GET /api/v1/audit", s.protected(s.listAudit))
	mux.HandleFunc("GET /api/v1/backup", s.protected(s.backup))
	mux.HandleFunc("POST /api/v1/restore", s.protected(s.restore))
	mux.HandleFunc("GET /s/{token}", s.serveShare)
	mux.Handle("/", frontend.Handler())
	return s.common(mux)
}

func (s *Server) common(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validHost(r.Host) {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; worker-src 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/media/") || strings.HasPrefix(r.URL.Path, "/n/") {
			w.Header().Set("Cache-Control", "no-store, private")
			w.Header().Set("Pragma", "no-cache")
		}
		if !s.applyCORS(w, r) {
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.enterRequest(clientIP(r), 8, 128) {
			respond(w, http.StatusTooManyRequests, 429, "too many concurrent requests", nil)
			return
		}
		defer s.leaveRequest(clientIP(r))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	allowed := origin == requestOrigin(r)
	if !allowed {
		for _, candidate := range s.cfg.Get().AllowedOrigins {
			if subtle.ConstantTimeCompare([]byte(origin), []byte(candidate)) == 1 {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		respond(w, http.StatusForbidden, 403, "cross-origin request denied", nil)
		return false
	}
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-OmniShare-Key, X-OmniShare-Confirm")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
	return true
}

func (s *Server) validHost(raw string) bool {
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		host = raw
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if base := s.cfg.Get().PublicBaseURL; base != "" {
		if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	interfaces, _ := net.InterfaceAddrs()
	for _, addr := range interfaces {
		var local net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			local = v.IP
		case *net.IPAddr:
			local = v.IP
		}
		if local != nil && local.Equal(ip) {
			return true
		}
	}
	return false
}

func (s *Server) enterRequest(ip string, perIP, global int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.requestIP {
		total += n
	}
	if total >= global || s.requestIP[ip] >= perIP {
		return false
	}
	s.requestIP[ip]++
	return true
}

func (s *Server) leaveRequest(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestIP[ip] <= 1 {
		delete(s.requestIP, ip)
	} else {
		s.requestIP[ip]--
	}
}

func (s *Server) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			_ = s.store.AddAudit("auth_denied", "security", "", "authorization denied", requestMetadata(r))
			respond(w, http.StatusUnauthorized, 401, "access key required", nil)
			return
		}
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if !s.cfg.HasAccessKey() {
		return !s.cfg.Get().AllowLAN
	}
	return s.cfg.VerifyAccessKey(r.Header.Get("X-OmniShare-Key"))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, 0, "ok", map[string]interface{}{"status": "ok", "version": Version})
}

func (s *Server) verifyAuth(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry := s.authRetryAfter(ip); retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		respond(w, http.StatusTooManyRequests, 429, "too many failed attempts", nil)
		return
	}
	if s.authorized(r) {
		s.resetAuthFailures(ip)
		_ = s.store.AddAudit("auth_success", "security", "", "authentication succeeded", requestMetadata(r))
		respond(w, http.StatusOK, 0, "authorized", nil)
		return
	}
	s.recordAuthFailure(ip)
	_ = s.store.AddAudit("auth_failure", "security", "", "authentication failed", requestMetadata(r))
	respond(w, http.StatusUnauthorized, 401, "invalid access key", nil)
}

func (s *Server) authRetryAfter(ip string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.auth[ip]
	if st == nil || time.Now().After(st.BlockedTill) {
		return 0
	}
	return time.Until(st.BlockedTill)
}

func (s *Server) recordAuthFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	st := s.auth[ip]
	if st == nil || now.Sub(st.WindowStart) > 10*time.Minute {
		st = &authState{WindowStart: now}
		s.auth[ip] = st
	}
	st.Failures++
	if st.Failures >= 5 {
		steps := st.Failures - 5
		if steps > 5 {
			steps = 5
		}
		st.BlockedTill = now.Add(time.Duration(1<<steps) * time.Second)
	}
}

func (s *Server) resetAuthFailures(ip string) {
	s.mu.Lock()
	delete(s.auth, ip)
	s.mu.Unlock()
}

func (s *Server) dashboard(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, 0, "success", s.store.Stats())
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, 0, "success", s.cfg.Public())
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	var req model.AppConfig
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	current := s.cfg.Get()
	if req.AccessKey == "" && current.AccessKeyHash != "" && r.Header.Get("X-OmniShare-Confirm") != "CLEAR-ACCESS-KEY" {
		respond(w, http.StatusBadRequest, 400, "clearing the access key requires X-OmniShare-Confirm: CLEAR-ACCESS-KEY", nil)
		return
	}
	if req.AllowLAN && req.AccessKey == "__KEEP__" && current.AccessKeyHash == "" {
		respond(w, http.StatusBadRequest, 400, "set a strong access key before enabling LAN access", nil)
		return
	}
	if err := s.cfg.Update(req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	_ = s.store.AddAudit("config_update", "config", "", "configuration updated", map[string]interface{}{
		"source_ip": clientIP(r), "node_name_changed": current.NodeName != s.cfg.Get().NodeName, "lan_enabled": s.cfg.Get().AllowLAN,
	})
	respond(w, http.StatusOK, 0, "configuration saved; network changes require restart", s.cfg.Public())
}

func (s *Server) listNotes(w http.ResponseWriter, r *http.Request) {
	items := s.store.ListNotes(r.URL.Query().Get("q"), r.URL.Query().Get("tag"))
	for i := range items {
		if items[i].IsBurnAfterRead || items[i].MaxReadCount > 0 {
			items[i].Content = ""
			items[i].ContentRedacted = true
		}
	}
	respondPage(w, r, items)
}

func (s *Server) createNote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content         string   `json:"content"`
		ContentType     string   `json:"content_type"`
		Tags            []string `json:"tags"`
		Pinned          bool     `json:"pinned"`
		IsBurnAfterRead bool     `json:"is_burn_after_read"`
		MaxReadCount    int      `json:"max_read_count"`
		TTLSeconds      int64    `json:"ttl_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		respond(w, http.StatusBadRequest, 400, "content is required", nil)
		return
	}
	if len([]byte(req.Content)) > 2*1024*1024 {
		respond(w, http.StatusRequestEntityTooLarge, 413, "note is too large", nil)
		return
	}
	if req.MaxReadCount < 0 || req.MaxReadCount > 1_000_000 {
		respond(w, http.StatusBadRequest, 400, "max_read_count is out of range", nil)
		return
	}
	exp, err := expiryFromSeconds(req.TTLSeconds)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	if req.ContentType == "" {
		req.ContentType = detectContentType(req.Content)
	}
	now := time.Now()
	n := model.QuickNote{ID: storage.NewID("n"), Content: req.Content, ContentType: req.ContentType, Tags: normalizeTags(append(req.Tags, extractTags(req.Content)...)), Pinned: req.Pinned, IsBurnAfterRead: req.IsBurnAfterRead, MaxReadCount: req.MaxReadCount, ExpiresAt: exp, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateNote(n); err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusCreated, 0, "created", n)
}

func (s *Server) getNote(w http.ResponseWriter, r *http.Request) {
	var n model.QuickNote
	var err error
	if r.Method == http.MethodHead {
		n, err = s.store.GetNote(r.PathValue("id"))
	} else {
		n, err = s.store.ReadNote(r.PathValue("id"))
	}
	if err != nil {
		respond(w, http.StatusNotFound, 404, "note not found", nil)
		return
	}
	respond(w, http.StatusOK, 0, "success", n)
}

func (s *Server) manageNote(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNote(r.PathValue("id"))
	if err != nil {
		respond(w, http.StatusNotFound, 404, "note not found", nil)
		return
	}
	respond(w, http.StatusOK, 0, "success", n)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content         *string   `json:"content"`
		Tags            *[]string `json:"tags"`
		Pinned          *bool     `json:"pinned"`
		IsBurnAfterRead *bool     `json:"is_burn_after_read"`
		MaxReadCount    *int      `json:"max_read_count"`
		TTLSeconds      *int64    `json:"ttl_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	updated, err := s.store.UpdateNote(r.PathValue("id"), func(n *model.QuickNote) error {
		if req.Content != nil {
			if len([]byte(*req.Content)) > 2*1024*1024 {
				return errors.New("note is too large")
			}
			n.Content = *req.Content
			n.ContentType = detectContentType(*req.Content)
		}
		if req.Tags != nil {
			n.Tags = normalizeTags(*req.Tags)
		}
		if req.Pinned != nil {
			n.Pinned = *req.Pinned
		}
		if req.IsBurnAfterRead != nil {
			n.IsBurnAfterRead = *req.IsBurnAfterRead
		}
		if req.MaxReadCount != nil {
			if *req.MaxReadCount < 0 || *req.MaxReadCount > 1_000_000 || (*req.MaxReadCount > 0 && *req.MaxReadCount < n.ReadCount) {
				return errors.New("max_read_count is out of range")
			}
			n.MaxReadCount = *req.MaxReadCount
		}
		if req.TTLSeconds != nil {
			exp, err := expiryFromSeconds(*req.TTLSeconds)
			if err != nil {
				return err
			}
			n.ExpiresAt = exp
		}
		return nil
	})
	if errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "note not found", nil)
		return
	}
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	respond(w, http.StatusOK, 0, "updated", updated)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.DeleteNotes([]string{r.PathValue("id")})
	if errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "note not found", nil)
		return
	}
	if err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusOK, 0, "moved to trash", map[string]int{"deleted": count})
}

func (s *Server) bulkDeleteNotes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	count, err := s.store.DeleteNotes(req.IDs)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusOK, 0, "moved to trash", map[string]int{"deleted": count})
}

func (s *Server) rawNote(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNote(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="note.txt"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, n.Content)
	}
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	items := s.store.ListFiles(r.URL.Query().Get("q"), r.URL.Query().Get("type"))
	respondPage(w, r, items)
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	maxBytes := cfg.MaxUploadMB * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+2*1024*1024)
	reader, err := r.MultipartReader()
	if err != nil {
		respond(w, http.StatusBadRequest, 400, "multipart upload required", nil)
		return
	}
	stagingDir := filepath.Join(s.dataDir, "uploads", ".staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	var stagingPath, filename, mimeType, digest string
	var size int64
	var ttl int64
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			if stagingPath != "" {
				_ = os.Remove(stagingPath)
			}
			respond(w, http.StatusBadRequest, 400, partErr.Error(), nil)
			return
		}
		if part.FileName() == "" {
			data, _ := io.ReadAll(io.LimitReader(part, 1024))
			if part.FormName() == "ttl_seconds" {
				ttl, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			}
			_ = part.Close()
			continue
		}
		if stagingPath != "" {
			_ = part.Close()
			_ = os.Remove(stagingPath)
			respond(w, http.StatusBadRequest, 400, "only one file per request is allowed", nil)
			return
		}
		filename = sanitizeFilename(part.FileName())
		if filename == "" {
			_ = part.Close()
			respond(w, http.StatusBadRequest, 400, "invalid file name", nil)
			return
		}
		token, tokenErr := storage.NewSecureToken()
		if tokenErr != nil {
			_ = part.Close()
			respond(w, http.StatusInternalServerError, 500, tokenErr.Error(), nil)
			return
		}
		stagingPath = filepath.Join(stagingDir, token)
		dst, createErr := os.OpenFile(stagingPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr != nil {
			_ = part.Close()
			respond(w, http.StatusInternalServerError, 500, createErr.Error(), nil)
			return
		}
		h := sha256.New()
		prefix := make([]byte, 512)
		n, readErr := io.ReadFull(part, prefix)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			_ = dst.Close()
			_ = part.Close()
			_ = os.Remove(stagingPath)
			respond(w, http.StatusBadRequest, 400, readErr.Error(), nil)
			return
		}
		prefix = prefix[:n]
		mimeType = http.DetectContentType(prefix)
		written, copyErr := io.Copy(io.MultiWriter(dst, h), io.MultiReader(bytes.NewReader(prefix), part))
		size = written
		closeErr := dst.Close()
		_ = part.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(stagingPath)
			if copyErr == nil {
				copyErr = closeErr
			}
			respond(w, http.StatusBadRequest, 400, copyErr.Error(), nil)
			return
		}
		if size > maxBytes {
			_ = os.Remove(stagingPath)
			respond(w, http.StatusRequestEntityTooLarge, 413, "file exceeds upload limit", nil)
			return
		}
		digest = hex.EncodeToString(h.Sum(nil))
	}
	if stagingPath == "" {
		respond(w, http.StatusBadRequest, 400, "file is required", nil)
		return
	}
	defer os.Remove(stagingPath)
	exp, err := expiryFromSeconds(ttl)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	blobRelative := filepath.Join("uploads", "blobs", digest[:2], digest)
	blobPath := filepath.Join(s.dataDir, blobRelative)
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o700); err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	newBlob := false
	if info, statErr := os.Stat(blobPath); statErr == nil {
		if info.Size() != size || !fileHashMatches(blobPath, digest) {
			respond(w, http.StatusConflict, 409, "existing blob failed integrity validation", nil)
			return
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		respond(w, http.StatusInternalServerError, 500, statErr.Error(), nil)
		return
	} else if err := os.Rename(stagingPath, blobPath); err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	} else {
		newBlob = true
	}
	now := time.Now()
	id := storage.NewID("f")
	asset := model.FileAsset{ID: id, FileName: filename, FileSize: size, MIMEType: mimeType, StoragePath: blobRelative, FileHash: digest, IsVideo: strings.HasPrefix(mimeType, "video/"), DownloadURL: "/api/v1/files/" + id + "/download", ExpiresAt: exp, CreatedAt: now}
	if asset.IsVideo {
		asset.StreamURL = "/api/v1/files/" + id + "/stream"
	}
	if err := s.store.CreateFile(asset); err != nil {
		if newBlob {
			_ = os.Remove(blobPath)
		}
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusCreated, 0, "uploaded", asset)
}

func (s *Server) renameFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FileName string `json:"file_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	asset, err := s.store.RenameFile(r.PathValue("id"), req.FileName)
	if errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "file not found", nil)
		return
	}
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	respond(w, http.StatusOK, 0, "renamed", asset)
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, r, false) }
func (s *Server) streamFile(w http.ResponseWriter, r *http.Request)   { s.serveAsset(w, r, true) }

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, inline bool) {
	asset, err := s.store.GetFile(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, r, asset, inline, shouldCountDownload(r))
}

func (s *Server) createMediaTicket(w http.ResponseWriter, r *http.Request) {
	asset, err := s.store.GetFile(r.PathValue("id"))
	if err != nil {
		respond(w, http.StatusNotFound, 404, "file not found", nil)
		return
	}
	var req struct {
		Disposition string `json:"disposition"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			respond(w, http.StatusBadRequest, 400, err.Error(), nil)
			return
		}
	}
	if req.Disposition != "inline" {
		req.Disposition = "attachment"
	}
	nonce, err := storage.NewSecureToken()
	if err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	ticket := mediaTicket{FileID: asset.ID, Disposition: req.Disposition, ExpiresUnix: time.Now().Add(10 * time.Minute).Unix(), Nonce: nonce}
	signed, err := s.signValue(ticket)
	if err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusCreated, 0, "ticket created", map[string]interface{}{"url": s.safeBaseURL(r) + "/media/" + signed, "expires_at": time.Unix(ticket.ExpiresUnix, 0)})
}

func (s *Server) serveMediaTicket(w http.ResponseWriter, r *http.Request) {
	var ticket mediaTicket
	if err := s.verifySignedValue(r.PathValue("ticket"), &ticket); err != nil || time.Now().Unix() >= ticket.ExpiresUnix {
		http.Error(w, "media ticket expired", http.StatusGone)
		return
	}
	asset, err := s.store.GetFile(ticket.FileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inline := ticket.Disposition == "inline"
	count := r.Method == http.MethodGet && s.markTicketCounted(ticket.Nonce, time.Unix(ticket.ExpiresUnix, 0))
	s.serveFile(w, r, asset, inline, count)
}

func (s *Server) markTicketCounted(nonce string, expires time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, exp := range s.counted {
		if now.After(exp) {
			delete(s.counted, key)
		}
	}
	if _, ok := s.counted[nonce]; ok {
		return false
	}
	s.counted[nonce] = expires
	return true
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, asset model.FileAsset, inline, count bool) {
	path := filepath.Join(s.dataDir, asset.StoragePath)
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != asset.FileSize {
		http.Error(w, "file integrity check failed", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", asset.MIMEType)
	w.Header().Set("ETag", `"sha256-`+asset.FileHash+`"`)
	disposition := "attachment"
	if inline && safeInlineMIME(asset.MIMEType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+`; filename*=UTF-8''`+url.PathEscape(asset.FileName))
	if count && r.Method == http.MethodGet {
		if err := s.store.IncrementDownload(asset.ID); err != nil {
			http.Error(w, "could not record download", http.StatusInternalServerError)
			return
		}
	}
	http.ServeContent(w, r, asset.FileName, asset.CreatedAt, f)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	s.deleteFiles(w, []string{r.PathValue("id")})
}

func (s *Server) bulkDeleteFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	s.deleteFiles(w, req.IDs)
}

func (s *Server) deleteFiles(w http.ResponseWriter, ids []string) {
	deleted, err := s.store.DeleteFiles(ids)
	if errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "file not found", nil)
		return
	}
	if err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusOK, 0, "moved to trash", map[string]int{"deleted": len(deleted)})
}

func (s *Server) listPads(w http.ResponseWriter, r *http.Request) {
	items := s.store.ListPads(r.URL.Query().Get("q"))
	for i := range items {
		items[i].Content = ""
	}
	respondPage(w, r, items)
}

func (s *Server) createPad(w http.ResponseWriter, r *http.Request) {
	var req struct{ Title, Content string }
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || len([]rune(req.Title)) > 200 || len([]byte(req.Content)) > 10*1024*1024 {
		respond(w, http.StatusBadRequest, 400, "invalid document title or content size", nil)
		return
	}
	now := time.Now()
	p := model.PadDocument{ID: storage.NewID("pad"), Title: req.Title, Content: req.Content, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreatePad(p); err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusCreated, 0, "created", p)
}

func (s *Server) getPad(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetPad(r.PathValue("id"))
	if err != nil {
		respond(w, http.StatusNotFound, 404, "document not found", nil)
		return
	}
	respond(w, http.StatusOK, 0, "success", p)
}

func (s *Server) updatePad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title, Content string
		Version        int
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	if len([]byte(req.Content)) > 10*1024*1024 {
		respond(w, http.StatusRequestEntityTooLarge, 413, "document is too large", nil)
		return
	}
	p, err := s.store.UpdatePad(r.PathValue("id"), req.Title, req.Content, req.Version)
	if errors.Is(err, storage.ErrVersionConflict) {
		current, _ := s.store.GetPad(r.PathValue("id"))
		respond(w, http.StatusConflict, 409, "version conflict", current)
		return
	}
	if errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "document not found", nil)
		return
	}
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	respond(w, http.StatusOK, 0, "saved", p)
}

func (s *Server) deletePad(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePad(r.PathValue("id")); errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "document not found", nil)
	} else if err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
	} else {
		respond(w, http.StatusOK, 0, "moved to trash", nil)
	}
}

func (s *Server) listShares(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, 0, "success", s.store.ListShares())
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ObjectType     string `json:"object_type"`
		ObjectID       string `json:"object_id"`
		TTLSeconds     int64  `json:"ttl_seconds"`
		MaxAccessCount int    `json:"max_access_count"`
	}
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	if req.MaxAccessCount < 0 || req.MaxAccessCount > 1_000_000 {
		respond(w, http.StatusBadRequest, 400, "max_access_count is out of range", nil)
		return
	}
	name, err := s.shareObjectName(req.ObjectType, req.ObjectID)
	if err != nil {
		respond(w, http.StatusNotFound, 404, "object not found", nil)
		return
	}
	exp, err := expiryFromSeconds(req.TTLSeconds)
	if err != nil {
		respond(w, http.StatusBadRequest, 400, err.Error(), nil)
		return
	}
	token, err := storage.NewSecureToken()
	if err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	link := model.ShareLink{ID: storage.NewID("shr"), Token: token, ObjectType: req.ObjectType, ObjectID: req.ObjectID, Name: name, MaxAccessCount: req.MaxAccessCount, ExpiresAt: exp, CreatedAt: time.Now(), Status: "active"}
	link.URL = s.safeBaseURL(r) + "/s/" + token
	if err := s.store.CreateShare(link); err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	respond(w, http.StatusCreated, 0, "share created", link)
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeShare(r.PathValue("id")); errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "share not found", nil)
	} else if err != nil {
		respond(w, http.StatusInternalServerError, 500, err.Error(), nil)
	} else {
		respond(w, http.StatusOK, 0, "revoked", nil)
	}
}

func (s *Server) shareObjectName(kind, id string) (string, error) {
	switch kind {
	case "note":
		n, err := s.store.GetNote(id)
		return compactText(n.Content, 80), err
	case "file":
		f, err := s.store.GetFile(id)
		if err == nil {
			if info, statErr := os.Stat(filepath.Join(s.dataDir, f.StoragePath)); statErr != nil || info.Size() != f.FileSize {
				return "", storage.ErrNotFound
			}
		}
		return f.FileName, err
	case "pad":
		p, err := s.store.GetPad(id)
		return p.Title, err
	default:
		return "", errors.New("unsupported object type")
	}
}

func (s *Server) serveShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !s.enterShare(clientIP(r) + "|" + token) {
		http.Error(w, "too many share requests", http.StatusTooManyRequests)
		return
	}
	defer s.leaveShare(clientIP(r) + "|" + token)
	newSession := !s.validShareSession(r, token)
	link, err := s.store.LookupShareByToken(token, !newSession)
	if err != nil {
		s.writeShareError(w, err)
		return
	}
	// Validate the target, including the physical blob, before consuming a limited access.
	if err := s.validateShareTarget(link); err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodHead && newSession {
		link, err = s.store.ConsumeShare(token)
		if err != nil {
			s.writeShareError(w, err)
			return
		}
		s.setShareSession(w, link)
	}
	switch link.ObjectType {
	case "note":
		var n model.QuickNote
		if r.Method == http.MethodHead || !newSession {
			n, err = s.store.GetNote(link.ObjectID)
		} else {
			n, err = s.store.ReadNote(link.ObjectID)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		serveSharedText(w, r, link.Name, n.Content)
	case "pad":
		p, getErr := s.store.GetPad(link.ObjectID)
		if getErr != nil {
			http.NotFound(w, r)
			return
		}
		serveSharedText(w, r, p.Title, p.Content)
	case "file":
		f, getErr := s.store.GetFile(link.ObjectID)
		if getErr != nil {
			http.NotFound(w, r)
			return
		}
		s.servePublicFile(w, r, f)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) validateShareTarget(link model.ShareLink) error {
	switch link.ObjectType {
	case "note":
		_, err := s.store.GetNote(link.ObjectID)
		return err
	case "pad":
		_, err := s.store.GetPad(link.ObjectID)
		return err
	case "file":
		f, err := s.store.GetFile(link.ObjectID)
		if err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(s.dataDir, f.StoragePath))
		if err != nil || info.Size() != f.FileSize {
			return storage.ErrNotFound
		}
		return nil
	default:
		return storage.ErrNotFound
	}
}

func (s *Server) shareCookieSecure() bool {
	cfg := s.cfg.Get()
	if cfg.TLSCertFile != "" {
		return true
	}
	if u, err := url.Parse(cfg.PublicBaseURL); err == nil {
		return strings.EqualFold(u.Scheme, "https")
	}
	return false
}

func shouldCountDownload(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
	return rangeHeader == "" || strings.HasPrefix(rangeHeader, "bytes=0-")
}

func (s *Server) enterShare(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shareIP[key] >= 4 {
		return false
	}
	s.shareIP[key]++
	return true
}
func (s *Server) leaveShare(key string) {
	s.mu.Lock()
	if s.shareIP[key] <= 1 {
		delete(s.shareIP, key)
	} else {
		s.shareIP[key]--
	}
	s.mu.Unlock()
}

func (s *Server) writeShareError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrShareExpired), errors.Is(err, storage.ErrShareRevoked), errors.Is(err, storage.ErrShareExhausted):
		http.Error(w, err.Error(), http.StatusGone)
	default:
		http.Error(w, "share not found", http.StatusNotFound)
	}
}

func (s *Server) servePublicFile(w http.ResponseWriter, r *http.Request, f model.FileAsset) {
	path := filepath.Join(s.dataDir, f.StoragePath)
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != f.FileSize {
		http.Error(w, "file integrity check failed", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", f.MIMEType)
	w.Header().Set("ETag", `"sha256-`+f.FileHash+`"`)
	disposition := "attachment"
	if safeInlineMIME(f.MIMEType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+`; filename*=UTF-8''`+url.PathEscape(f.FileName))
	http.ServeContent(w, r, f.FileName, f.CreatedAt, file)
}

func serveSharedText(w http.ResponseWriter, r *http.Request, title, content string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>%s</title><style>body{max-width:900px;margin:48px auto;padding:0 24px;font:16px/1.7 system-ui;background:#f7f8fa;color:#172033}pre{white-space:pre-wrap;word-break:break-word;background:white;padding:24px;border-radius:14px;box-shadow:0 6px 28px #0001}</style><h1>%s</h1><pre>%s</pre>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(content))
}

func (s *Server) setShareSession(w http.ResponseWriter, link model.ShareLink) {
	expires := time.Now().Add(shareSessionDuration)
	if link.ExpiresAt != nil && link.ExpiresAt.Before(expires) {
		expires = *link.ExpiresAt
	}
	value, err := s.signValue(shareSession{Token: link.Token, ExpiresUnix: expires.Unix()})
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "omnishare_share", Value: value, Path: "/s/" + link.Token, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.shareCookieSecure(), Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}

func (s *Server) validShareSession(r *http.Request, token string) bool {
	cookie, err := r.Cookie("omnishare_share")
	if err != nil {
		return false
	}
	var session shareSession
	return s.verifySignedValue(cookie.Value, &session) == nil && session.Token == token && time.Now().Unix() < session.ExpiresUnix
}

func (s *Server) signValue(value interface{}) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.ticketKey)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifySignedValue(value string, target interface{}) error {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return errors.New("invalid signature")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, s.ticketKey)
	_, _ = mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return errors.New("invalid signature")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (s *Server) safeBaseURL(r *http.Request) string {
	cfg := s.cfg.Get()
	if cfg.PublicBaseURL != "" {
		return strings.TrimRight(cfg.PublicBaseURL, "/")
	}
	if s.validHost(r.Host) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	scheme := "http"
	if cfg.TLSCertFile != "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", scheme, cfg.Port)
}

func compactText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	r := []rune(value)
	if len(r) <= limit {
		return value
	}
	return string(r[:limit]) + "…"
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	items := s.store.ListTrash(r.URL.Query().Get("type"))
	respondPage(w, r, items)
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RestoreTrash(r.PathValue("type"), r.PathValue("id")); errors.Is(err, storage.ErrNotFound) {
		respond(w, http.StatusNotFound, 404, "trash item not found", nil)
	} else if err != nil {
		respond(w, http.StatusConflict, 409, err.Error(), nil)
	} else {
		respond(w, http.StatusOK, 0, "restored", nil)
	}
}

func (s *Server) purgeTrash(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("type"), r.PathValue("id")
	var stagedFrom, stagedTo string
	if kind == "file" {
		f, err := s.store.GetTrashedFile(id)
		if err != nil {
			respond(w, http.StatusNotFound, 404, "trash item not found", nil)
			return
		}
		if !s.store.StorageReferencedByOthers(f.StoragePath, f.ID) {
			stagedFrom = filepath.Join(s.dataDir, f.StoragePath)
			stagedTo = filepath.Join(s.dataDir, "uploads", ".purging", f.ID)
			if err := os.MkdirAll(filepath.Dir(stagedTo), 0o700); err != nil {
				respond(w, 500, 500, err.Error(), nil)
				return
			}
			if err := os.Rename(stagedFrom, stagedTo); err != nil && !errors.Is(err, os.ErrNotExist) {
				respond(w, 500, 500, err.Error(), nil)
				return
			}
		}
	}
	_, err := s.store.PurgeTrash(kind, id)
	if err != nil {
		if stagedTo != "" {
			_ = os.Rename(stagedTo, stagedFrom)
		}
		if errors.Is(err, storage.ErrNotFound) {
			respond(w, 404, 404, "trash item not found", nil)
		} else {
			respond(w, 500, 500, err.Error(), nil)
		}
		return
	}
	warning := ""
	if stagedTo != "" {
		if err := os.Remove(stagedTo); err != nil && !errors.Is(err, os.ErrNotExist) {
			warning = "metadata removed; physical purge is pending"
			_ = s.store.AddAudit("purge_pending", "file", id, warning, map[string]interface{}{"path": filepath.Base(stagedTo), "error": err.Error()})
		}
	}
	respond(w, http.StatusOK, 0, "permanently deleted", map[string]string{"warning": warning})
}

func (s *Server) emptyTrash(w http.ResponseWriter, _ *http.Request) {
	files, count, err := s.store.EmptyTrash()
	if errors.Is(err, storage.ErrNotFound) {
		respond(w, 200, 0, "trash already empty", map[string]int{"deleted": 0})
		return
	}
	if err != nil {
		respond(w, 500, 500, err.Error(), nil)
		return
	}
	failed := []string{}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.StoragePath] || s.store.StorageReferencedByOthers(f.StoragePath, f.ID) {
			continue
		}
		seen[f.StoragePath] = true
		if err := os.Remove(filepath.Join(s.dataDir, f.StoragePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = append(failed, f.ID)
		}
	}
	if len(failed) > 0 {
		_ = s.store.AddAudit("purge_pending", "trash", "", "some physical files could not be removed", map[string]interface{}{"file_ids": failed})
	}
	respond(w, http.StatusOK, 0, "trash emptied", map[string]interface{}{"deleted": count, "files": len(files), "physical_delete_failures": failed})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	respond(w, http.StatusOK, 0, "success", s.store.ListAudits(limit))
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	devices := summarizedLocalDevices(cfg.NodeName, cfg.Port, cfg.TLSCertFile != "", cfg.ListenAddress)
	if s.registry != nil {
		devices = append(devices, s.registry.List()...)
	}
	type result struct {
		index int
		node  model.DeviceNode
	}
	results := make(chan result, len(cfg.Peers))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, peer := range cfg.Peers {
		wg.Add(1)
		go func(index int, p model.PeerConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			node := model.DeviceNode{ID: p.ID, Hostname: p.Name, URL: p.URL, NetworkType: "manual", LastSeen: time.Now()}
			start := time.Now()
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, p.URL+"/api/v1/health", nil)
			resp, err := s.client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				var payload model.APIResponse
				decoder := json.NewDecoder(io.LimitReader(resp.Body, 64*1024))
				if resp.StatusCode == 200 && decoder.Decode(&payload) == nil && payload.Code == 0 {
					node.Online = true
				}
			}
			node.LatencyMS = time.Since(start).Milliseconds()
			results <- result{index, node}
		}(i, peer)
	}
	wg.Wait()
	close(results)
	manual := make([]model.DeviceNode, len(cfg.Peers))
	for item := range results {
		manual[item.index] = item.node
	}
	devices = append(devices, manual...)
	devices = dedupeDeviceNodes(devices)
	sort.SliceStable(devices, func(i, j int) bool {
		if devices[i].IsLocal != devices[j].IsLocal {
			return devices[i].IsLocal
		}
		return devices[i].URL < devices[j].URL
	})
	respond(w, http.StatusOK, 0, "success", devices)
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	tmp, err := os.CreateTemp(s.dataDir, "omnishare-backup-*.zip")
	if err != nil {
		respond(w, 500, 500, err.Error(), nil)
		return
	}
	name := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(name)
	if err := s.generateBackup(name); err != nil {
		respond(w, 500, 500, err.Error(), nil)
		return
	}
	f, err := os.Open(name)
	if err != nil {
		respond(w, 500, 500, err.Error(), nil)
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="omnishare-backup-`+time.Now().Format("20060102-150405")+`.zip"`)
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *Server) generateBackup(path string) error {
	configJSON, err := s.cfg.BackupJSON()
	if err != nil {
		return err
	}
	return s.store.WithSnapshot(func(stateJSON []byte, files []model.FileAsset) error {
		manifest := model.BackupManifest{FormatVersion: 1, AppVersion: Version, CreatedAt: time.Now().UTC(), StateSHA256: hexDigest(stateJSON), ConfigSHA256: hexDigest(configJSON), Files: []model.BackupFileManifest{}}
		unique := map[string]model.FileAsset{}
		for _, f := range files {
			if f.StoragePath != "" {
				unique[f.StoragePath] = f
			}
		}
		paths := make([]string, 0, len(unique))
		for p := range unique {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			f := unique[p]
			full := filepath.Join(s.dataDir, p)
			info, err := os.Stat(full)
			if err != nil {
				return fmt.Errorf("backup missing file %s: %w", f.ID, err)
			}
			if info.Size() != f.FileSize || !fileHashMatches(full, f.FileHash) {
				return fmt.Errorf("backup integrity failure for %s", f.ID)
			}
			manifest.Files = append(manifest.Files, model.BackupFileManifest{ID: f.ID, ArchivePath: filepath.ToSlash(p), Size: info.Size(), SHA256: f.FileHash})
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		zw := zip.NewWriter(out)
		writeBytes := func(name string, data []byte) error {
			h := &zip.FileHeader{Name: name, Method: zip.Deflate}
			h.SetMode(0o600)
			entry, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			_, err = entry.Write(data)
			return err
		}
		if err := writeBytes("omnishare.json", stateJSON); err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		if err := writeBytes("config.json", configJSON); err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		for _, item := range manifest.Files {
			full := filepath.Join(s.dataDir, filepath.FromSlash(item.ArchivePath))
			src, err := os.Open(full)
			if err != nil {
				_ = zw.Close()
				_ = out.Close()
				return err
			}
			header := &zip.FileHeader{Name: item.ArchivePath, Method: zip.Store}
			header.SetMode(0o600)
			entry, err := zw.CreateHeader(header)
			if err == nil {
				_, err = io.Copy(entry, src)
			}
			_ = src.Close()
			if err != nil {
				_ = zw.Close()
				_ = out.Close()
				return err
			}
		}
		manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
		if err := writeBytes("backup-manifest.json", manifestJSON); err != nil {
			_ = zw.Close()
			_ = out.Close()
			return err
		}
		if err := zw.Close(); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-OmniShare-Confirm") != "RESTORE-BACKUP" {
		respond(w, 400, 400, "restore requires X-OmniShare-Confirm: RESTORE-BACKUP", nil)
		return
	}
	max := s.cfg.Get().MaxUploadMB*1024*1024 + 64*1024*1024
	r.Body = http.MaxBytesReader(w, r.Body, max)
	reader, err := r.MultipartReader()
	if err != nil {
		respond(w, 400, 400, "multipart backup upload required", nil)
		return
	}
	var archive string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			respond(w, 400, 400, err.Error(), nil)
			return
		}
		if part.FormName() != "backup" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		tmp, err := os.CreateTemp(s.dataDir, "restore-*.zip")
		if err != nil {
			respond(w, 500, 500, err.Error(), nil)
			return
		}
		archive = tmp.Name()
		_, copyErr := io.Copy(tmp, part)
		closeErr := tmp.Close()
		_ = part.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(archive)
			respond(w, 400, 400, "could not save backup", nil)
			return
		}
		break
	}
	if archive == "" {
		respond(w, 400, 400, "backup file is required", nil)
		return
	}
	defer os.Remove(archive)
	if err := s.applyRestore(archive); err != nil {
		respond(w, 409, 409, err.Error(), nil)
		return
	}
	_ = s.store.AddAudit("restore", "backup", "", "backup restored", requestMetadata(r))
	respond(w, 200, 0, "backup restored; restart is recommended", nil)
}

func (s *Server) applyRestore(archive string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		clean := filepath.ToSlash(filepath.Clean(f.Name))
		if strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return errors.New("backup contains unsafe path")
		}
		entries[clean] = f
	}
	manifestFile, stateFile, configFile := entries["backup-manifest.json"], entries["omnishare.json"], entries["config.json"]
	if manifestFile == nil || stateFile == nil || configFile == nil {
		return errors.New("backup manifest, state or config is missing")
	}
	readZip := func(f *zip.File, limit int64) ([]byte, error) {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, limit))
	}
	manifestJSON, err := readZip(manifestFile, 4*1024*1024)
	if err != nil {
		return err
	}
	var manifest model.BackupManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return err
	}
	if manifest.FormatVersion != 1 {
		return errors.New("unsupported backup format")
	}
	stateJSON, err := readZip(stateFile, 256*1024*1024)
	if err != nil {
		return err
	}
	if hexDigest(stateJSON) != manifest.StateSHA256 {
		return errors.New("state checksum mismatch")
	}
	configJSON, err := readZip(configFile, 16*1024*1024)
	if err != nil {
		return err
	}
	if hexDigest(configJSON) != manifest.ConfigSHA256 {
		return errors.New("config checksum mismatch")
	}
	stage := filepath.Join(s.dataDir, "restore-staging-"+storage.NewID("r"))
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, item := range manifest.Files {
		f := entries[item.ArchivePath]
		if f == nil {
			return fmt.Errorf("backup file missing: %s", item.ArchivePath)
		}
		target := filepath.Join(stage, filepath.FromSlash(item.ArchivePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = src.Close()
			return err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(dst, h), src)
		_ = src.Close()
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil {
			return errors.New("could not extract backup")
		}
		if n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
			return fmt.Errorf("backup file checksum mismatch: %s", item.ArchivePath)
		}
	}
	oldState, err := s.store.Snapshot()
	if err != nil {
		return err
	}
	oldConfig, err := s.cfg.BackupJSON()
	if err != nil {
		return err
	}
	newUploads := filepath.Join(stage, "uploads")
	if err := os.MkdirAll(newUploads, 0o700); err != nil {
		return err
	}
	oldUploads := filepath.Join(s.dataDir, "uploads.pre-restore")
	_ = os.RemoveAll(oldUploads)
	currentUploads := filepath.Join(s.dataDir, "uploads")
	hadUploads := false
	if _, statErr := os.Stat(currentUploads); statErr == nil {
		hadUploads = true
		if err := os.Rename(currentUploads, oldUploads); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	restoreUploads := func() {
		_ = os.RemoveAll(currentUploads)
		if hadUploads {
			_ = os.Rename(oldUploads, currentUploads)
		} else {
			_ = os.MkdirAll(currentUploads, 0o700)
		}
	}
	if err := os.Rename(newUploads, currentUploads); err != nil {
		restoreUploads()
		return err
	}
	if err := s.store.ReplaceStateJSON(stateJSON); err != nil {
		restoreUploads()
		_ = s.store.ReplaceStateJSON(oldState)
		return err
	}
	if err := s.cfg.RestoreJSON(configJSON); err != nil {
		restoreUploads()
		_ = s.store.ReplaceStateJSON(oldState)
		_ = s.cfg.RestoreJSON(oldConfig)
		return err
	}
	if err := os.RemoveAll(oldUploads); err != nil {
		_ = s.store.AddAudit("restore_warning", "backup", "", "old uploads cleanup failed", map[string]interface{}{"error": err.Error()})
	}
	return os.MkdirAll(filepath.Join(currentUploads, ".staging"), 0o700)
}

func localDevices(name string, port int, tls bool) []model.DeviceNode {
	scheme := "http"
	if tls {
		scheme = "https"
	}
	seen := map[string]bool{}
	out := []model.DeviceNode{}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsUnspecified() {
			continue
		}
		value := ip.String()
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, model.DeviceNode{ID: "local-" + strings.ReplaceAll(value, ":", "_"), Hostname: name, IP: value, Port: port, URL: scheme + "://" + net.JoinHostPort(value, strconv.Itoa(port)), NetworkType: networkType(value), IsLocal: true, Online: true, LastSeen: time.Now()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

func networkType(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	if parsed.IsLoopback() {
		return "loopback"
	}
	if v4 := parsed.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return "tailscale"
	}
	return "lan"
}
func detectContentType(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return "text/uri-list"
	}
	if strings.Contains(trimmed, "\n") && (strings.Contains(trimmed, "```") || strings.Contains(trimmed, "func ") || strings.Contains(trimmed, "class ")) {
		return "text/x-code"
	}
	return "text/plain"
}
func extractTags(value string) []string {
	fields := strings.Fields(value)
	out := []string{}
	for _, f := range fields {
		if strings.HasPrefix(f, "#") && len([]rune(f)) > 1 {
			out = append(out, strings.Trim(strings.TrimPrefix(f, "#"), ".,，。;；:：!?！？"))
		}
	}
	return out
}
func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, tag := range tags {
		tag = strings.TrimPrefix(strings.TrimSpace(tag), "#")
		tag = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, tag)
		r := []rune(tag)
		if len(r) > 40 {
			tag = string(r[:40])
		}
		key := strings.ToLower(tag)
		if tag != "" && !seen[key] {
			seen[key] = true
			out = append(out, tag)
		}
		if len(out) >= 20 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func decodeJSON(r *http.Request, target interface{}) error {
	reader := io.LimitReader(r.Body, maxJSONBody+1)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func respond(w http.ResponseWriter, status, code int, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.APIResponse{Code: code, Message: message, Data: data})
}

func respondPage[T any](w http.ResponseWriter, r *http.Request, items []T) {
	total := len(items)
	limit := 100
	offset := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n >= 0 {
		offset = n
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	respond(w, 200, 0, "success", items[offset:end])
}

func expiryFromSeconds(seconds int64) (*time.Time, error) {
	if seconds < 0 || seconds > maxTTLSeconds {
		return nil, errors.New("ttl_seconds is out of range")
	}
	if seconds == 0 {
		return nil, nil
	}
	t := time.Now().Add(time.Duration(seconds) * time.Second)
	return &t, nil
}
func sanitizeFilename(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u202a' || r == '\u202b' || r == '\u202d' || r == '\u202e' || r == '\u2066' || r == '\u2067' || r == '\u2068' || r == '\u2069' {
			return -1
		}
		return r
	}, value)
	r := []rune(value)
	if len(r) > 240 {
		value = string(r[:240])
	}
	if value == "." || value == "" {
		return ""
	}
	return value
}
func safeInlineMIME(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return strings.HasPrefix(value, "video/") || strings.HasPrefix(value, "audio/") || value == "image/png" || value == "image/jpeg" || value == "image/gif" || value == "image/webp" || value == "image/avif"
}
func fileHashMatches(path, expected string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}
func hexDigest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func requestMetadata(r *http.Request) map[string]interface{} {
	return map[string]interface{}{"ip": clientIP(r), "method": r.Method, "path": r.URL.Path, "user_agent": compactText(r.UserAgent(), 160)}
}

var _ = mime.TypeByExtension
