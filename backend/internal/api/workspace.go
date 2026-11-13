package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Lists are federated once. A remote object has a stable, source-qualified ID;
// no database copies, transitive aggregation, or name-based deduplication.
type workspaceCache struct {
	Data  json.RawMessage
	At    time.Time
	Total int
}
type workspaceNode struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Online    bool      `json:"online"`
	Cached    bool      `json:"cached,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}
type sourceResult struct {
	node  workspaceNode
	data  json.RawMessage
	total int
}

var workspaceResources = map[string]bool{"dashboard": true, "notes": true, "files": true, "pads": true, "shares": true, "trash": true, "audit": true}
var qualifiedObject = regexp.MustCompile(`^(node-[0-9a-f]{20})~([A-Za-z0-9_-]{1,160})$`)

func qualifyID(node, id string) string {
	if node == "" || id == "" {
		return id
	}
	return node + "~" + id
}
func splitObject(id string) (string, string) {
	m := qualifiedObject.FindStringSubmatch(id)
	if m == nil {
		return "", id
	}
	return m[1], m[2]
}

type captureResponse struct {
	header   http.Header
	code     int
	body     bytes.Buffer
	overflow bool
}

func newCapture() *captureResponse             { return &captureResponse{header: http.Header{}, code: 200} }
func (c *captureResponse) Header() http.Header { return c.header }
func (c *captureResponse) WriteHeader(n int)   { c.code = n }
func (c *captureResponse) Write(b []byte) (int, error) {
	if c.body.Len()+len(b) > 16<<20 {
		c.overflow = true
		return 0, io.ErrShortBuffer
	}
	return c.body.Write(b)
}

func (s *Server) federated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		resource := ""
		if len(segments) >= 3 && segments[0] == "api" && segments[1] == "v1" {
			resource = segments[2]
		}
		if r.URL.Query().Get("scope") == "all" && len(segments) == 3 && workspaceResources[resource] && (r.Method == "GET" || (resource == "trash" && r.Method == "DELETE")) {
			s.protected(func(w http.ResponseWriter, r *http.Request) { s.aggregateWorkspace(w, r, next, resource) })(w, r)
			return
		}
		index := -1
		if len(segments) >= 4 && (resource == "notes" || resource == "files" || resource == "pads" || resource == "shares") {
			index = 3
		}
		if resource == "trash" && len(segments) >= 5 {
			index = 4
		}
		if len(segments) == 3 && segments[0] == "n" && segments[2] == "raw" {
			index = 1
		}
		if index >= 0 {
			node, id := splitObject(segments[index])
			if node != "" {
				segments[index] = id
				path := "/" + strings.Join(segments, "/")
				if q := r.URL.RawQuery; q != "" {
					path += "?" + q
				}
				s.protected(func(w http.ResponseWriter, r *http.Request) { s.proxyObject(w, r, node, path, nil) })(w, r)
				return
			}
		}
		if resource == "shares" && len(segments) == 3 && r.Method == "POST" {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
			r.Body = io.NopCloser(bytes.NewReader(body))
			if err == nil && len(body) <= maxJSONBody {
				var data map[string]json.RawMessage
				if json.Unmarshal(body, &data) == nil {
					var id string
					_ = json.Unmarshal(data["object_id"], &id)
					node, object := splitObject(id)
					if node != "" {
						data["object_id"], _ = json.Marshal(object)
						replacement, _ := json.Marshal(data)
						s.protected(func(w http.ResponseWriter, r *http.Request) {
							s.proxyObject(w, r, node, r.URL.RequestURI(), replacement)
						})(w, r)
						return
					}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Invalidating before and after an attempted mutation also covers uncertain
// network outcomes. An earlier list response must not repopulate a snapshot
// containing an object the user just changed or removed.
func (s *Server) invalidateWorkspaceCache(node string) {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	s.peers.cacheEpoch++
	for key := range s.peers.cache {
		if strings.HasPrefix(key, node+"/") {
			delete(s.peers.cache, key)
		}
	}
}

func workspaceTimestamp(item map[string]interface{}, resource string) time.Time {
	field := "updated_at"
	if resource == "trash" {
		field = "deleted_at"
	}
	raw := stringField(item, field)
	if raw == "" {
		raw = stringField(item, "created_at")
	}
	stamp, _ := time.Parse(time.RFC3339Nano, raw)
	return stamp
}

func peerActionTimeout(method, path string) time.Duration {
	path = strings.SplitN(path, "?", 2)[0]
	if (method == http.MethodGet || method == http.MethodHead) &&
		strings.HasPrefix(path, "/api/v1/files/") &&
		(strings.HasSuffix(path, "/download") || strings.HasSuffix(path, "/stream")) {
		return 30 * time.Minute
	}
	return 18 * time.Second
}

func (s *Server) sourceData(ctx context.Context, r *http.Request, next http.Handler, resource string, p *peerBinding) sourceResult {
	cfg := s.cfg.Get()
	node := workspaceNode{ID: cfg.NodeID, Name: cfg.NodeName, Online: true, UpdatedAt: time.Now().UTC()}
	if p != nil {
		node.ID = p.Identity.ID
		node.Name = p.Identity.Name
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.invalidateWorkspaceCache(node.ID)
		defer s.invalidateWorkspaceCache(node.ID)
	}
	s.peers.mu.Lock()
	epoch := s.peers.cacheEpoch
	s.peers.mu.Unlock()
	q := r.URL.Query()
	q.Del("scope")
	q.Del("offset")
	q.Del("limit")
	key := node.ID + "/" + resource + "?" + q.Encode()
	q.Set("limit", "500")
	q.Set("scope", "local")
	items := []json.RawMessage{}
	var data json.RawMessage
	total := 0
	sourceBytes := 0
	var failure error
	revoked := false
	for offset := 0; offset < 10000; offset += 500 {
		q.Set("offset", strconv.Itoa(offset))
		path := "/api/v1/" + resource + "?" + q.Encode()
		var raw []byte
		status := 200
		headers := http.Header{}
		if p == nil {
			req := r.Clone(ctx)
			u, _ := url.Parse(path)
			req.URL = u
			rec := newCapture()
			next.ServeHTTP(rec, req)
			status, headers, raw = rec.code, rec.header, rec.body.Bytes()
			if rec.overflow {
				failure = errors.New("response is too large")
				break
			}
		} else {
			resp, err := s.peerRequest(ctx, *p, r.Method, path, nil, nil)
			if err != nil {
				failure = err
				break
			}
			status, headers = resp.StatusCode, resp.Header
			raw, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
			resp.Body.Close()
			if err != nil || len(raw) > 16<<20 {
				failure = errors.New("response is too large")
				break
			}
		}
		var packet struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if status != 200 || json.Unmarshal(raw, &packet) != nil || packet.Code != 0 {
			if status == 401 {
				revoked = true
				failure = errors.New("设备绑定已失效或被撤销，请重新绑定")
			} else {
				failure = errors.New("设备服务返回异常（HTTP " + strconv.Itoa(status) + "）")
			}
			break
		}
		if resource == "dashboard" || r.Method != "GET" {
			data = packet.Data
			break
		}
		var page []json.RawMessage
		if json.Unmarshal(packet.Data, &page) != nil {
			failure = errors.New("invalid device list")
			break
		}
		for _, item := range page {
			if sourceBytes+len(item) > 4<<20 {
				node.Truncated = true
				break
			}
			items = append(items, item)
			sourceBytes += len(item)
		}
		if node.Truncated {
			break
		}
		total, _ = strconv.Atoi(headers.Get("X-Total-Count"))
		if total <= len(items) || len(page) == 0 {
			break
		}
		if offset+500 >= 10000 {
			node.Truncated = true
		}
	}
	if data == nil && failure == nil {
		data, _ = json.Marshal(items)
		if total < len(items) {
			total = len(items)
		}
	}
	if failure == nil && r.Method == "GET" && len(data) <= 4<<20 {
		s.peers.mu.Lock()
		size := len(data)
		for _, entry := range s.peers.cache {
			size += len(entry.Data)
		}
		if len(s.peers.cache) >= 64 || size > 32<<20 {
			for k := range s.peers.cache {
				size -= len(s.peers.cache[k].Data)
				delete(s.peers.cache, k)
				if size <= 24<<20 && len(s.peers.cache) < 64 {
					break
				}
			}
		}
		if epoch == s.peers.cacheEpoch {
			s.peers.cache[key] = workspaceCache{Data: append([]byte(nil), data...), At: node.UpdatedAt, Total: total}
		}
		s.peers.mu.Unlock()
	}
	if failure != nil {
		node.Online = false
		node.Error = peerError(failure)
		if strings.Contains(failure.Error(), "设备") {
			node.Error = failure.Error()
		}
		if r.Method == "GET" && !revoked {
			s.peers.mu.Lock()
			cached, ok := s.peers.cache[key]
			s.peers.mu.Unlock()
			if ok && time.Since(cached.At) < 15*time.Minute {
				data = cached.Data
				total = cached.Total
				node.Cached = true
				node.UpdatedAt = cached.At
			}
		}
	}
	return sourceResult{node: node, data: data, total: total}
}
func (s *Server) aggregateWorkspace(w http.ResponseWriter, r *http.Request, next http.Handler, resource string) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	peers := s.boundPeers()
	results := make([]sourceResult, len(peers)+1)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				if i > 0 {
					results[i].node = workspaceNode{ID: peers[i-1].Identity.ID, Name: peers[i-1].Identity.Name, Error: "查询超时"}
				}
				return
			}
			defer func() { <-sem }()
			var p *peerBinding
			if i > 0 {
				p = &peers[i-1]
			}
			results[i] = s.sourceData(ctx, r, next, resource, p)
		}(i)
	}
	wg.Wait()
	nodes := []workspaceNode{}
	partial := false
	all := []map[string]interface{}{}
	stats := map[string]interface{}{}
	for i, result := range results {
		nodes = append(nodes, result.node)
		if !result.node.Online || result.node.Truncated {
			partial = true
		}
		if len(result.data) == 0 {
			continue
		}
		if resource == "dashboard" || r.Method != "GET" {
			var one map[string]interface{}
			if json.Unmarshal(result.data, &one) == nil {
				for k, v := range one {
					if n, ok := v.(float64); ok {
						old, _ := stats[k].(float64)
						stats[k] = old + n
					}
				}
			}
			continue
		}
		var list []map[string]interface{}
		if json.Unmarshal(result.data, &list) != nil {
			partial = true
			continue
		}
		for _, item := range list {
			tagItem(item, result.node, i > 0)
			all = append(all, item)
		}
	}
	var data interface{} = stats
	if resource != "dashboard" && r.Method == "GET" {
		sort.SliceStable(all, func(i, j int) bool {
			a, b := all[i], all[j]
			if resource == "notes" {
				pa, _ := a["pinned"].(bool)
				pb, _ := b["pinned"].(bool)
				if pa != pb {
					return pa
				}
			}
			left, right := workspaceTimestamp(a, resource), workspaceTimestamp(b, resource)
			if left.Equal(right) {
				return stringField(a, "id")+stringField(a, "source_node_id") < stringField(b, "id")+stringField(b, "source_node_id")
			}
			return left.After(right)
		})
		total := len(all)
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		limit := 100
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && n > 0 && n <= 500 {
			limit = n
		}
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		data = all[offset:end]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "message": "success", "data": data, "workspace": map[string]interface{}{"partial": partial, "nodes": nodes, "local_node_id": s.cfg.Get().NodeID}})
}
func stringField(item map[string]interface{}, key string) string {
	value, _ := item[key].(string)
	return value
}
func tagItem(item map[string]interface{}, node workspaceNode, remote bool) {
	if remote {
		for _, key := range []string{"id", "object_id"} {
			if value := stringField(item, key); value != "" {
				item[key] = qualifyID(node.ID, value)
			}
		}
	}
	item["source_node_id"] = node.ID
	item["source_node_name"] = node.Name
	item["source_online"] = node.Online
	item["source_updated_at"] = node.UpdatedAt
}
func (s *Server) proxyObject(w http.ResponseWriter, r *http.Request, id, path string, replacement []byte) {
	if r.Header.Get("X-OmniShare-Peer") != "" {
		respond(w, 403, 403, "transitive workspace access is not allowed", nil)
		return
	}
	p, ok := s.boundPeer(id)
	if !ok {
		respond(w, 404, 404, "设备未绑定或已解绑", nil)
		return
	}
	if !peerContentPath(strings.SplitN(path, "?", 2)[0]) {
		respond(w, 400, 400, "unsupported remote action", nil)
		return
	}
	body := replacement
	var err error
	if body == nil && r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
		if err != nil || len(body) > maxJSONBody {
			respond(w, 413, 413, "request is too large", nil)
			return
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.invalidateWorkspaceCache(id)
		defer s.invalidateWorkspaceCache(id)
	}
	ctx, cancel := context.WithTimeout(r.Context(), peerActionTimeout(r.Method, path))
	defer cancel()
	resp, err := s.peerRequest(ctx, p, r.Method, path, body, r.Header)
	if err != nil {
		respond(w, 502, 502, "远端操作未确认："+peerError(err), nil)
		return
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		respond(w, 424, 424, "对方已撤销绑定，请重新绑定此设备", nil)
		return
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		allowLongTransfer(w)
		relayPeerResponse(w, resp)
		return
	}
	defer resp.Body.Close()
	var packet map[string]interface{}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&packet) != nil {
		respond(w, 502, 502, "invalid peer response", nil)
		return
	}
	if item, ok := packet["data"].(map[string]interface{}); ok {
		tagItem(item, workspaceNode{ID: id, Name: p.Identity.Name, Online: true, UpdatedAt: time.Now().UTC()}, true)
		if strings.HasSuffix(strings.SplitN(path, "?", 2)[0], "/ticket") && resp.StatusCode < 300 {
			u, e := url.Parse(stringField(item, "url"))
			if e != nil || !strings.HasPrefix(u.Path, "/media/") || u.RawQuery != "" {
				respond(w, 502, 502, "invalid remote media ticket", nil)
				return
			}
			token, e := s.signValue(peerMedia{Peer: id, Path: u.Path, Expires: time.Now().Add(9 * time.Minute).Unix()})
			if e != nil {
				respond(w, 500, 500, "ticket error", nil)
				return
			}
			item["url"] = "/peer-media/" + token
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_ = json.NewEncoder(w).Encode(packet)
}

type peerMedia struct {
	Peer    string `json:"peer"`
	Path    string `json:"path"`
	Expires int64  `json:"expires"`
}

func (s *Server) servePeerMedia(w http.ResponseWriter, r *http.Request) {
	var ticket peerMedia
	if s.verifySignedValue(r.PathValue("ticket"), &ticket) != nil || time.Now().Unix() >= ticket.Expires || !strings.HasPrefix(ticket.Path, "/media/") || strings.ContainsAny(strings.TrimPrefix(ticket.Path, "/media/"), "/?#\\") {
		http.Error(w, "media ticket expired", 410)
		return
	}
	p, ok := s.boundPeer(ticket.Peer)
	if !ok {
		http.Error(w, "device unbound", 410)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	resp, err := s.peerRequest(ctx, p, r.Method, ticket.Path, nil, r.Header)
	if err != nil {
		http.Error(w, "remote file temporarily unavailable", 502)
		return
	}
	allowLongTransfer(w)
	relayPeerResponse(w, resp)
}
