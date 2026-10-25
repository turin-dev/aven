package catalog

import (
	"fmt"
	"strings"
)

// ---- 고가용성(HA) 동반 템플릿 선언 ----
//

// 동반(HA) 템플릿을 가리키고, 동반 템플릿의 root 서비스가 revertsToTemplateCode와 haConversionConfig를 선언한다.
// 동반 템플릿의 모든 서비스는 clusterRole(root | replica | internal | edge)을 갖는다. 전환할 때 root는 이미 있는
// 서비스를 그대로 쓰고(데이터, 볼륨, 서비스 ID 유지), 나머지 역할은 서비스 원형으로 쓰여 고른 개수만큼 만들어진다.
// clusterWiring은 구성원이 서로를 찾는 변수 이름이다(노드를 더하거나 빼면 Aven이 다시 채운다).

// HA는 서비스 정의의 HA 선언이다(ServiceSpec에 평평하게 들어간다).
type HA struct {
	HATemplateCode        string              `json:"haTemplateCode,omitempty"`
	RevertsToTemplateCode string              `json:"revertsToTemplateCode,omitempty"`
	ClusterRole           string              `json:"clusterRole,omitempty"`
	HAConversionConfig    *HAConversionConfig `json:"haConversionConfig,omitempty"`
	ClusterWiring         *ClusterWiring      `json:"clusterWiring,omitempty"`
}

// HA 클러스터 역할
const (
	RoleRoot     = "root"
	RoleReplica  = "replica"
	RoleInternal = "internal"
	RoleEdge     = "edge"
)

// HAConversionConfig는 동반 템플릿 root 서비스가 선언하는 전환 설정이다.
// 개수는 구성원 수다(replica 개수에 root는 들어가지 않는다).
type HAConversionConfig struct {
	Description string      `json:"description,omitempty"`
	Replica     *HASelector `json:"replica,omitempty"`
	Internal    *HASelector `json:"internal,omitempty"`
	Edge        *HASelector `json:"edge,omitempty"`
	// SupportedImageMajorVersions는 전환할 수 있는 root 이미지 major다. 태그에서 major를 알 수 없으면(:latest) 전환하지 않는다.
	SupportedImageMajorVersions []int `json:"supportedImageMajorVersions,omitempty"`
	// PinToMinorVersion이면 root 태그에 minor가 있어야 한다(복제 형식이 minor마다 다른 엔진).
	PinToMinorVersion bool `json:"pinToMinorVersion,omitempty"`
	// SupportedImages는 root가 쓸 수 있는 이미지 저장소다(예: postgres, docker.io/library/postgres). 비우면 검사하지 않는다.
	SupportedImages []string `json:"supportedImages,omitempty"`
}

// HASelector는 한 역할에서 고를 수 있는 구성원 수다.
type HASelector struct {
	Label        string `json:"label"`
	NodeLabel    string `json:"nodeLabel"`
	Description  string `json:"description,omitempty"`
	Options      []int  `json:"options"`
	DefaultValue int    `json:"defaultValue,omitempty"`
}

// Default는 기본 개수다(defaultValue가 없으면 첫 선택지).
func (s *HASelector) Default() int {
	if s.DefaultValue > 0 {
		return s.DefaultValue
	}
	if len(s.Options) > 0 {
		return s.Options[0]
	}
	return 1
}

// Allows는 n이 선택지 중 하나인지 알려준다.
func (s *HASelector) Allows(n int) bool {
	for _, o := range s.Options {
		if o == n {
			return true
		}
	}
	return false
}

// Selector는 역할의 선택지다(없으면 nil).
func (c *HAConversionConfig) Selector(role string) *HASelector {
	switch role {
	case RoleReplica:
		return c.Replica
	case RoleInternal:
		return c.Internal
	case RoleEdge:
		return c.Edge
	}
	return nil
}

// ClusterWiring은 구성원 사이를 잇는 변수 이름이다. 형식 문자열에는 {host}(구성원의 사설 도메인 참조),
// {name}(구성원 서비스 이름), {rootName}(root 서비스의 실제 이름), {port}를 쓸 수 있다.
// 템플릿 서비스 이름을 형식에 직접 쓰지 말고 {rootName}을 쓴다(전환 뒤 root 이름이 바뀔 수 있다).
type ClusterWiring struct {
	InternalNodeNameVariable string `json:"internalNodeNameVariable,omitempty"` // internal 구성원: 자기 이름
	InternalNodesVariable    string `json:"internalNodesVariable,omitempty"`    // internal 구성원: 모든 internal 구성원 목록
	InternalNodeFormat       string `json:"internalNodeFormat,omitempty"`
	CoordinatorHostsVariable string `json:"coordinatorHostsVariable,omitempty"` // 데이터 노드: internal 구성원 주소 목록
	CoordinatorPort          int    `json:"coordinatorPort,omitempty"`
	CoordinatorHostFormat    string `json:"coordinatorHostFormat,omitempty"`
	ReplicaNodeNameVariable  string `json:"replicaNodeNameVariable,omitempty"` // replica: 자기 이름
	DataNodesVariable        string `json:"dataNodesVariable,omitempty"`       // edge: 데이터 노드 주소 목록
	DataNodeFormat           string `json:"dataNodeFormat,omitempty"`
	// Aven 확장: 조정 노드(etcd) 대신 Aven 제어부가 주 노드를 정하는 엔진에 쓴다.
	RoleVariable        string `json:"roleVariable,omitempty"`        // 데이터 노드: primary | replica
	PrimaryHostVariable string `json:"primaryHostVariable,omitempty"` // 데이터 노드: 현재 주 노드 주소
	PrimaryHostFormat   string `json:"primaryHostFormat,omitempty"`
	PeerHostsVariable   string `json:"peerHostsVariable,omitempty"` // 데이터 노드: 모든 데이터 노드 주소 목록
	PeerHostFormat      string `json:"peerHostFormat,omitempty"`
	QuorumVariable      string `json:"quorumVariable,omitempty"` // 데이터 노드: 데이터 노드 과반수
	RejoinVariable      string `json:"rejoinVariable,omitempty"` // 데이터 노드: 강등된 주 노드가 다시 합류하는 방법(standby | reclone)
	// EdgePrimaryOnly면 edge 목록(dataNodesVariable)에 현재 주 노드만 넣는다. edge가 주 노드를 스스로 가려내지
	// 못하는 엔진(Postgres)에서 쓰고, failover 때 Aven이 목록을 바꿔 edge를 다시 배포한다.
	EdgePrimaryOnly bool `json:"edgePrimaryOnly,omitempty"`
}

// HACompanionRoot는 동반 템플릿의 root 서비스다(동반 템플릿이 아니면 nil).
func (d *Definition) HACompanionRoot() *ServiceSpec {
	for i := range d.Services {
		if d.Services[i].ClusterRole == RoleRoot && d.Services[i].HAConversionConfig != nil {
			return &d.Services[i]
		}
	}
	return nil
}

// HAPrototype은 동반 템플릿에서 역할의 서비스 원형이다(없으면 nil).
func (d *Definition) HAPrototype(role string) *ServiceSpec {
	for i := range d.Services {
		if d.Services[i].ClusterRole == role {
			return &d.Services[i]
		}
	}
	return nil
}

// IsHACompanion은 HA 동반 템플릿인지 알려준다(단독으로 배포하지 않는다).
func (d *Definition) IsHACompanion() bool {
	for _, sv := range d.Services {
		if sv.ClusterRole != "" {
			return true
		}
	}
	return false
}

// HATemplateCode는 단독 템플릿이 가리키는 동반 템플릿 키다(첫 서비스 기준, 없으면 "").
func (d *Definition) HATemplateCode() string {
	for _, sv := range d.Services {
		if sv.HATemplateCode != "" {
			return sv.HATemplateCode
		}
	}
	return ""
}

func (d *Definition) validateHA() []string {
	var probs []string
	bad := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	roles := map[string][]*ServiceSpec{}
	for i := range d.Services {
		sv := &d.Services[i]
		pre := "service " + sv.Name + ": "
		if sv.HATemplateCode != "" && !slugRe.MatchString(sv.HATemplateCode) {
			bad("%shaTemplateCode must be a template key", pre)
		}
		if sv.RevertsToTemplateCode != "" && !slugRe.MatchString(sv.RevertsToTemplateCode) {
			bad("%srevertsToTemplateCode must be a template key", pre)
		}
		switch sv.ClusterRole {
		case "":
		case RoleRoot, RoleReplica, RoleInternal, RoleEdge:
			roles[sv.ClusterRole] = append(roles[sv.ClusterRole], sv)
		default:
			bad("%sclusterRole must be root, replica, internal or edge", pre)
		}
		if sv.HATemplateCode != "" && sv.ClusterRole != "" {
			bad("%shaTemplateCode belongs to the standalone template, not to a cluster service", pre)
		}
		if (sv.HAConversionConfig != nil || sv.ClusterWiring != nil || sv.RevertsToTemplateCode != "") && sv.ClusterRole != RoleRoot {
			bad("%shaConversionConfig, clusterWiring and revertsToTemplateCode are only valid on the root service", pre)
		}
	}
	if len(roles) == 0 {
		return probs
	}
	for i := range d.Services {
		if d.Services[i].ClusterRole == "" {
			bad("service %s: every service of an HA template needs a clusterRole", d.Services[i].Name)
		}
	}
	if len(roles[RoleRoot]) != 1 {
		bad("HA templates need exactly one root service")
		return probs
	}
	for _, role := range []string{RoleReplica, RoleInternal, RoleEdge} {
		if len(roles[role]) > 1 {
			bad("HA templates may declare only one %s service (it is copied for every %s node)", role, role)
		}
	}
	root := roles[RoleRoot][0]
	if root.Source.Database == "" {
		bad("HA root service must use a database source")
	}
	if len(roles[RoleReplica]) == 1 && roles[RoleReplica][0].Source.Database != root.Source.Database {
		bad("HA replica service must use the same database source as the root")
	}
	cfg := root.HAConversionConfig
	if cfg == nil {
		bad("HA root service must declare haConversionConfig")
		return probs
	}
	if root.RevertsToTemplateCode == "" {
		bad("HA root service must declare revertsToTemplateCode")
	}
	if len(cfg.Description) > 1000 {
		bad("haConversionConfig.description is longer than 1000 characters")
	}
	for _, role := range []string{RoleReplica, RoleInternal, RoleEdge} {
		sel := cfg.Selector(role)
		if sel == nil {
			continue
		}
		if len(roles[role]) != 1 {
			bad("haConversionConfig.%s needs a %s service", role, role)
		}
		if sel.Label == "" || len(sel.Label) > 100 || len(sel.NodeLabel) > 100 || len(sel.Description) > 1000 {
			bad("haConversionConfig.%s: label (1..100), nodeLabel (..100) and description (..1000) are required/limited", role)
		}
		if len(sel.Options) == 0 || len(sel.Options) > 16 {
			bad("haConversionConfig.%s.options must list 1..16 counts", role)
			continue
		}
		seen := map[int]bool{}
		for _, n := range sel.Options {
			if n < 1 || n > 10 || seen[n] {
				bad("haConversionConfig.%s.options must be distinct counts from 1..10", role)
				break
			}
			seen[n] = true
		}
		if sel.DefaultValue != 0 && !sel.Allows(sel.DefaultValue) {
			bad("haConversionConfig.%s.defaultValue must be one of its options", role)
		}
	}
	if len(cfg.SupportedImageMajorVersions) == 0 {
		bad("haConversionConfig.supportedImageMajorVersions must list at least one major version")
	}
	if len(cfg.SupportedImageMajorVersions) > 32 {
		bad("at most 32 supportedImageMajorVersions")
	}
	for _, m := range cfg.SupportedImageMajorVersions {
		if m < 1 || m > 999 {
			bad("supportedImageMajorVersions must be major versions from 1..999")
			break
		}
	}
	if len(cfg.SupportedImages) > 16 {
		bad("at most 16 supportedImages")
	}
	for _, img := range cfg.SupportedImages {
		if !imageNameRe.MatchString(img) || strings.ContainsAny(img, ":@") {
			bad("supportedImages must be image repositories without tag (%q)", img)
		}
	}
	if w := root.ClusterWiring; w != nil {
		for name, v := range map[string]string{
			"internalNodeNameVariable": w.InternalNodeNameVariable, "internalNodesVariable": w.InternalNodesVariable,
			"coordinatorHostsVariable": w.CoordinatorHostsVariable, "replicaNodeNameVariable": w.ReplicaNodeNameVariable,
			"dataNodesVariable": w.DataNodesVariable, "roleVariable": w.RoleVariable, "primaryHostVariable": w.PrimaryHostVariable,
			"peerHostsVariable": w.PeerHostsVariable, "quorumVariable": w.QuorumVariable, "rejoinVariable": w.RejoinVariable,
		} {
			if v != "" && !secretVarRe.MatchString(v) {
				bad("clusterWiring.%s must be a variable name", name)
			}
		}
		if w.CoordinatorPort < 0 || w.CoordinatorPort > 65535 {
			bad("clusterWiring.coordinatorPort must be 1..65535")
		}
		for name, f := range map[string]string{
			"internalNodeFormat": w.InternalNodeFormat, "coordinatorHostFormat": w.CoordinatorHostFormat,
			"dataNodeFormat": w.DataNodeFormat, "primaryHostFormat": w.PrimaryHostFormat, "peerHostFormat": w.PeerHostFormat,
		} {
			if len(f) > 512 || strings.Contains(f, "${{") {
				bad("clusterWiring.%s must be a format (up to 512 characters, {host} {name} {rootName} {port}) without references", name)
			}
		}
		if (w.InternalNodesVariable != "" || w.CoordinatorHostsVariable != "") && len(roles[RoleInternal]) == 0 {
			bad("clusterWiring internal/coordinator variables need an internal service")
		}
		if (w.DataNodesVariable != "" || w.EdgePrimaryOnly) && len(roles[RoleEdge]) == 0 {
			bad("clusterWiring.dataNodesVariable needs an edge service")
		}
	}
	return probs
}
