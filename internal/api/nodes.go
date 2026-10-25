package api

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// syncMesh는 mesh 원하는 상태를 백그라운드에서 다시 맞춘다(요청을 막지 않는다).
func (s *Server) syncMesh() {
	if s.Deploy == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.Deploy.SyncMesh(ctx); err != nil {
			s.Log.Warn("mesh sync", "err", err)
		}
	}()
}

func (s *Server) localNode(w http.ResponseWriter, r *http.Request) (*store.Node, bool) {
	n, err := s.Store.LocalNode(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	n.Version = s.Version
	n.DockerVersion = s.Deploy.Docker.Version(r.Context())
	n.CPUCores = runtime.NumCPU()
	applyLocalIngressEnv(&n.NodeIngress, s.localTCPProxyDomain()) // KEEL_INGRESS_MODE / KEEL_INGRESS_HOSTNAME / KEEL_TCP_PROXY_DOMAIN
	if s.Deploy.LocalIPv6PoolFromEnv() {
		n.IPv6Pool = strings.TrimSpace(s.Deploy.Cfg.IPv6Pool) // KEEL_IPV6_POOL
	}
	n.Address = store.NodeAddress{IPv4: nilIfEmpty(s.Deploy.Cfg.PublicIPv4), IPv6: nilIfEmpty(s.Deploy.Cfg.PublicIPv6)}
	if p := strings.TrimSpace(s.Deploy.Cfg.PrivateAddress); p != "" { // KEEL_PRIVATE_ADDRESS(mesh가 쓰는 주소)
		n.Address.Private = &p
	} else if host := strings.TrimSpace(s.Deploy.Cfg.NodeHostname); host != "" {
		n.Address.Private = &host
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(s.Deploy.Cfg.DataDir, &fs) == nil {
		n.DiskFreeBytes = fs.Bavail * uint64(fs.Bsize)
	}
	if mem, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(mem), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				if fields := strings.Fields(strings.TrimPrefix(line, "MemTotal:")); len(fields) > 0 {
					if kib, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
						n.MemoryBytes = kib * 1024
					}
				}
				break
			}
		}
	}
	return n, true
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	local, ok := s.localNode(w, r)
	if !ok {
		return
	}
	nodes, err := s.Store.ListNodes(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := make([]nodeView, 0, len(nodes))
	for i, n := range nodes {
		if n.IsLocal {
			nodes[i] = local
		} else {
			n.Status = s.nodeStatus(n)
		}
		out = append(out, nodeView{Node: nodes[i], Mesh: s.Deploy.MeshStatus(r.Context(), nodes[i]), IPv6Status: s.Deploy.IPv6Status(r.Context(), nodes[i].ID)})
	}
	writeJSON(w, http.StatusOK, out)
}

// nodeView는 GET /api/nodes의 항목이다: 노드와 노드 간 사설 네트워킹(mesh) 상태.
type nodeView struct {
	*store.Node
	Mesh mesh.Status `json:"mesh"`
	// IPv6Status는 도메인별 IPv6 반영 상태다(풀이 없으면 null).
	IPv6Status *deploy.IPv6NodeStatus `json:"ipv6_status"`
}

func (s *Server) patchNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("nid")
	var n *store.Node
	if id == store.LocalNodeID {
		var ok bool
		n, ok = s.localNode(w, r)
		if !ok {
			return
		}
	} else {
		var err error
		n, err = s.Store.GetNode(r.Context(), id)
		if err != nil {
			s.storeErr(w, err)
			return
		}
	}
	var patch struct {
		Name        *string            `json:"name"`
		Schedulable *bool              `json:"schedulable"`
		Labels      *map[string]string `json:"labels"`
		// ingress(공개 진입) 설정. 빈 문자열이나 null은 값을 지운다(ingress_mode는 direct로).
		IngressMode     *string `json:"ingress_mode"`
		IngressHostname *string `json:"ingress_hostname"`
		TCPHostname     *string `json:"tcp_hostname"`
		// IPv6Pool은 도메인별 공개 IPv6 풀(CIDR, /64~/120)이다. 빈 문자열이나 null은 풀을 지운다(도메인은 노드 기본 IPv6를 같이 쓴다).
		IPv6Pool *string `json:"ipv6_pool"`
	}
	if !decode(w, r, &patch) {
		return
	}
	if patch.Name != nil {
		if !nameRe.MatchString(*patch.Name) {
			writeErr(w, 400, "invalid node name")
			return
		}
		n.Name = *patch.Name
	}
	if patch.Schedulable != nil {
		n.Schedulable = *patch.Schedulable
	}
	if patch.Labels != nil {
		if *patch.Labels == nil {
			writeErr(w, 400, "labels must be an object")
			return
		}
		if len(*patch.Labels) > 32 {
			writeErr(w, 400, "too many node labels")
			return
		}
		for k, v := range *patch.Labels {
			if len(k) > 64 || len(v) > 256 {
				writeErr(w, 400, "invalid node label")
				return
			}
		}
		n.Labels = *patch.Labels
	}
	ingressSet := patch.IngressMode != nil || patch.IngressHostname != nil || patch.TCPHostname != nil
	var stored store.NodeIngress
	if ingressSet {
		// 저장 값(로컬 노드의 env 덮어쓰기 전)에서 보낸 키만 바꾼다.
		if cur, err := s.Store.GetNode(r.Context(), n.ID); err == nil {
			stored = cur.NodeIngress
		}
		if patch.IngressMode != nil {
			switch m := strings.TrimSpace(*patch.IngressMode); m {
			case "", store.IngressDirect, store.IngressCloudflareTunnel:
				stored.IngressMode = m
			default:
				writeErr(w, 400, "ingress_mode must be direct or cloudflare_tunnel")
				return
			}
		}
		for _, f := range []struct {
			in  *string
			out *string
		}{{patch.IngressHostname, &stored.IngressHostname}, {patch.TCPHostname, &stored.TCPHostname}} {
			if f.in == nil {
				continue
			}
			h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(*f.in), "."))
			if h != "" && (!domainRe.MatchString(h) || strings.HasPrefix(h, "*.")) {
				writeErr(w, 400, "invalid hostname: "+h)
				return
			}
			*f.out = h
		}
	}
	var oldPool, newPool netip.Prefix
	poolChanged := false
	if patch.IPv6Pool != nil {
		cur, err := s.Store.GetNode(r.Context(), n.ID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if p, err := node.ParseIPv6Pool(cur.IPv6Pool); err == nil {
			oldPool = p
		}
		raw := strings.TrimSpace(*patch.IPv6Pool)
		if raw != "" {
			p, err := node.ParseIPv6Pool(raw)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			if err := s.checkPoolOverlap(r.Context(), n.ID, p); err != nil {
				writeErr(w, 409, err.Error())
				return
			}
			if p != oldPool {
				if err := s.Deploy.CheckIPv6Pool(r.Context(), n.ID, p); err != nil {
					writeErr(w, 409, err.Error())
					return
				}
			}
			newPool = p
		}
		poolChanged = newPool != oldPool
	}
	if err := s.Store.UpdateNode(r.Context(), n); err != nil {
		s.storeErr(w, err)
		return
	}
	if patch.IPv6Pool != nil {
		raw := ""
		if newPool.IsValid() {
			raw = newPool.String()
		}
		if err := s.Store.SetNodeIPv6Pool(r.Context(), n.ID, raw); err != nil {
			s.storeErr(w, err)
			return
		}
		n.IPv6Pool = raw
		if n.IsLocal && s.Deploy.LocalIPv6PoolFromEnv() {
			n.IPv6Pool = strings.TrimSpace(s.Deploy.Cfg.IPv6Pool)
		}
		if poolChanged {
			s.applyIPv6PoolChange(n.ID, oldPool)
		}
	}
	if ingressSet {
		if err := s.Store.SetNodeIngress(r.Context(), n.ID, stored); err != nil {
			s.storeErr(w, err)
			return
		}
		n.NodeIngress = stored
		if n.IngressMode == "" {
			n.IngressMode = store.IngressDirect
		}
		if n.IsLocal {
			applyLocalIngressEnv(&n.NodeIngress, s.localTCPProxyDomain())
		}
	}
	if patch.Schedulable != nil && *patch.Schedulable && n.Draining {
		if err := s.Store.SetNodeDraining(r.Context(), n.ID, false); err != nil {
			s.storeErr(w, err)
			return
		}
		n.Draining = false
	}
	if !n.IsLocal {
		n.Status = s.nodeStatus(n)
	}
	writeJSON(w, 200, n)
}

// checkPoolOverlap은 pool이 다른 노드의 풀(로컬 노드는 KEEL_IPV6_POOL 포함)과 겹치지 않는지 본다.
func (s *Server) checkPoolOverlap(ctx context.Context, nodeID string, pool netip.Prefix) error {
	nodes, err := s.Store.ListNodes(ctx)
	if err != nil {
		return err
	}
	for _, o := range nodes {
		if o.ID == store.NormNode(nodeID) {
			continue
		}
		if op, ok := s.Deploy.NodeIPv6Pool(ctx, o.ID); ok && op.Overlaps(pool) {
			return fmt.Errorf("IPv6 풀 %s가 노드 %s의 풀 %s와 겹칩니다", pool, o.Name, op)
		}
	}
	return nil
}

// applyIPv6PoolChange는 풀을 바꾼 뒤 백그라운드로 주소를 다시 배정·반영하고, 예전 풀에 남은 aven 주소를 거둔다.
func (s *Server) applyIPv6PoolChange(nodeID string, old netip.Prefix) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.Deploy.SyncDomainIPv6(ctx); err != nil {
			s.Log.Warn("domain ipv6 sync after pool change", "node", nodeID, "err", err)
		}
		if old.IsValid() {
			if err := s.Deploy.ReleaseIPv6Pool(ctx, nodeID, old); err != nil {
				s.Log.Warn("release old ipv6 pool", "node", nodeID, "pool", old, "err", err)
			}
		}
	}()
}

func (s *Server) createJoinToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		TTLS int    `json:"ttl_s"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Name == "" {
		body.Name = "node-" + store.NewID("join")[5:13]
	}
	if !nameRe.MatchString(body.Name) {
		writeErr(w, 400, "invalid node name")
		return
	}
	if body.TTLS == 0 {
		body.TTLS = 3600
	}
	if body.TTLS < 60 || body.TTLS > 86400 {
		writeErr(w, 400, "ttl_s must be 60..86400")
		return
	}
	if !validPublicURL(s.PublicURL) {
		writeErr(w, 503, "KEEL_PUBLIC_URL must be an HTTPS URL before joining nodes")
		return
	}
	token, err := agentprotocol.Secret()
	if err != nil {
		s.storeErr(w, err)
		return
	}
	expires := time.Now().UTC().Add(time.Duration(body.TTLS) * time.Second)
	if err := s.Store.CreateJoinToken(r.Context(), agentprotocol.Hash(token), body.Name, expires); err != nil {
		s.storeErr(w, err)
		return
	}
	// 토큰은 이 응답에서만 1회 돌려준다(저장은 해시만, 서버 로그에도 남기지 않음).
	// command는 설치 스크립트 한 줄이다. 스크립트는 토큰을 root 전용 env 파일(0600)로만 agent에 넘기고, 가입 후 지운다.
	writeJSON(w, 201, map[string]any{"token": token, "expires_at": expires, "command": joinCommand(s.PublicURL, token)})
}

// agentHeartbeat는 연결이 살아 있는지 주기적으로 확인한다(반쯤 끊긴 TCP를 빨리 offline으로 만든다).
const agentHeartbeat = 15 * time.Second

// recordNodeStatus는 노드 상태를 hub의 실제 연결 여부로 기록한다(제한 시간 있음, 최신 연결 상태가 이긴다).
func (s *Server) recordNodeStatus(nodeID string) {
	s.Agents.SetStatus(nodeID, func(online bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		status := "offline"
		if online {
			status = "online"
		}
		if err := s.Store.SetNodeStatus(ctx, nodeID, status); err != nil {
			s.Log.Warn("node status", "node", nodeID, "status", status, "err", err)
		}
	})
}

// refreshNodeInfo는 agent에 ping을 보내 주소와 자원 정보를 저장한다.
func (s *Server) refreshNodeInfo(ctx context.Context, nodeID string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var p agentprotocol.PingResult
	if err := s.Agents.Call(ctx, nodeID, agentprotocol.OpPing, struct{}{}, &p); err != nil {
		s.Log.Warn("agent ping", "node", nodeID, "err", err)
		return
	}
	addr := store.NodeAddress{IPv4: nilIfEmpty(p.IPv4), IPv6: nilIfEmpty(p.IPv6), Private: nilIfEmpty(p.Private)}
	info := store.NodeInfo{Version: p.AgentVersion, DockerVersion: p.DockerVersion, CPUCores: p.CPUCores, MemoryBytes: p.MemoryBytes, DiskFreeBytes: p.DiskFreeBytes,
		MeshPort: p.MeshPort, ExternalUpstreams: p.ExternalUpstreams}
	if err := s.Store.SetNodeInfo(ctx, nodeID, addr, info); err != nil {
		s.Log.Warn("node info", "node", nodeID, "err", err)
	}
}

// deleteNode: DELETE /api/nodes/{nid}[?force=1]
// 인스턴스가 남아 있으면 409. force=1이면 인스턴스를 offline으로 만들고(로컬 배치로 되돌린다) 노드를 지우며,
// agent 연결과 인증 정보(인증서/비밀키)는 폐기된다.
func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("nid")
	if id == store.LocalNodeID {
		writeErr(w, 400, "local node cannot be deleted")
		return
	}
	ctx := r.Context()
	if _, err := s.Store.GetNode(ctx, id); err != nil {
		s.storeErr(w, err)
		return
	}
	insts, err := s.Deploy.NodeInstances(ctx, id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(insts) > 0 {
		if r.URL.Query().Get("force") != "1" {
			writeErr(w, 409, "node still has instances; drain it (POST /api/nodes/{nid}/drain) or delete with ?force=1 to take them offline")
			return
		}
		if _, err := s.Deploy.ForceRemoveNode(ctx, id); err != nil {
			s.storeErr(w, err)
			return
		}
	} else {
		s.Deploy.StopNodeStreams(id)
	}
	if err := s.Store.DeleteNode(ctx, id); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Store.DeleteNodeMetrics(ctx, id)
	// 인증서 폐기 + 열린 연결 끊기(노드 행이 이미 없으니 인증서는 어차피 거부되지만, 기록과 연결을 확실히 정리한다).
	if err := s.revokeNodeAccess(ctx, id); err != nil {
		s.Log.Warn("revoke node certificates", "node", id, "err", err)
	}
	// 다른 노드의 mesh gateway가 이 노드를 peer에서 빼도록(폐기된 인증서는 즉시 거부) 바로 다시 맞춘다.
	s.syncMesh()
	w.WriteHeader(204)
}
