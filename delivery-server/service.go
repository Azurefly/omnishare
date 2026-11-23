// Package delivery serves immutable, explicitly published copies using separate
// management and public handlers. It never retrieves URLs or accesses OmniShare.
package delivery

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Prefix = "/delivery"
const maxManifest = 512 << 10

var hexID = regexp.MustCompile("^[a-f0-9]{64}$")
var publisherID = regexp.MustCompile("^[a-zA-Z0-9_-]{1,64}$")

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	RequestID  string `json:"request_id"`
	Token      string `json:"token"`
	Password   string `json:"password,omitempty"`
	Title      string `json:"title"`
	Article    string `json:"article,omitempty"`
	TTLSeconds int64  `json:"ttl_seconds"`
	Files      []File `json:"files"`
}

type Entry struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	ExpiresAt time.Time `json:"expires_at"`
	Bytes     int64     `json:"bytes"`
	URL       string    `json:"url,omitempty"`
}

type multipartReader struct {
	*multipart.Reader
	total int64
}

type packet struct {
	Entry   *Entry  `json:"entry,omitempty"`
	Entries []Entry `json:"entries,omitempty"`
	Error   string  `json:"error,omitempty"`
}

type entryRecord struct {
	Entry
	Publisher    string
	Digest       string
	TokenHash    string
	PasswordSalt string
	PasswordHash string
	Article      string
	Files        []File
}

type Options struct {
	Root, PublicOrigin      string
	Publishers              map[string]string // SHA-256 verification digests, one per owner
	MaxBytes, CapacityBytes int64
	CheckSpace              func(int64) bool
}

type Service struct {
	options   Options
	key       []byte
	mu        sync.Mutex
	entries   map[string]entryRecord
	revoked   map[string]time.Time
	used      int64
	uploading chan struct{}
	downloads chan struct{}
	claims    chan struct{}
	claimMu   sync.Mutex
	attempts  map[string]attempt
}

type attempt struct {
	count   int
	expires time.Time
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func New(options Options) (*Service, error) {
	u, err := url.Parse(options.PublicOrigin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("public origin must be an HTTPS origin")
	}
	options.PublicOrigin = strings.TrimSuffix(options.PublicOrigin, "/")
	if options.Root == "" || options.MaxBytes <= maxManifest || options.CapacityBytes < options.MaxBytes || len(options.Publishers) == 0 {
		return nil, errors.New("invalid delivery storage or publisher configuration")
	}
	for id, hash := range options.Publishers {
		if !publisherID.MatchString(id) || !hexID.MatchString(hash) {
			return nil, errors.New("invalid publisher digest")
		}
	}
	if err := os.MkdirAll(options.Root, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(options.Root, "session.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err == nil {
			err = writeAtomic(keyPath, key, 0o600)
		}
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid session key")
	}
	s := &Service{options: options, key: key, entries: make(map[string]entryRecord),
		uploading: make(chan struct{}, 1), downloads: make(chan struct{}, 16),
		claims: make(chan struct{}, 4), attempts: make(map[string]attempt),
		revoked: make(map[string]time.Time)}
	if err := s.loadRevocations(); err != nil {
		return nil, err
	}
	dirs, err := os.ReadDir(options.Root)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		path := filepath.Join(options.Root, dir.Name())
		if strings.HasPrefix(dir.Name(), ".incoming-") {
			if err := os.RemoveAll(path); err != nil {
				return nil, err
			}
			continue
		}
		if !hexID.MatchString(dir.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, "entry.json"))
		var value entryRecord
		if err != nil || json.Unmarshal(data, &value) != nil || value.ID != dir.Name() || value.Bytes < 0 {
			return nil, errors.New("delivery storage contains an invalid manifest")
		}
		s.entries[value.ID] = value
		s.used += value.Bytes
	}
	s.Sweep()
	return s, nil
}

func headers(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "private, no-store")
	// Preserve the browser form POST Origin while suppressing cross-origin referrers.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
}

func reply(w http.ResponseWriter, code int, body packet) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		next.ServeHTTP(w, r)
	})
}

func (s *Service) Admin() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("POST /v1/deliveries", s.publish)
	mux.HandleFunc("GET /v1/deliveries", s.list)
	mux.HandleFunc("DELETE /v1/deliveries/{id}", s.revoke)
	return originGuard(mux)
}

func (s *Service) Public() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET "+Prefix+"/assets/claim.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, claimJS)
	})
	mux.HandleFunc("GET "+Prefix+"/assets/site.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, pageCSS)
	})
	mux.HandleFunc("GET "+Prefix+"/s/{id}/{$}", s.open)
	mux.HandleFunc("POST "+Prefix+"/s/{id}/claim", s.claim)
	mux.HandleFunc("GET "+Prefix+"/s/{id}/files/{index}", s.download)
	return originGuard(mux)
}

func (s *Service) authorized(w http.ResponseWriter, r *http.Request) (string, bool) {
	owner := r.Header.Get("X-OmniShare-Publisher")
	expected, found := s.options.Publishers[owner]
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || !ok || !hexID.MatchString(token) || !hmac.Equal([]byte(expected), []byte(digest(token))) {
		reply(w, 401, packet{Error: "发布凭据无效"})
		return "", false
	}
	return owner, true
}

func parseManifest(reader *http.Request, limit int64) (Manifest, *multipartReader, error) {
	var manifest Manifest
	parts, err := reader.MultipartReader()
	if err != nil {
		return manifest, nil, err
	}
	first, err := parts.NextPart()
	if err != nil || first.FormName() != "manifest" {
		return manifest, nil, errors.New("manifest part is required")
	}
	raw, err := io.ReadAll(io.LimitReader(first, maxManifest+1))
	if err != nil || len(raw) > maxManifest || json.Unmarshal(raw, &manifest) != nil {
		return manifest, nil, errors.New("invalid manifest")
	}
	if manifest.Title == "" || len(manifest.Title) > 512 || len(manifest.Password) > 128 || len(manifest.Article) > 256<<10 ||
		!hexID.MatchString(manifest.RequestID) || !hexID.MatchString(manifest.Token) ||
		manifest.TTLSeconds < 60 || manifest.TTLSeconds > 7*24*60*60 || len(manifest.Files) > 32 ||
		(manifest.Article == "" && len(manifest.Files) == 0) {
		return manifest, nil, errors.New("unsupported size, deadline, or manifest field")
	}
	total := int64(len(raw)) + 8192
	for _, file := range manifest.Files {
		if file.Name == "" || len(file.Name) > 512 || strings.ContainsAny(file.Name, "/\\\x00\r\n") ||
			!hexID.MatchString(file.SHA256) || file.Size < 0 || file.Size > limit-total {
			return manifest, nil, errors.New("invalid file or bundle exceeds limit")
		}
		total += file.Size
	}
	return manifest, &multipartReader{Reader: parts, total: total}, nil
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.authorized(w, r)
	if !ok {
		return
	}
	select {
	case s.uploading <- struct{}{}:
		defer func() { <-s.uploading }()
	default:
		reply(w, 429, packet{Error: "有交付正在上传，请稍后用原请求重试"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.options.MaxBytes+maxManifest+(128<<10))
	manifest, parts, err := parseManifest(r, s.options.MaxBytes)
	if err != nil {
		reply(w, 400, packet{Error: "交付清单无效或超过限制"})
		return
	}
	id := digest(owner + ":" + manifest.RequestID)
	canonical, _ := json.Marshal(manifest)
	expected := digest(string(canonical))
	tokenHash := digest(manifest.Token)
	s.mu.Lock()
	previous, exists := s.entries[id]
	_, revoked := s.revoked[id]
	overCapacity := len(s.entries) >= 128 || parts.total > s.options.CapacityBytes-s.used
	s.mu.Unlock()
	if revoked {
		reply(w, 410, packet{Error: "该交付已撤销，请创建新的交付"})
		return
	}
	if exists {
		if previous.Digest != expected || !previous.ExpiresAt.After(time.Now()) {
			reply(w, 409, packet{Error: "此发布请求已过期或对应不同内容"})
			return
		}
		s.published(w, previous.Entry, manifest.Token)
		return
	}
	if overCapacity || (s.options.CheckSpace != nil && !s.options.CheckSpace(parts.total)) {
		reply(w, 507, packet{Error: "交付服务器可用空间不足"})
		return
	}
	stage, err := os.MkdirTemp(s.options.Root, ".incoming-")
	if err != nil {
		reply(w, 507, packet{Error: "无法建立交付暂存区"})
		return
	}
	defer os.RemoveAll(stage)
	for i, file := range manifest.Files {
		part, err := parts.NextPart()
		if err != nil || part.FormName() != "file" ||
			storeFile(filepath.Join(stage, strconv.Itoa(i)), part, file) != nil {
			reply(w, 422, packet{Error: "文件上传或校验失败，交付尚未发布"})
			return
		}
		_ = part.Close()
	}
	if _, err := parts.NextPart(); !errors.Is(err, io.EOF) {
		reply(w, 400, packet{Error: "上传的文件超出交付清单"})
		return
	}
	if r.Context().Err() != nil {
		return
	}
	record := entryRecord{Entry: Entry{ID: id, Title: manifest.Title,
		ExpiresAt: time.Now().UTC().Add(time.Duration(manifest.TTLSeconds) * time.Second), Bytes: parts.total},
		Publisher: owner, Digest: expected, TokenHash: tokenHash, Article: manifest.Article, Files: manifest.Files}
	if manifest.Password != "" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			reply(w, 500, packet{Error: "无法生成领取凭据"})
			return
		}
		record.PasswordSalt = hex.EncodeToString(salt)
		record.PasswordHash = passwordVerifier(manifest.Password, record.PasswordSalt)
	}
	metadata, _ := json.Marshal(record)
	if err := writeAtomic(filepath.Join(stage, "entry.json"), metadata, 0o600); err != nil {
		reply(w, 507, packet{Error: "无法保存交付记录"})
		return
	}
	s.mu.Lock()
	err = os.Rename(stage, s.entryPath(id))
	if err == nil {
		err = syncDir(s.options.Root)
		if err != nil {
			_ = os.Rename(s.entryPath(id), stage)
		}
	}
	if err == nil {
		s.entries[id] = record
		s.used += record.Bytes
	}
	s.mu.Unlock()
	if err != nil {
		reply(w, 500, packet{Error: "交付提交未确认"})
		return
	}
	s.published(w, record.Entry, manifest.Token)
}

func storeFile(path string, reader io.Reader, file File) error {
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer dst.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(reader, file.Size+1))
	if err != nil || n != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
		return errors.New("file integrity mismatch")
	}
	if err = dst.Sync(); err != nil {
		return err
	}
	return dst.Close()
}

func (s *Service) published(w http.ResponseWriter, entry Entry, secret string) {
	entry.URL = s.options.PublicOrigin + sharePath(entry.ID) + "#" + secret
	reply(w, 201, packet{Entry: &entry})
}

func (s *Service) entryPath(id string) string { return filepath.Join(s.options.Root, id) }

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.authorized(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	items := make([]Entry, 0)
	for _, item := range s.entries {
		if _, revoked := s.revoked[item.ID]; !revoked && item.Publisher == owner && item.ExpiresAt.After(time.Now()) {
			items = append(items, item.Entry)
		}
	}
	s.mu.Unlock()
	reply(w, 200, packet{Entries: items})
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.authorized(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	item, present := s.entries[id]
	if !present || item.Publisher != owner {
		reply(w, 404, packet{Error: "交付不存在"})
		return
	}
	if err := s.tombstone(item); err != nil {
		reply(w, 500, packet{Error: "撤销未确认，请重试"})
		return
	}
	// The tombstone is durable before deleting any bytes. Cleanup failure
	// leaves the entry unavailable and the periodic sweep will retry.
	_ = s.remove(item)
	w.WriteHeader(204)
}

func (s *Service) remove(item entryRecord) error {
	// Move away the manifest atomically before opening any new downloads.
	path := s.entryPath(item.ID)
	retired := filepath.Join(s.options.Root, ".incoming-retired-"+item.ID)
	if err := os.Rename(path, retired); err != nil {
		return err
	}
	delete(s.entries, item.ID)
	s.used -= item.Bytes
	if err := syncDir(s.options.Root); err != nil {
		return err
	}
	_ = os.RemoveAll(retired)
	return nil
}

func (s *Service) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.entries {
		if _, revoked := s.revoked[item.ID]; revoked || !item.ExpiresAt.After(time.Now()) {
			_ = s.remove(item)
		}
	}
	for id, expires := range s.revoked {
		if _, present := s.entries[id]; !expires.After(time.Now()) && !present {
			if err := os.Remove(filepath.Join(s.options.Root, "revoked", id)); err == nil {
				delete(s.revoked, id)
			}
		}
	}
}

func sharePath(id string) string { return Prefix + "/s/" + id + "/" }

func passwordVerifier(password, salt string) string {
	value, _ := pbkdf2.Key(sha256.New, password, []byte(salt), 600000, 32)
	return hex.EncodeToString(value)
}

func (s *Service) lookup(id string) (entryRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.entries[id]
	_, revoked := s.revoked[id]
	return item, ok && !revoked && item.ExpiresAt.After(time.Now())
}

func (s *Service) session(id string, expiry time.Time) string {
	stamp := strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, s.key)
	_, _ = io.WriteString(mac, id+":"+stamp)
	return stamp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) claimed(r *http.Request, id string) bool {
	cookie, err := r.Cookie("omni_delivery")
	if err != nil {
		return false
	}
	stamp, _, ok := strings.Cut(cookie.Value, ".")
	until, err := strconv.ParseInt(stamp, 10, 64)
	if !ok || err != nil || time.Now().Unix() >= until {
		return false
	}
	return hmac.Equal([]byte(cookie.Value), []byte(s.session(id, time.Unix(until, 0))))
}

func (s *Service) allowClaim(id string) bool {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	now := time.Now()
	for key, item := range s.attempts {
		if !item.expires.After(now) {
			delete(s.attempts, key)
		}
	}
	item, found := s.attempts[id]
	if !found {
		if len(s.attempts) >= 512 {
			return false
		}
		item.expires = now.Add(time.Minute)
	}
	if item.count >= 15 {
		return false
	}
	item.count++
	s.attempts[id] = item
	return true
}

func (s *Service) claim(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	item, ok := s.lookup(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	origin := r.Header.Get("Origin")
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if (origin != "" && origin != s.options.PublicOrigin) || contentType != "application/x-www-form-urlencoded" {
		http.Error(w, "领取请求来源无效", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		http.Error(w, "请求无效", 400)
		return
	}
	raw := r.PostForm.Get("token")
	password := r.PostForm.Get("password")
	if !hexID.MatchString(raw) || len(password) > 128 || !hmac.Equal([]byte(digest(raw)), []byte(item.TokenHash)) {
		http.Error(w, "领取凭据无效", 403)
		return
	}
	if item.PasswordHash != "" {
		if !s.allowClaim(id) {
			http.Error(w, "领取尝试过多，请稍后重试", 429)
			return
		}
		select {
		case s.claims <- struct{}{}:
			defer func() { <-s.claims }()
		default:
			http.Error(w, "领取尝试过多，请稍后重试", 429)
			return
		}
		if !hmac.Equal([]byte(passwordVerifier(password, item.PasswordSalt)), []byte(item.PasswordHash)) {
			s.render(w, 403, page{Root: sharePath(id), Password: true, Message: "提取码不匹配，请从分享链接重新打开后再试。"})
			return
		}
	}
	until := time.Now().Add(time.Hour)
	if item.ExpiresAt.Before(until) {
		until = item.ExpiresAt
	}
	if _, active := s.lookup(id); !active {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "omni_delivery", Value: s.session(id, until), Path: sharePath(id),
		Expires: until, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, sharePath(id), 303)
}

type page struct {
	Root     string
	Password bool
	Ready    bool
	Title    string
	Article  string
	Expiry   string
	Files    []File
	Message  string
}

func (s *Service) render(w http.ResponseWriter, status int, data page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = claimPage.Execute(w, data)
}

func (s *Service) open(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	item, ok := s.lookup(id)
	if !ok {
		s.render(w, 404, page{Message: "交付已撤销、过期或不存在。"})
		return
	}
	if !s.claimed(r, id) {
		s.render(w, 200, page{Root: sharePath(id), Password: item.PasswordHash != ""})
		return
	}
	s.render(w, 200, page{Root: sharePath(id), Ready: true, Title: item.Title, Article: item.Article,
		Expiry: item.ExpiresAt.Format("2006-01-02 15:04 UTC"), Files: item.Files})
}

func (s *Service) download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	item, ok := s.lookup(id)
	index, err := strconv.Atoi(r.PathValue("index"))
	if !ok || !s.claimed(r, id) || err != nil || index < 0 || index >= len(item.Files) {
		http.NotFound(w, r)
		return
	}
	select {
	case s.downloads <- struct{}{}:
		defer func() { <-s.downloads }()
	default:
		http.Error(w, "下载繁忙，请稍后重试", 429)
		return
	}
	if strings.Contains(r.Header.Get("Range"), ",") {
		http.Error(w, "只支持单段续传", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	s.mu.Lock()
	_, revoked := s.revoked[id]
	file, err := os.Open(filepath.Join(s.entryPath(id), strconv.Itoa(index)))
	s.mu.Unlock()
	if revoked && err == nil {
		_ = file.Close()
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	meta := item.Files[index]
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": meta.Name}))
	w.Header().Set("ETag", fmt.Sprintf("%q", meta.SHA256))
	http.ServeContent(w, r, meta.Name, time.Time{}, file)
}

var claimPage = template.Must(template.New("delivery").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OmniShare · 公开交付</title><link rel="stylesheet" href="/delivery/assets/site.css">
<script src="/delivery/assets/claim.js" defer></script></head>
<body><main><header>OmniShare</header>
{{if .Ready}}<h1>{{.Title}}</h1><p class="muted">有效至 <time data-utc="{{.Expiry}}">{{.Expiry}}</time></p>
{{if .Article}}<pre>{{.Article}}</pre>{{end}}
{{if .Files}}<h2>附件</h2><ul>{{range $i,$f := .Files}}<li><a href="{{$.Root}}files/{{$i}}">{{$f.Name}}</a><span data-bytes="{{$f.Size}}">{{$f.Size}} B</span></li>{{end}}</ul>{{end}}
{{else}}<h1>领取交付</h1>
{{if .Message}}<p role="status">{{.Message}}</p>{{else}}
<form id="claim" method="post" action="{{.Root}}claim">
<input type="hidden" name="token" id="claimToken">
{{if .Password}}<label for="password">提取码</label><input type="password" name="password" id="password" required maxlength="128" autocomplete="off">{{end}}
<button id="claimSubmit" type="submit" disabled>打开交付</button>
<p id="claimStatus" role="status"></p></form>{{end}}{{end}}
</main></body></html>`))

const claimJS = `const form=document.querySelector('#claim');
if(form){
 const token=location.hash.slice(1);
 if(/^[a-f0-9]{64}$/.test(token)){
  document.querySelector('#claimToken').value=token;
  document.querySelector('#claimSubmit').disabled=false;
 }else{
  document.querySelector('#claimStatus').textContent='请打开发送者提供的完整链接';
 }
}
for(const span of document.querySelectorAll('[data-bytes]')){
 const bytes=Number(span.dataset.bytes);
 if(bytes>=1048576)span.textContent=(bytes/1048576).toFixed(1)+' MB';
 else if(bytes>=1024)span.textContent=(bytes/1024).toFixed(1)+' KB';
}`
const pageCSS = `*{box-sizing:border-box}body{margin:0;padding:clamp(16px,6vw,70px);font:15px/1.6 system-ui,sans-serif;background:#f3f5f9;color:#172036}main{max-width:760px;margin:auto;padding:clamp(16px,5vw,42px);background:#fff;border:1px solid #e0e6ef;border-radius:22px;box-shadow:0 12px 42px #16233d12}header,.muted,li span{color:#52637a}header{font-weight:650}h1{font-size:clamp(24px,6vw,40px);line-height:1.18;overflow-wrap:anywhere;margin:10px 0}h2{font-size:18px}form{display:grid;gap:10px;max-width:340px;margin-top:18px}input,button{font:inherit;padding:9px 14px;border:1px solid #bdc8da;border-radius:12px}button{background:#1558bf;color:#fff;font-weight:650;cursor:pointer}button:disabled{background:#66758c;cursor:default}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:inherit;padding:16px;background:#f4f6fb;border-radius:13px}ul{list-style:none;padding:0}li{display:flex;justify-content:space-between;gap:16px;align-items:baseline;padding:8px 10px;border-bottom:1px solid #ebeff5}a{color:#1c55ae;overflow-wrap:anywhere}li span{flex:none;font-size:12px}`
