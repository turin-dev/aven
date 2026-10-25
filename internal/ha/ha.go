// Package ha는 데이터베이스 고가용성(HA) 전환의 순수 로직이다: 이미지 버전 검사, 구성원 수와 노드 배치,
// 구성원 사이의 변수 배선(clusterWiring), 엔진별 상태 확인/승격 명령과 출력 해석, failover 후보 선택.
// 서비스 생성, 배포, exec 같은 부수 효과는 api 패키지(ha.go)가 한다.
package ha

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
)

var tagVersionRe = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.\d+)*(?:[-_+].*)?$`)

// Version은 이미지 태그에서 읽은 엔진 버전이다.
type Version struct {
	Major int `json:"major"`
	Minor int `json:"minor"` // 태그에 없으면 -1
}

func (v Version) String() string {
	if v.Minor < 0 {
		return strconv.Itoa(v.Major)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// SplitImage는 이미지 참조를 저장소와 태그로 나눈다(digest는 버린다). 태그가 없으면 "".
func SplitImage(image string) (repo, tag string) {
	ref := strings.SplitN(strings.TrimSpace(image), "@", 2)[0]
	slash := strings.LastIndexByte(ref, '/')
	colon := strings.LastIndexByte(ref, ':')
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}

// NormalizeRepo는 Docker Hub 공식 이미지 이름을 짧은 이름으로 맞춘다(docker.io/library/postgres → postgres).
func NormalizeRepo(repo string) string {
	repo = strings.ToLower(repo)
	for _, p := range []string{"docker.io/library/", "index.docker.io/library/", "registry-1.docker.io/library/", "library/", "docker.io/"} {
		if strings.HasPrefix(repo, p) {
			return strings.TrimPrefix(repo, p)
		}
	}
	return repo
}

// ImageVersion은 태그의 major(.minor)를 읽는다. :latest나 이름 태그는 버전을 알 수 없어서 거절한다.
func ImageVersion(image string) (Version, error) {
	_, tag := SplitImage(image)
	if tag == "" || tag == "latest" {
		return Version{}, fmt.Errorf("이미지 %q에 버전 태그가 없습니다(:latest는 지원하지 않습니다). 예: :17-alpine처럼 major 버전을 고정하세요", image)
	}
	m := tagVersionRe.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, fmt.Errorf("이미지 태그 %q에서 버전을 알 수 없습니다. 숫자 버전 태그로 고정하세요", tag)
	}
	v := Version{Minor: -1}
	v.Major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		v.Minor, _ = strconv.Atoi(m[2])
	}
	return v, nil
}

// CheckImage는 root 이미지가 동반 템플릿의 전환 조건(저장소, major, minor 고정)을 만족하는지 검사한다.
func CheckImage(cfg *catalog.HAConversionConfig, image string) (Version, []string) {
	var probs []string
	repo, _ := SplitImage(image)
	if len(cfg.SupportedImages) > 0 {
		ok := false
		for _, s := range cfg.SupportedImages {
			if NormalizeRepo(s) == NormalizeRepo(repo) {
				ok = true
				break
			}
		}
		if !ok {
			probs = append(probs, fmt.Sprintf("이미지 %s는 HA 전환을 지원하지 않습니다(지원: %s). 다른 배포판이나 확장 이미지는 쓸 수 없습니다", repo, strings.Join(cfg.SupportedImages, ", ")))
		}
	}
	v, err := ImageVersion(image)
	if err != nil {
		return v, append(probs, err.Error())
	}
	supported := false
	for _, m := range cfg.SupportedImageMajorVersions {
		if m == v.Major {
			supported = true
		}
	}
	if !supported {
		vs := make([]string, len(cfg.SupportedImageMajorVersions))
		for i, m := range cfg.SupportedImageMajorVersions {
			vs[i] = strconv.Itoa(m)
		}
		probs = append(probs, fmt.Sprintf("버전 %d는 HA 전환을 지원하지 않습니다(지원: %s)", v.Major, strings.Join(vs, ", ")))
	}
	if cfg.PinToMinorVersion && v.Minor < 0 {
		probs = append(probs, fmt.Sprintf("이 엔진은 노드끼리 minor 버전까지 같아야 합니다. 이미지 태그를 %d.x처럼 minor까지 고정하세요", v.Major))
	}
	return v, probs
}

// Counts는 사용자가 고른 역할별 구성원 수를 선택지로 검사하고, 고르지 않은 역할은 기본값으로 채운다.
// prototypes는 동반 템플릿에 서비스 원형이 있는 역할이다. 선택지(selector)가 없는 역할은 1개로 고정한다.
func Counts(cfg *catalog.HAConversionConfig, requested map[string]int, prototypes map[string]bool) (map[string]int, error) {
	for role := range requested {
		if role != catalog.RoleReplica && role != catalog.RoleInternal && role != catalog.RoleEdge {
			return nil, fmt.Errorf("알 수 없는 역할 %q", role)
		}
		if !prototypes[role] {
			return nil, fmt.Errorf("이 HA 템플릿에는 %s 역할이 없습니다", role)
		}
	}
	out := map[string]int{}
	for _, role := range []string{catalog.RoleReplica, catalog.RoleInternal, catalog.RoleEdge} {
		if !prototypes[role] {
			continue
		}
		sel := cfg.Selector(role)
		n := 1
		if sel != nil {
			n = sel.Default()
		}
		if v, ok := requested[role]; ok {
			switch {
			case sel == nil && v != 1:
				return nil, fmt.Errorf("%s 개수는 1로 고정입니다", role)
			case sel != nil && !sel.Allows(v):
				return nil, fmt.Errorf("%s 개수는 %v 중 하나여야 합니다", role, sel.Options)
			}
			n = v
		}
		out[role] = n
	}
	return out, nil
}

// SpreadNodes는 구성원 count개를 가장 덜 쓰인 노드에 차례로 놓는다. used는 이미 놓인 구성원 수다(root 노드 등).
// 같은 부하면 candidates 순서를 따른다.
func SpreadNodes(candidates []string, used map[string]int, count int) []string {
	if count <= 0 || len(candidates) == 0 {
		return nil
	}
	load := map[string]int{}
	for k, v := range used {
		load[k] = v
	}
	out := make([]string, 0, count)
	for range count {
		best := candidates[0]
		for _, id := range candidates[1:] {
			if load[id] < load[best] {
				best = id
			}
		}
		load[best]++
		out = append(out, best)
	}
	return out
}

// Placement는 노드 목록(SpreadNodes 결과)을 노드별 개수로 묶는다. 처음 나온 노드 순서를 지킨다.
func Placement(nodes []string) ([]string, map[string]int) {
	var order []string
	n := map[string]int{}
	for _, id := range nodes {
		if n[id] == 0 {
			order = append(order, id)
		}
		n[id]++
	}
	return order, n
}

// MemberName은 원형 이름에서 실제 구성원 이름을 만든다. 원형 이름이 root 원형 이름으로 시작하면 그 뒤를
// 실제 root 이름 뒤에 붙인다(postgres-replica → <root>-replica). 여러 개인 역할은 번호를 붙인다(-1, -2, ...).
func MemberName(rootName, rootProto, proto string, ordinal int, numbered bool) string {
	suffix := "-" + proto
	if strings.HasPrefix(proto, rootProto+"-") {
		suffix = strings.TrimPrefix(proto, rootProto)
	}
	name := rootName + suffix
	if numbered {
		name += "-" + strconv.Itoa(ordinal)
	}
	return name
}

// SortedRoles는 역할 순서(root, replica, internal, edge)다.
func SortedRoles(m map[string]int) []string {
	order := map[string]int{catalog.RoleRoot: 0, catalog.RoleReplica: 1, catalog.RoleInternal: 2, catalog.RoleEdge: 3}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
	return out
}
