package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/dotenv"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// ---- 봉인(sealed) 변수 ----
//
// 봉인한 변수의 값은 저장한 뒤 어떤 API로도 돌려주지 않는다(인스턴스·canvas의 variables, 해석된 변수,
// 환경 동기화 미리보기, 템플릿 내보내기, raw 편집기). 배포 때는 그대로 주입되고 다른 서비스가 참조할 수 있지만,
// 그 참조를 해석한 값도 SealedValue로 가린다. 봉인은 풀 수 없다: 지우고 다시 만들어야 한다.
// 환경 복제·동기화는 봉인 변수를 옮기지 않는다.

// SealedValue는 봉인된 변수 값 대신 보여 주는 표시다. 이 값을 그대로 다시 저장하면 원래 값이 유지된다.
const SealedValue = "<sealed>"

// sealSentinel은 해석된 값이 봉인 값에서 왔는지 찾는 표시다(변수 값에 나올 수 없는 NUL 포함).
const sealSentinel = "\x00keel-sealed\x00"

// instanceSealed는 인스턴스 변수 중 봉인된 키 집합이다. 읽지 못하면 모든 키를 봉인으로 본다(값을 흘리지 않는다).
func (s *Server) instanceSealed(ctx context.Context, in *store.Instance) map[string]bool {
	sealed, err := s.Store.SealedKeys(ctx, in.ID, in.Variables)
	if err != nil {
		s.Log.Warn("sealed variables", "instance", in.ID, "err", err)
		sealed = map[string]bool{}
		for k := range in.Variables {
			sealed[k] = true
		}
	}
	return sealed
}

// sharedSealed는 환경 공유 변수 중 봉인된 키 집합이다.
func (s *Server) sharedSealed(ctx context.Context, envID string, shared map[string]string) map[string]bool {
	sealed, err := s.Store.SealedKeys(ctx, store.SharedSealOwner(envID), shared)
	if err != nil {
		s.Log.Warn("sealed shared variables", "env", envID, "err", err)
		sealed = map[string]bool{}
		for k := range shared {
			sealed[k] = true
		}
	}
	return sealed
}

// hideSealed는 m의 복사본에서 봉인된 키의 값을 SealedValue로 바꾼다.
func hideSealed(m map[string]string, sealed map[string]bool) map[string]string {
	out := copyMap(m)
	for k := range sealed {
		if _, ok := out[k]; ok {
			out[k] = SealedValue
		}
	}
	return out
}

// withoutSealed는 봉인된 키를 뺀 복사본이다(환경 복제).
func withoutSealed(m map[string]string, sealed map[string]bool) map[string]string {
	out := copyMap(m)
	for k := range sealed {
		delete(out, k)
	}
	return out
}

// withKeys는 a와 b의 합집합이다(nil 가능).
func withKeys(a, b map[string]bool) map[string]bool {
	if len(b) == 0 {
		return a
	}
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// keepSealed는 next에서 봉인된 키 값이 SealedValue이면 old 값으로 되돌린다(가린 값을 그대로 다시 저장해도 유지).
// keepMissing이면 next에 없는 봉인 키도 old 값으로 남긴다(raw 편집기: 봉인 변수는 편집기에 나오지 않는다).
func keepSealed(old, next map[string]string, sealed map[string]bool, keepMissing bool) {
	for k := range sealed {
		ov, had := old[k]
		if !had {
			continue
		}
		nv, ok := next[k]
		if (ok && nv == SealedValue) || (!ok && keepMissing) {
			next[k] = ov
		}
	}
}

// sealRequest는 봉인을 요청한 키를 검사한다. 모두 next에 있어야 한다.
func sealRequest(keys []string, next map[string]string) error {
	if len(keys) > maxSealKeys {
		return fmt.Errorf("at most %d sealed variables per request", maxSealKeys)
	}
	for _, k := range keys {
		if _, ok := next[k]; !ok {
			return fmt.Errorf("cannot seal %s: no such variable", k)
		}
	}
	return nil
}

const maxSealKeys = 500

// resealKeys는 저장 전에 봉인을 기록할 키다: 이미 봉인된 키(값이 바뀌었을 수 있음)와 새로 봉인할 키.
// 기록은 멱등이라 저장할 때마다 다시 적어도 된다. 저장보다 먼저 적어야 바뀐 값이 잠깐이라도 보이지 않는다.
func resealKeys(sealed map[string]bool, add []string) []string {
	out := store.SortedKeys(sealed)
	out = append(out, add...)
	return out
}

// parseSealList는 PATCH의 sealed_variables(봉인할 키 목록)를 읽는다.
func parseSealList(raw json.RawMessage) ([]string, error) {
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, errors.New("sealed_variables must be an array of variable names")
	}
	return keys, nil
}

// sealedResolved는 in의 해석된 변수 중 봉인 값(자기 것, 공유 변수, 다른 서비스 참조)에서 온 키 집합이다.
func (s *Server) sealedResolved(ctx context.Context, in *store.Instance) (map[string]bool, error) {
	scope, names, err := s.Deploy.Scope(ctx, in.EnvironmentID, nil)
	if err != nil {
		return nil, err
	}
	shared := store.SharedSealOwner(in.EnvironmentID)
	owners := []string{shared}
	for id := range names {
		owners = append(owners, id)
	}
	recs, err := s.Store.SealedRecords(ctx, owners...)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if len(recs) == 0 {
		return out, nil
	}
	for k := range recs.Sealed(shared, scope.Shared) {
		scope.Shared[k] = sealSentinel
	}
	for id, name := range names {
		m := scope.Services[name]
		for k := range recs.Sealed(id, m) {
			m[k] = sealSentinel
		}
	}
	res, err := vars.Resolve(scope, names[in.ID])
	if err != nil {
		return nil, err
	}
	for k, v := range res.Vars {
		if strings.Contains(v, sealSentinel) {
			out[k] = true
		}
	}
	return out, nil
}

// hideSealedResolved는 in의 해석된 변수 m의 복사본에서 봉인 값에서 온 키를 SealedValue로 바꾼다.
// 봉인 여부를 계산하지 못하면 모든 값을 가린다(값을 흘리지 않는다).
func (s *Server) hideSealedResolved(ctx context.Context, in *store.Instance, m map[string]string) map[string]string {
	sealed, err := s.sealedResolved(ctx, in)
	if err != nil {
		s.Log.Warn("sealed resolved variables", "instance", in.ID, "err", err)
		sealed = map[string]bool{}
		for k := range m {
			sealed[k] = true
		}
	}
	return hideSealed(m, sealed)
}

// checkInstanceVars는 저장 전에 변수 이름·크기와 순환 참조를 검사한다.
func (s *Server) checkInstanceVars(ctx context.Context, in *store.Instance, next map[string]string) error {
	if err := validateVars(next); err != nil {
		return err
	}
	if _, err := s.Deploy.Resolve(ctx, in, &deploy.Override{InstanceID: in.ID, Vars: next, Spec: &in.Spec}); err != nil {
		var ce *vars.CycleError
		if errors.As(err, &ce) {
			return err
		}
	}
	return nil
}

// ---- raw 편집기 ----

type rawVars struct {
	Format  string `json:"format"` // env | json
	Content string `json:"content"`
	// SealedVariables는 편집기에 나오지 않는(값을 볼 수 없는) 봉인 변수 이름이다. 저장해도 그대로 남는다.
	SealedVariables []string `json:"sealed_variables"`
}

func rawFormat(f string) (string, bool) {
	switch f {
	case "", "env", "dotenv":
		return "env", true
	case "json":
		return "json", true
	}
	return "", false
}

// RenderRawVars는 봉인 변수를 뺀 변수를 format(env|json) 텍스트로 쓴다. env면 봉인 변수를 주석으로 알린다.
func RenderRawVars(format string, m map[string]string, sealed map[string]bool) string {
	visible := withoutSealed(m, sealed)
	if format == "json" {
		b, _ := json.MarshalIndent(visible, "", "  ")
		return string(b) + "\n"
	}
	var b strings.Builder
	keys := store.SortedKeys(sealed)
	if len(keys) > 0 {
		b.WriteString("# 봉인된 변수는 값을 볼 수 없고 이 편집기로 바뀌지 않습니다(지우려면 변수 목록에서 삭제하세요).\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "# %s=%s\n", k, SealedValue)
		}
		if len(visible) > 0 {
			b.WriteString("\n")
		}
	}
	b.WriteString(dotenv.Marshal(visible))
	return b.String()
}

// ParseRawVars는 raw 편집기 내용을 변수 맵으로 읽는다.
func ParseRawVars(format, content string) (map[string]string, error) {
	if format == "json" {
		if strings.TrimSpace(content) == "" {
			return map[string]string{}, nil
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(content), &m); err != nil {
			return nil, errors.New("content must be a JSON object of strings")
		}
		if m == nil {
			m = map[string]string{}
		}
		return m, nil
	}
	return dotenv.Parse(content)
}

func (s *Server) getRawVars(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	format, ok := rawFormat(r.URL.Query().Get("format"))
	if !ok {
		writeErr(w, 400, "format must be env or json")
		return
	}
	sealed := s.instanceSealed(r.Context(), c.in)
	writeJSON(w, 200, rawVars{Format: format, Content: RenderRawVars(format, c.in.Variables, sealed), SealedVariables: store.SortedKeys(sealed)})
}

func (s *Server) putRawVars(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body rawVars
	if !decode(w, r, &body) {
		return
	}
	format, ok := rawFormat(body.Format)
	if !ok {
		writeErr(w, 400, "format must be env or json")
		return
	}
	next, err := ParseRawVars(format, body.Content)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	in := c.in
	old := copyMap(in.Variables)
	sealed := s.instanceSealed(ctx, in)
	keepSealed(old, next, sealed, true)
	if err := s.checkInstanceVars(ctx, in, next); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Store.SealVariables(ctx, in.ID, next, resealKeys(sealed, nil)); err != nil {
		s.storeErr(w, err)
		return
	}
	in.Variables = next
	if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "variables.updated", fmt.Sprintf("%s (%s): raw 편집기로 변수를 변경했습니다", c.svc.Name, c.env.Name))
	fresh, err := s.Store.GetInstance(ctx, in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, s.viewInstance(r, c.svc, c.env, fresh))
}

// ---- 공유 변수 ----

// sharedVarsBody는 PUT shared-variables의 새 형식이다. 예전 형식(문자열 맵 그대로)도 받는다.
type sharedVarsBody struct {
	Values map[string]string `json:"values"`
	Sealed []string          `json:"sealed"`
}

// parseSharedVarsBody는 {values, sealed} 또는 예전 형식(키 → 문자열)을 읽는다.
// "values"가 JSON 객체이면 새 형식이다(예전 형식에서 값은 늘 문자열이다).
func parseSharedVarsBody(raw []byte) (*sharedVarsBody, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, errors.New("shared variables must be an object")
	}
	if v, ok := probe["values"]; ok {
		if t := bytes.TrimSpace(v); len(t) > 0 && (t[0] == '{' || string(t) == "null") {
			for k := range probe {
				if k != "values" && k != "sealed" {
					return nil, fmt.Errorf("unknown field %q (use {values, sealed} or a plain object of strings)", k)
				}
			}
			var b sharedVarsBody
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, errors.New("values must be an object of strings and sealed an array of names")
			}
			if b.Values == nil {
				b.Values = map[string]string{}
			}
			return &b, nil
		}
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("shared variables must be an object of strings")
	}
	if m == nil {
		m = map[string]string{}
	}
	return &sharedVarsBody{Values: m}, nil
}

// sharedVarsView는 GET shared-variables?detail=1 응답이다.
type sharedVarsView struct {
	Values map[string]string `json:"values"`
	Sealed []string          `json:"sealed"`
}

// newlySealed는 add 중 아직 봉인되지 않았던 키다(정렬, 중복 제거).
func newlySealed(before map[string]bool, add []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range add {
		if !before[k] && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
