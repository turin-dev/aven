package catalog

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/turin-dev/aven/internal/store"
)

// goldenService는 템플릿 서비스 하나를 인스턴스에 반영한 모양이다(무작위 값은 길이만 남긴다).
type goldenService struct {
	Name         string            `json:"name"`
	Icon         string            `json:"icon"`
	SourceType   string            `json:"source_type"`
	DatabaseKind string            `json:"database_kind"`
	Spec         json.RawMessage   `json:"spec"`
	Variables    map[string]string `json:"variables"`
	Volumes      []string          `json:"volumes"`
	Domains      int               `json:"domains"`
}

type goldenTemplate struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Primary     string          `json:"primary"`
	Services    []goldenService `json:"services"`
}

func shapeOf(defs, again []ServiceDef) []goldenService {
	out := []goldenService{}
	for i, d := range defs {
		in := &store.Instance{}
		in.Spec.Defaults()
		d.ApplyToInstance(in)
		spec, _ := json.Marshal(in.Spec)
		vars := map[string]string{}
		for k, v := range in.Variables {
			if again[i].Variables[k] != v {
				v = fmt.Sprintf("<random:%d>", len(v))
			}
			vars[k] = v
		}
		vols := append([]string{}, d.Volumes...)
		sort.Strings(vols)
		n := len(d.Domains)
		if d.Domain {
			n++
		}
		out = append(out, goldenService{Name: d.Name, Icon: d.Icon, SourceType: d.SourceType, DatabaseKind: d.DatabaseKind,
			Spec: spec, Variables: vars, Volumes: vols, Domains: n})
	}
	return out
}

// goldenShapes는 내장 템플릿 전체의 모양이다. 대표 서비스는 예전 규칙(도메인이 있는 첫 서비스, 없으면 첫 서비스)이다.
func goldenShapes(services func(key string) []ServiceDef) map[string]goldenTemplate {
	out := map[string]goldenTemplate{}
	for _, k := range Keys() {
		t := Templates[k]
		if t.Definition.IsHACompanion() { // HA 동반 템플릿은 단독으로 배포하지 않는다(ha_test.go)
			continue
		}
		defs := services(k)
		svcs := shapeOf(defs, services(k))
		primary := defs[0].Name
		for _, d := range defs {
			if d.Domain || len(d.Domains) > 0 {
				primary = d.Name
				break
			}
		}
		out[k] = goldenTemplate{Name: t.Name, Description: t.Description, Icon: t.Icon, Primary: primary, Services: svcs}
	}
	return out
}
