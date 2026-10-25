package api

import (
	"fmt"
	"net/http"

	"github.com/turin-dev/aven/internal/store"
)

func (s *Server) addTCPProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	if c.in.TCPProxy == nil { // 기존 프록시의 포트 변경은 제한하지 않는다
		if err := s.checkTCPProxyPolicy(ctx, r); err != nil {
			s.policyErr(w, err)
			return
		}
	}
	var body struct {
		ContainerPort int `json:"container_port"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ContainerPort < 1 || body.ContainerPort > 65535 {
		writeErr(w, 400, "container_port must be 1..65535")
		return
	}
	if c.in.TCPProxy != nil {
		c.in.TCPProxy.ContainerPort = body.ContainerPort
	} else {
		// 공인 IP가 없는 노드(Cloudflare 터널 모드, 주소 없음)는 외부에서 TCP로 닿을 수 없다.
		if _, ok := s.Deploy.TCPProxyEndpoint(ctx, c.in); !ok {
			writeErr(w, 409, tcpNoPublicIPReason)
			return
		}
		port, err := s.Deploy.AllocateTCPPortFor(ctx, s.Deploy.HomeNode(ctx, c.in))
		if err != nil {
			writeErr(w, 503, err.Error())
			return
		}
		c.in.TCPProxy = &store.TCPProxy{ContainerPort: body.ContainerPort, PublicPort: port}
	}
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Deploy.SyncTCP(ctx, c.in.ID); err != nil {
		s.Log.Warn("tcp proxy", "err", err)
	}
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "tcp_proxy.enabled",
		fmt.Sprintf("%s (%s): TCP proxy %s:%d → %d 활성화", c.svc.Name, c.env.Name, s.Deploy.TCPProxyHostFor(ctx, c.in), c.in.TCPProxy.PublicPort, c.in.TCPProxy.ContainerPort))
	writeJSON(w, 200, s.viewInstance(r, c.svc, c.env, c.in))
}

func (s *Server) deleteTCPProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	c.in.TCPProxy = nil
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Deploy.SyncTCP(ctx, c.in.ID); err != nil {
		s.Log.Warn("tcp proxy", "err", err)
	}
	w.WriteHeader(204)
}

// patchTCPProxy: PATCH /api/services/{sid}/instances/{eid}/tcp-proxy {container_port} — 프록시 뒤의 내부 포트만 바꾼다
// (공개 포트는 그대로라 접속 주소가 바뀌지 않는다).
func (s *Server) patchTCPProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body struct {
		ContainerPort int `json:"container_port"`
	}
	if !decode(w, r, &body) {
		return
	}
	if c.in.TCPProxy == nil {
		writeErr(w, 404, "TCP proxy is not enabled")
		return
	}
	if body.ContainerPort < 1 || body.ContainerPort > 65535 {
		writeErr(w, 400, "container_port must be 1..65535")
		return
	}
	prev := c.in.TCPProxy.ContainerPort
	c.in.TCPProxy.ContainerPort = body.ContainerPort
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Deploy.SyncTCP(ctx, c.in.ID); err != nil {
		s.Log.Warn("tcp proxy", "err", err)
	}
	if prev != body.ContainerPort {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "tcp_proxy.updated",
			fmt.Sprintf("%s (%s): TCP proxy 내부 포트 %d → %d", c.svc.Name, c.env.Name, prev, body.ContainerPort))
	}
	writeJSON(w, 200, s.viewInstance(r, c.svc, c.env, c.in))
}

// getTCPProxy: GET /api/services/{sid}/instances/{eid}/tcp-proxy — 접속 호스트, 포트, 이 노드에서 쓸 수 있는지(GraphQL tcpProxies).
func (s *Server) getTCPProxy(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.tcpProxyInfo(r.Context(), c.in))
}
