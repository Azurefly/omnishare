package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"omnishare/internal/durable"
	"omnishare/internal/model"
)

const deliveryMax = 16 << 20

var deliveryHex = regexp.MustCompile("^[a-f0-9]{64}$")
var deliveryPublisher = regexp.MustCompile("^[A-Za-z0-9_-]{1,64}$")
var deliveryObject = regexp.MustCompile("^[A-Za-z0-9_-]{1,160}$")
var deliveryReference = regexp.MustCompile(`\[([^\]\r\n]+)\]\(omnishare-file:(node-[0-9a-f]{20}~[A-Za-z0-9_-]{1,160})\)`)

type deliveryConfig struct {
	PublicOrigin string `json:"public_origin"`
	Management   string `json:"management"`
	Publisher    string `json:"publisher"`
	Token        string `json:"token,omitempty"`
}

type deliverySelection struct {
	Kind        string   `json:"object_type"`
	Object      string   `json:"object_id"`
	Revision    string   `json:"revision"`
	Token       string   `json:"token"`
	RequestID   string   `json:"request_id"`
	Password    string   `json:"password"`
	TTL         int64    `json:"ttl_seconds"`
	Attachments []string `json:"attachments"`
}

type deliveryAttachment struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Hash string `json:"sha256"`
}

type deliveryPreview struct {
	Title      string               `json:"title"`
	Revision   string               `json:"revision"`
	References []deliveryAttachment `json:"references"`
	article    string
}

type gatewayEntry struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	URL       string    `json:"url,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type gatewayReply struct {
	Entry   *gatewayEntry  `json:"entry"`
	Entries []gatewayEntry `json:"entries"`
	Error   string         `json:"error"`
}

func isDeliveryConsole(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return net.ParseIP(clientIP(r)).IsLoopback() &&
		(strings.EqualFold(host, "localhost") || net.ParseIP(strings.Trim(host, "[]")).IsLoopback()) &&
		(r.Header.Get("Origin") == "" || r.Header.Get("Origin") == requestOrigin(r))
}

func localDelivery(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isDeliveryConsole(r) {
			respond(w, 403, 403, "请从 OmniShare 本机应用管理公开交付", nil)
			return
		}
		next(w, r)
	}
}

func deliveryOrigin(raw string, publisher bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && !(publisher && u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) {
		return "", errors.New("请填写完整的 HTTPS 地址，管理通道也可使用 SSH 转发的本机地址")
	}
	return u.Scheme + "://" + u.Host, nil
}

func (s *Server) deliveryConfigPath() string {
	return filepath.Join(s.dataDir, "delivery-publisher.json")
}

func (s *Server) readDeliveryConfig() (deliveryConfig, error) {
	s.deliverySettingsMu.Lock()
	defer s.deliverySettingsMu.Unlock()
	return s.loadDeliveryConfig()
}

func (s *Server) loadDeliveryConfig() (deliveryConfig, error) {
	var cfg deliveryConfig
	data, err := os.ReadFile(s.deliveryConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if len(data) > 8<<10 || json.Unmarshal(data, &cfg) != nil {
		return cfg, errors.New("本机交付配置无效")
	}
	return cfg, nil
}

func (s *Server) deliverySettings(w http.ResponseWriter, r *http.Request) {
	s.deliverySettingsMu.Lock()
	defer s.deliverySettingsMu.Unlock()
	cfg, err := s.loadDeliveryConfig()
	if err != nil {
		respond(w, 500, 500, "无法读取本机交付配置", nil)
		return
	}
	if r.Method == "PUT" {
		var input deliveryConfig
		if decodeJSON(r, &input) != nil {
			respond(w, 400, 400, "交付配置无效", nil)
			return
		}
		if strings.TrimSpace(input.PublicOrigin) == "" {
			if err := os.Remove(s.deliveryConfigPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
				respond(w, 500, 500, "无法移除交付配置", nil)
				return
			}
			cfg = deliveryConfig{}
		} else {
			input.PublicOrigin, err = deliveryOrigin(input.PublicOrigin, false)
			if err == nil {
				if strings.TrimSpace(input.Management) == "" {
					input.Management = input.PublicOrigin
				} else {
					input.Management, err = deliveryOrigin(input.Management, true)
				}
			}
			if input.Token == "" && input.Publisher == cfg.Publisher && input.Management == cfg.Management && input.PublicOrigin == cfg.PublicOrigin {
				input.Token = cfg.Token
			}
			if err != nil || !deliveryHex.MatchString(input.Token) || !deliveryPublisher.MatchString(input.Publisher) {
				respond(w, 400, 400, "请填写有效的 HTTPS 领取地址、管理地址、发布设备名和 64 字符发布密钥", nil)
				return
			}
			if r.URL.Query().Get("verify") == "1" {
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				_, status, err := gatewayRequest(ctx, input, http.MethodGet, "/v1/deliveries", nil, "")
				cancel()
				switch {
				case status == http.StatusUnauthorized || status == http.StatusForbidden:
					respond(w, 422, 422, "发布凭据被拒绝，请核对发布设备名和密钥", nil)
					return
				case status == http.StatusTooManyRequests:
					respond(w, 429, 429, "交付服务器请求过多，请稍后重试", nil)
					return
				case err != nil || status != http.StatusOK:
					respond(w, 502, 502, "无法连通交付管理接口，请检查服务器地址、HTTPS 证书及管理路由", nil)
					return
				}
			}
			raw, _ := json.Marshal(input)
			if err := durable.WriteSecretFile(s.deliveryConfigPath(), raw); err != nil {
				respond(w, 500, 500, "无法保存交付配置", nil)
				return
			}
			cfg = input
		}
	}
	configured := cfg.Token != ""
	cfg.Token = ""
	respond(w, 200, 0, "交付配置", map[string]interface{}{"configured": configured, "settings": cfg})
}

func (s *Server) requireDelivery(w http.ResponseWriter) (deliveryConfig, bool) {
	cfg, err := s.readDeliveryConfig()
	if err != nil || cfg.Token == "" {
		respond(w, 412, 412, "尚未配置公开交付服务器", nil)
		return cfg, false
	}
	return cfg, true
}

func deliveryIdentity(raw string) (string, string, bool) {
	node, id := splitObject(raw)
	return node, id, deliveryObject.MatchString(id) && (node != "" || id == raw)
}

func (s *Server) readDeliveryObject(ctx context.Context, kind, raw string) (string, string, error) {
	node, id, ok := deliveryIdentity(raw)
	if !ok {
		return "", "", errors.New("交付对象无效")
	}
	var note model.QuickNote
	var pad model.PadDocument
	if node == "" || node == s.cfg.Get().NodeID {
		var err error
		switch kind {
		case "note":
			note, err = s.store.GetNote(id)
		case "pad":
			pad, err = s.store.GetPad(id)
		default:
			return "", "", errors.New("交付内容类型无效")
		}
		if err != nil {
			return "", "", errors.New("内容不存在")
		}
	} else {
		peer, ok := s.boundPeer(node)
		if !ok {
			return "", "", errors.New("内容所在设备未绑定")
		}
		var path string
		var object any
		switch kind {
		case "note":
			path = "/api/v1/notes/" + id + "/manage"
			object = &note
		case "pad":
			path = "/api/v1/pads/" + id
			object = &pad
		default:
			return "", "", errors.New("交付内容类型无效")
		}
		res, err := s.peerRequest(ctx, peer, "GET", path, nil, nil)
		if err != nil {
			return "", "", errors.New("无法读取内容所在设备")
		}
		defer res.Body.Close()
		wrapper := struct {
			Data any `json:"data"`
		}{object}
		if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 512<<10)).Decode(&wrapper) != nil {
			return "", "", errors.New("无法读取待交付内容")
		}
	}
	if kind == "note" {
		if note.ID != id || note.IsBurnAfterRead || note.MaxReadCount > 0 {
			return "", "", errors.New("受查看次数或阅后即焚保护的随手记无法公开交付")
		}
		return compactText(note.Content, 60), note.Content, nil
	}
	if pad.ID != id {
		return "", "", errors.New("协同文档无效")
	}
	return compactText(pad.Title, 80), pad.Content, nil
}

func (s *Server) deliveryFile(ctx context.Context, raw string) (model.FileAsset, error) {
	node, id, ok := deliveryIdentity(raw)
	if !ok {
		return model.FileAsset{}, errors.New("交付附件标识无效")
	}
	if node == "" {
		node = s.cfg.Get().NodeID
	}
	// Use the same protected device resolution as internal Markdown links.
	rec := newCapture()
	req, _ := http.NewRequestWithContext(ctx, "GET", "/api/v1/file-reference/"+qualifyID(node, id), nil)
	req.SetPathValue("reference", qualifyID(node, id))
	s.resolveWorkspaceFile(rec, req)
	var wrapper struct {
		Data fileReference `json:"data"`
	}
	if rec.code != 200 || json.Unmarshal(rec.body.Bytes(), &wrapper) != nil || wrapper.Data.File == nil {
		return model.FileAsset{}, errors.New("附件所在设备不可达或原件已删除")
	}
	asset := *wrapper.Data.File
	if asset.SourceFileError != "" {
		return model.FileAsset{}, errors.New(asset.SourceFileError)
	}
	if !deliveryHex.MatchString(asset.FileHash) || asset.FileSize < 0 || asset.FileSize > deliveryMax ||
		asset.FileName == "" || strings.ContainsAny(asset.FileName, "/\\\r\n\x00") {
		return model.FileAsset{}, errors.New("附件超出交付大小限制或缺少有效校验值")
	}
	return asset, nil
}

func (s *Server) previewDelivery(ctx context.Context, in deliverySelection) (deliveryPreview, error) {
	if in.Kind == "file" {
		item, err := s.deliveryFile(ctx, in.Object)
		if err != nil {
			return deliveryPreview{}, err
		}
		return deliveryPreview{Title: item.FileName, Revision: hexDigest([]byte(item.ID + "\n" + item.FileHash + "\n" + item.FileName)),
			References: []deliveryAttachment{}}, nil
	}
	title, article, err := s.readDeliveryObject(ctx, in.Kind, in.Object)
	if err != nil {
		return deliveryPreview{}, err
	}
	if len(article) == 0 || len(article) > 256<<10 {
		return deliveryPreview{}, errors.New("交付正文为空或超过 256 KiB")
	}
	if title == "" {
		title = "OmniShare 文档"
	}
	out := deliveryPreview{Title: title, Revision: hexDigest([]byte(title + "\n" + article)), References: []deliveryAttachment{}}
	seen := map[string]bool{}
	for _, match := range deliveryReference.FindAllStringSubmatch(article, 256) {
		if !seen[match[2]] {
			seen[match[2]] = true
			out.References = append(out.References, deliveryAttachment{ID: match[2], Name: match[1]})
		}
	}
	// Do not expose internal identities from workspace-file links.
	out.article = deliveryReference.ReplaceAllString(article, "$1")
	return out, nil
}

func (s *Server) prepareDelivery(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireDelivery(w); !ok {
		return
	}
	var input deliverySelection
	if decodeJSON(r, &input) != nil {
		respond(w, 400, 400, "交付请求无效", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	preview, err := s.previewDelivery(ctx, input)
	if err != nil {
		respond(w, 422, 422, err.Error(), nil)
		return
	}
	respond(w, 200, 0, "交付清单", preview)
}

var deliveryHTTP = &http.Client{Transport: &http.Transport{
	Proxy: nil, DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
	TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 90 * time.Second,
}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func gatewayRequest(ctx context.Context, cfg deliveryConfig, method, path string, body io.Reader, contentType string) (gatewayReply, int, error) {
	var result gatewayReply
	req, err := http.NewRequestWithContext(ctx, method, cfg.Management+path, body)
	if err != nil {
		return result, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("X-OmniShare-Publisher", cfg.Publisher)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := deliveryHTTP.Do(req)
	if err != nil {
		return result, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == 204 {
		return result, 204, nil
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 256<<10)).Decode(&result)
	return result, res.StatusCode, err
}

func (s *Server) openDeliveryFile(ctx context.Context, asset model.FileAsset) (io.ReadCloser, error) {
	node, id, ok := deliveryIdentity(asset.ID)
	if !ok {
		return nil, errors.New("附件标识无效")
	}
	if node == "" || node == s.cfg.Get().NodeID {
		local, err := s.store.GetFile(id)
		if err != nil || local.FileHash != asset.FileHash {
			return nil, errors.New("附件已变化")
		}
		return s.openAsset(local)
	}
	peer, ok := s.boundPeer(node)
	if !ok {
		return nil, errors.New("附件所在设备未绑定")
	}
	response, err := s.peerRequest(ctx, peer, "GET", "/api/v1/files/"+id+"/download", nil, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 || response.ContentLength >= 0 && response.ContentLength != asset.FileSize {
		_ = response.Body.Close()
		return nil, errors.New("无法读取确认过的附件")
	}
	return response.Body, nil
}

func (s *Server) publishDelivery(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.requireDelivery(w)
	if !ok {
		return
	}
	var input deliverySelection
	if decodeJSON(r, &input) != nil || !deliveryHex.MatchString(input.Token) || !deliveryHex.MatchString(input.RequestID) ||
		len(input.Password) > 128 || len(input.Attachments) > 16 || input.TTL < 60 || input.TTL > 604800 {
		respond(w, 400, 400, "交付参数无效", nil)
		return
	}
	allowLongTransfer(w)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	preview, err := s.previewDelivery(ctx, input)
	if err != nil || preview.Revision != input.Revision {
		respond(w, 409, 409, "待交付内容已变化或不可用，请重新确认清单", nil)
		return
	}
	ids := input.Attachments
	if input.Kind == "file" {
		ids = []string{input.Object}
	}
	allowed := map[string]bool{}
	for _, ref := range preview.References {
		allowed[ref.ID] = true
	}
	assets := make([]model.FileAsset, 0, len(ids))
	files := make([]deliveryAttachment, 0, len(ids))
	var size int64
	for _, id := range ids {
		if input.Kind != "file" && !allowed[id] {
			respond(w, 400, 400, "附件不在交付清单中或重复选择", nil)
			return
		}
		delete(allowed, id)
		asset, err := s.deliveryFile(ctx, id)
		if err != nil {
			respond(w, 422, 422, err.Error(), nil)
			return
		}
		if size > deliveryMax-asset.FileSize {
			respond(w, 413, 413, "交付包超过 16 MiB", nil)
			return
		}
		size += asset.FileSize
		assets = append(assets, asset)
		files = append(files, deliveryAttachment{Name: asset.FileName, Size: asset.FileSize, Hash: asset.FileHash})
	}
	manifest := map[string]interface{}{"request_id": input.RequestID, "token": input.Token, "password": input.Password,
		"title": preview.Title, "article": preview.article, "ttl_seconds": input.TTL, "files": files}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	finished := make(chan struct{})
	defer func() {
		cancel()
		_ = reader.Close()
		<-finished
	}()
	go func() {
		defer close(finished)
		err := func() error {
			header, err := form.CreateFormField("manifest")
			if err != nil {
				return err
			}
			if err = json.NewEncoder(header).Encode(manifest); err != nil {
				return err
			}
			for _, asset := range assets {
				part, err := form.CreateFormFile("file", "object")
				if err != nil {
					return err
				}
				stream, err := s.openDeliveryFile(ctx, asset)
				if err != nil {
					return err
				}
				n, err := io.Copy(part, io.LimitReader(stream, asset.FileSize+1))
				_ = stream.Close()
				if err != nil || n != asset.FileSize {
					return errors.New("附件复制未完成")
				}
			}
			return form.Close()
		}()
		_ = writer.CloseWithError(err)
	}()
	result, status, err := gatewayRequest(ctx, cfg, "POST", "/v1/deliveries", reader, form.FormDataContentType())
	if err != nil || status != 201 || result.Entry == nil || !deliveryHex.MatchString(result.Entry.ID) {
		if status >= 400 && status <= 599 && result.Error != "" {
			respond(w, status, status, result.Error, nil)
			return
		}
		respond(w, 502, 502, "无法确认发布，请检查管理通道并重试", nil)
		return
	}
	expected := fmt.Sprintf("%s/delivery/s/%s/#%s", cfg.PublicOrigin, result.Entry.ID, input.Token)
	if expected != result.Entry.URL {
		respond(w, 502, 502, "交付服务器返回的领取地址与配置不一致", nil)
		return
	}
	respond(w, 201, 0, "交付已发布", result.Entry)
}

func (s *Server) manageDeliveries(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.requireDelivery(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	path := "/v1/deliveries"
	if r.Method == "DELETE" {
		id := r.PathValue("id")
		if !deliveryHex.MatchString(id) {
			respond(w, 400, 400, "交付标识无效", nil)
			return
		}
		path += "/" + id
	}
	reply, status, err := gatewayRequest(ctx, cfg, r.Method, path, nil, "")
	if err != nil || (status != 200 && status != 204) {
		respond(w, 502, 502, "公开交付管理通道不可用，请检查地址和发布凭据", nil)
		return
	}
	if status == 204 {
		respond(w, 200, 0, "交付已撤销", nil)
		return
	}
	if reply.Entries == nil {
		reply.Entries = []gatewayEntry{}
	}
	respond(w, 200, 0, "公开交付", reply.Entries)
}
