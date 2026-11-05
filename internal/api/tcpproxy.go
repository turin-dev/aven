package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/turin-dev/aven/internal/node"
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
		ContainerPort int       `json:"container_port"`
		AllowCIDRs    *[]string `json:"allow_cidrs"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ContainerPort < 1 || body.ContainerPort > 65535 {
		writeErr(w, 400, "container_port must be 1..65535")
		return
	}
	allow, ok := parseAllowCIDRs(w, body.AllowCIDRs)
	if !ok {
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
	if body.AllowCIDRs != nil {
		c.in.TCPProxy.AllowCIDRs = allow
	}
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.storeErr(w, err)
		return
	}
	syncErr := s.Deploy.SyncTCP(ctx, c.in.ID)
	if syncErr != nil {
		s.Log.Warn("tcp proxy", "err", syncErr)
		if errors.Is(syncErr, node.ErrTCPAllowUnsupported) {
			writeErr(w, 409, syncErr.Error())
			return
		}
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
		ContainerPort *int      `json:"container_port"`
		AllowCIDRs    *[]string `json:"allow_cidrs"`
	}
	if !decode(w, r, &body) {
		return
	}
	if c.in.TCPProxy == nil {
		writeErr(w, 404, "TCP proxy is not enabled")
		return
	}
	if body.ContainerPort == nil && body.AllowCIDRs == nil {
		writeErr(w, 400, "nothing to change: send container_port and/or allow_cidrs")
		return
	}
	if body.ContainerPort != nil && (*body.ContainerPort < 1 || *body.ContainerPort > 65535) {
		writeErr(w, 400, "container_port must be 1..65535")
		return
	}
	allow, ok := parseAllowCIDRs(w, body.AllowCIDRs)
	if !ok {
		return
	}
	prev, prevAllow := c.in.TCPProxy.ContainerPort, c.in.TCPProxy.AllowCIDRs
	if body.ContainerPort != nil {
		c.in.TCPProxy.ContainerPort = *body.ContainerPort
	}
	if body.AllowCIDRs != nil {
		c.in.TCPProxy.AllowCIDRs = allow
	}
	if err := s.Store.UpdateInstanceConfig(ctx, c.in); err != nil {
		s.storeErr(w, err)
		return
	}
	syncErr := s.Deploy.SyncTCP(ctx, c.in.ID)
	if syncErr != nil {
		s.Log.Warn("tcp proxy", "err", syncErr)
	}
	if prev != c.in.TCPProxy.ContainerPort {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "tcp_proxy.updated",
			fmt.Sprintf("%s (%s): TCP proxy 내부 포트 %d → %d", c.svc.Name, c.env.Name, prev, c.in.TCPProxy.ContainerPort))
	}
	if !slices.Equal(prevAllow, c.in.TCPProxy.AllowCIDRs) {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "tcp_proxy.allowlist_updated",
			fmt.Sprintf("%s (%s): TCP proxy 허용 IP %s", c.svc.Name, c.env.Name, describeAllow(c.in.TCPProxy.AllowCIDRs)))
		s.audit(r, "tcp_proxy.allowlist_updated", c.in.ID, fmt.Sprintf("%s (%s) TCP proxy 허용 IP: %s", c.svc.Name, c.env.Name, describeAllow(c.in.TCPProxy.AllowCIDRs)))
	}
	if syncErr != nil && errors.Is(syncErr, node.ErrTCPAllowUnsupported) {
		// The setting is saved but the node cannot enforce it; say so instead of reporting success.
		writeErr(w, 409, syncErr.Error())
		return
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

// parseAllowCIDRs validates an optional allow_cidrs body field. nil means "not sent".
func parseAllowCIDRs(w http.ResponseWriter, in *[]string) ([]string, bool) {
	if in == nil {
		return nil, true
	}
	allow, err := node.NormalizeTCPAllowCIDRs(*in)
	if err != nil {
		writeErr(w, 400, "allow_cidrs: "+err.Error())
		return nil, false
	}
	return allow, true
}

func describeAllow(allow []string) string {
	switch {
	case len(allow) == 0:
		return "서버 기본값"
	case len(allow) == 1 && allow[0] == node.TCPAllowAll:
		return "모두 허용"
	default:
		return strings.Join(allow, ", ")
	}
}
