package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"omnishare/internal/aiquota"
)

const quotaPath = "/api/v1/ai-quota"

func (s *Server) registerQuota(mux *http.ServeMux) {
	local := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !localWorkbench(r) {
				respond(w, 403, 403, "请在当前设备的本机工作台管理 AI 额度", nil)
				return
			}
			s.protected(handler)(w, r)
		}
	}
	mux.HandleFunc("GET "+quotaPath, local(s.quotaList))
	mux.HandleFunc("POST "+quotaPath+"/accounts", local(s.quotaCreate))
	mux.HandleFunc("POST "+quotaPath+"/accounts/{id}/refresh", local(s.quotaRefresh))
	mux.HandleFunc("PUT "+quotaPath+"/accounts/{id}/sharing", local(s.quotaSharing))
	mux.HandleFunc("DELETE "+quotaPath+"/accounts/{id}", local(s.quotaRemove))
	mux.HandleFunc("GET "+quotaPath+"/shared", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorizedPeer(r) {
			respond(w, 403, 403, "需要已绑定设备的签名", nil)
			return
		}
		list, e := s.quota.List(true)
		s.quotaResponse(w, list, e)
	})
}
func (s *Server) quotaResponse(w http.ResponseWriter, data any, e error) {
	if e != nil {
		status := 400
		if errors.Is(e, aiquota.ErrMissing) {
			status = 404
		}
		respond(w, status, status, e.Error(), nil)
		return
	}
	respond(w, 200, 0, "OK", data)
}

type quotaDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) quotaList(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	devices := []quotaDevice{{cfg.NodeID, cfg.NodeName}}
	for _, p := range s.boundPeers() {
		devices = append(devices, quotaDevice{p.Identity.ID, peerDisplayName(p)})
	}
	target := r.URL.Query().Get("device")
	if target != "" && target != cfg.NodeID {
		p, ok := s.boundPeer(target)
		if !ok {
			respond(w, 404, 404, "设备已解绑或不存在", nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		res, e := s.peerRequest(ctx, p, http.MethodGet, quotaPath+"/shared", nil, nil)
		if e != nil {
			respond(w, 502, 502, "无法读取该设备的额度，请检查连接", nil)
			return
		}
		defer res.Body.Close()
		var packet struct {
			Code *int              `json:"code"`
			Data []aiquota.Account `json:"data"`
		}
		if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&packet) != nil || packet.Code == nil || *packet.Code != 0 || packet.Data == nil || len(packet.Data) > 48 {
			respond(w, 502, 502, "对方未提供可用的额度响应，请确认设备在线且版本支持 AI 额度", nil)
			return
		}
		respond(w, 200, 0, "OK", map[string]any{"local": false, "devices": devices, "accounts": packet.Data, "providers": aiquota.Providers()})
		return
	}
	list, e := s.quota.List(false)
	s.quotaResponse(w, map[string]any{"local": true, "devices": devices, "accounts": list, "providers": aiquota.Providers()}, e)
}
func (s *Server) quotaCreate(w http.ResponseWriter, r *http.Request) {
	var input aiquota.Input
	if e := decodeJSON(r, &input); e != nil {
		respond(w, 400, 400, "额度账号参数无效", nil)
		return
	}
	a, e := s.quota.Create(r.Context(), input)
	s.quotaResponse(w, a, e)
}
func (s *Server) quotaRefresh(w http.ResponseWriter, r *http.Request) {
	a, e := s.quota.Refresh(r.Context(), r.PathValue("id"))
	s.quotaResponse(w, a, e)
}
func (s *Server) quotaSharing(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Shared *bool `json:"shared"`
	}
	if decodeJSON(r, &input) != nil || input.Shared == nil {
		respond(w, 400, 400, "请选择额度共享状态", nil)
		return
	}
	s.quotaResponse(w, nil, s.quota.SetShared(r.PathValue("id"), *input.Shared))
}
func (s *Server) quotaRemove(w http.ResponseWriter, r *http.Request) {
	s.quotaResponse(w, nil, s.quota.Remove(r.PathValue("id")))
}
