package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// exportEnvironmentTemplate: POST /api/projects/{pid}/environments/{eid}/export-template?secrets=generate|inputs
//
// 환경의 서비스로 템플릿 정의를 만든다(저장하지 않는다). 내보내지 않는 것: custom domain, TCP proxy 공개 포트,
// deploy key, 레지스트리 자격 증명, 노드 배치, 도메인 인증/경로 규칙. DB 서비스가 만든 비밀번호는 database 소스가
// 배포할 때 새로 만들고, 비밀처럼 보이는 값(이름이 SECRET|PASSWORD|TOKEN|KEY|PRIVATE이고 참조가 없는 값, 비밀번호가
// 든 URL)은 ${{ secret(32) }}로 바꾸거나(secrets=generate) 필수 입력으로 바꾼다(secrets=inputs). 변수 값과 시작/배포 전
// 명령 안에 그대로 적힌 비밀(알려진 접두사, PEM 개인 키, 엔트로피가 높은 긴 글자)도 같은 방식으로 바꾸고 경고를 남긴다.
func (s *Server) exportEnvironmentTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, p, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	mode := r.URL.Query().Get("secrets")
	if mode == "" {
		mode = "generate"
	}
	if mode != "generate" && mode != "inputs" {
		writeErr(w, 400, "secrets must be generate or inputs")
		return
	}
	svcs, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	vols, err := s.Store.ListVolumesByEnv(ctx, env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	volsByInst := map[string][]*store.Volume{}
	for _, v := range vols {
		if v.InstanceID != nil {
			volsByInst[*v.InstanceID] = append(volsByInst[*v.InstanceID], v)
		}
	}
	warnings := []string{}
	type entry struct {
		svc *store.Service
		in  *store.Instance
	}
	var list []entry
	for _, sv := range svcs {
		in, err := s.Store.GetInstanceFor(ctx, sv.ID, env.ID)
		if err != nil {
			continue
		}
		switch {
		case sv.StackID != nil || in.Compose != nil:
			warnings = append(warnings, sv.Name+": compose 스택 서비스는 내보내지 않습니다")
			continue
		case sv.DatabaseKind == nil && (in.RepoURL == nil || *in.RepoURL == "") && (in.Image == nil || *in.Image == ""):
			warnings = append(warnings, sv.Name+": 소스가 없는 서비스는 내보내지 않습니다")
			continue
		case sv.DatabaseKind != nil && catalog.Databases[*sv.DatabaseKind].Image == "":
			warnings = append(warnings, sv.Name+": 알 수 없는 데이터베이스 종류라 내보내지 않습니다")
			continue
		case sv.Name == "inputs":
			warnings = append(warnings, sv.Name+": 템플릿에서 쓸 수 없는 서비스 이름이라 내보내지 않습니다")
			continue
		}
		list = append(list, entry{sv, in})
	}
	if len(list) == 0 {
		writeErr(w, 400, "내보낼 서비스가 없습니다")
		return
	}
	d := &catalog.Definition{Version: catalog.DefinitionVersion, Name: p.Name, Description: p.Description,
		Key: sanitizeName(p.Name + "-" + env.Name)}
	if len(d.Description) > 1000 {
		d.Description = d.Description[:1000]
	}
	exported := map[string]bool{}
	for _, e := range list {
		exported[e.svc.Name] = true
	}
	for _, e := range list {
		sv, in := e.svc, e.in
		spec := catalog.ServiceSpec{Name: sv.Name, Variables: map[string]string{}}
		if sv.Icon != nil {
			spec.Icon = *sv.Icon
		}
		var base catalog.ServiceDef
		var credKeys map[string]bool
		var dbDefaults map[string]string
		switch {
		case sv.DatabaseKind != nil:
			kind := *sv.DatabaseKind
			spec.Source.Database = kind
			base, _ = catalog.DatabaseService(kind)
			credKeys = catalog.CredentialKeys(kind)
			dbDefaults = catalog.Databases[kind].Variables("")
			if in.Image != nil && *in.Image != base.Image {
				warnings = append(warnings, fmt.Sprintf("%s: 데이터베이스 이미지 %s 대신 기본 이미지 %s로 배포됩니다", sv.Name, *in.Image, base.Image))
			}
		case in.RepoURL != nil && *in.RepoURL != "":
			spec.Source.Repo = &catalog.RepoSource{URL: *in.RepoURL, Branch: in.Branch}
			if in.RootDir != "" && in.RootDir != "/" {
				spec.Source.Repo.RootDir = in.RootDir
			}
			base = catalog.ServiceDef{SourceType: "repo", RepoURL: *in.RepoURL, Branch: in.Branch, RootDir: in.RootDir}
		default:
			spec.Source.Image = *in.Image
			base = catalog.ServiceDef{SourceType: "image", Image: *in.Image}
		}
		cur := in.Spec
		cur.Defaults()
		spec.Settings = catalog.SettingsDiff(catalog.BaselineSpec(base), cur)
		redactCommandSecrets(d, mode, sv.Name, &spec.Settings, &warnings)
		// variables
		sealed := s.instanceSealed(ctx, in)
		for _, k := range sortedKeys(in.Variables) {
			v := in.Variables[k]
			if credKeys[k] {
				continue // database 소스가 배포할 때 새 비밀번호를 만든다
			}
			if sealed[k] {
				// 봉인된 값은 내보내지 않는다: inputs 모드면 배포할 때 입력받고, 아니면 새 비밀값을 만든다
				// (변수를 빼면 이 변수를 참조하는 다른 변수가 깨진다).
				if mode == "inputs" {
					ik := uniqueInputKey(d, catalog.InputKey(sv.Name, k))
					d.Inputs = append(d.Inputs, catalog.Input{Key: ik, Label: sv.Name + ": " + k, Required: true, Secret: true})
					spec.Variables[k] = "${{ inputs." + ik + " }}"
				} else {
					spec.Variables[k] = "${{ secret(32) }}"
					warnings = append(warnings, fmt.Sprintf("%s: 봉인된 변수 %s는 값을 내보내지 않고 배포할 때 새 비밀값을 만듭니다", sv.Name, k))
				}
				continue
			}
			if dv, ok := dbDefaults[k]; ok && dv == v {
				continue // DB 기본값과 같다
			}
			if missing := exportRefProblem(sv.Name, k, v, in.Variables, dbDefaults, exported); missing != "" {
				warnings = append(warnings, fmt.Sprintf("%s: 변수 %s는 템플릿 밖을 참조해서 뺐습니다(%s)", sv.Name, k, missing))
				continue
			}
			if catalog.LooksSecret(k, v) {
				if mode == "inputs" {
					ik := uniqueInputKey(d, catalog.InputKey(sv.Name, k))
					d.Inputs = append(d.Inputs, catalog.Input{Key: ik, Label: sv.Name + ": " + k, Required: true, Secret: true})
					v = "${{ inputs." + ik + " }}"
				} else {
					v = "${{ secret(32) }}"
				}
			} else if strings.Contains(v, "${{") && !exportTokensOK(v) {
				warnings = append(warnings, fmt.Sprintf("%s: 변수 %s의 ${{ }} 표현식을 템플릿에서 쓸 수 없어 뺐습니다", sv.Name, k))
				continue
			}
			// 이름이 비밀 같지 않아도 값 안에 그대로 적힌 비밀(키, 토큰, 개인 키)은 바꾼다(templates_secrets.go)
			spec.Variables[k] = redactSecretLiterals(d, mode, sv.Name, k, "변수 "+k, v, &warnings)
		}
		if len(spec.Variables) == 0 {
			spec.Variables = nil
		}
		// volumes: DB 데이터 볼륨은 database 소스가 만든다(상한이 있을 때만 적는다).
		for _, v := range volsByInst[in.ID] {
			if sv.DatabaseKind != nil && containsString(base.Volumes, v.MountPath) && v.SizeLimitMB == nil {
				continue
			}
			spec.Volumes = append(spec.Volumes, catalog.VolumeSpec{MountPath: v.MountPath, SizeLimitMB: v.SizeLimitMB})
		}
		// domains: generated만, custom domain은 내보내지 않는다.
		for _, dm := range in.Domains {
			if dm.Kind != "generated" || strings.HasPrefix(dm.Host, "*.") {
				continue
			}
			spec.Domains = append(spec.Domains, catalog.DomainSpec{Generate: true, TargetPort: dm.TargetPort})
			if len(spec.Domains) == 5 {
				break
			}
		}
		if in.TCPProxy != nil && in.TCPProxy.ContainerPort > 0 {
			spec.TCPProxy = &catalog.TCPProxySpec{ContainerPort: in.TCPProxy.ContainerPort} // 공개 포트는 배포할 때 새로 받는다
		}
		if in.Canvas != nil {
			c := *in.Canvas
			spec.Canvas = &c
		}
		d.Services = append(d.Services, spec)
	}
	for _, sv := range d.Services {
		if len(sv.Domains) > 0 {
			d.Primary = sv.Name
			break
		}
	}
	if d.Primary == "" {
		d.Primary = d.Services[0].Name
	}
	if err := d.Validate(); err != nil {
		writeValidation(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"definition": d, "warnings": warnings})
}

// exportRefProblem은 참조가 내보내는 서비스 안에서 풀리는지 본다(아니면 문제가 되는 참조).
func exportRefProblem(svc, key, v string, own, dbDefaults map[string]string, exported map[string]bool) string {
	for _, ref := range vars.References(v) {
		scope, name, cross := strings.Cut(ref, ".")
		if !cross {
			if _, ok := own[ref]; ok {
				continue
			}
			if _, ok := dbDefaults[ref]; ok {
				continue
			}
			if strings.HasPrefix(ref, "AVEN_") || strings.HasPrefix(ref, "KEEL_") || ref == "PORT" {
				continue
			}
			return ref
		}
		if scope == "shared" {
			continue
		}
		if scope == "inputs" || !exported[scope] {
			return ref
		}
		_ = name
	}
	return ""
}

// exportTokensOK: 변수 값의 ${{ }}가 모두 참조 형태인지(템플릿 함수/입력으로 잘못 해석될 표현식이 없는지).
func exportTokensOK(v string) bool {
	d := &catalog.Definition{Version: 1, Services: []catalog.ServiceSpec{{Name: "x", Source: catalog.Source{Image: "x"}, Variables: map[string]string{"V": v}}}}
	err := d.Validate()
	if err == nil {
		return true
	}
	// 참조 대상 문제는 exportRefProblem이 따로 본다. 여기서는 표현식 문법만.
	for _, p := range err.(*catalog.ValidationError).Problems {
		if strings.Contains(p, "invalid expression") || strings.Contains(p, "unknown function") || strings.Contains(p, "unknown input") ||
			strings.Contains(p, "secret") || strings.Contains(p, "randomInt") || strings.Contains(p, "uuid") {
			return false
		}
	}
	return true
}

func uniqueInputKey(d *catalog.Definition, k string) string {
	used := map[string]bool{}
	for _, in := range d.Inputs {
		used[in.Key] = true
	}
	out := k
	for i := 2; used[out]; i++ {
		out = fmt.Sprintf("%s_%d", k, i)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
