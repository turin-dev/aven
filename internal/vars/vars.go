// ${{ VAR }}            같은 서비스(인스턴스)의 변수
// ${{ shared.VAR }}     환경의 공유 변수
// ${{ service.VAR }}    같은 환경의 다른 서비스 변수
package vars

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var refRe = regexp.MustCompile(`\$\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

// Scope는 한 환경의 변수 전체다. Services의 값은 주입 변수 위에 사용자 변수를 덮은 것이다.
type Scope struct {
	Shared   map[string]string
	Services map[string]map[string]string // service name → vars
}

// CycleError는 참조 순환을 알린다.
type CycleError struct{ Path []string }

func (e *CycleError) Error() string {
	return "variable reference cycle: " + strings.Join(e.Path, " → ")
}

// Result는 해석 결과와, 찾지 못한 참조 목록이다(찾지 못한 참조는 빈 문자열이 된다).
type Result struct {
	Vars    map[string]string
	Missing []string
}

type key struct{ scope, name string } // scope: "shared" 또는 서비스 이름

func (k key) String() string { return k.scope + "." + k.name }

type resolver struct {
	s       *Scope
	done    map[key]string
	visit   map[key]bool
	stack   []key
	missing map[string]bool
}

func (r *resolver) raw(k key) (string, bool) {
	var m map[string]string
	if k.scope == "shared" {
		m = r.s.Shared
	} else {
		m = r.s.Services[k.scope]
	}
	v, ok := m[k.name]
	return v, ok
}

func (r *resolver) resolve(k key) (string, error) {
	if v, ok := r.done[k]; ok {
		return v, nil
	}
	if r.visit[k] {
		path := []string{}
		start := 0
		for i, s := range r.stack {
			if s == k {
				start = i
			}
		}
		for _, s := range r.stack[start:] {
			path = append(path, s.String())
		}
		path = append(path, k.String())
		return "", &CycleError{Path: path}
	}
	raw, ok := r.raw(k)
	if !ok {
		r.missing[k.String()] = true
		return "", nil
	}
	r.visit[k] = true
	r.stack = append(r.stack, k)
	var firstErr error
	out := refRe.ReplaceAllStringFunc(raw, func(m string) string {
		if firstErr != nil {
			return ""
		}
		ref := refRe.FindStringSubmatch(m)[1]
		target := key{scope: k.scope, name: ref}
		if scope, name, ok := strings.Cut(ref, "."); ok {
			target = key{scope: scope, name: name}
		}
		// shared 변수 안의 ${{ VAR }}는 shared 범위를 가리킨다.
		v, err := r.resolve(target)
		if err != nil {
			firstErr = err
		}
		return v
	})
	r.stack = r.stack[:len(r.stack)-1]
	delete(r.visit, k)
	if firstErr != nil {
		return "", firstErr
	}
	r.done[k] = out
	return out, nil
}

// Resolve는 service의 모든 변수를 해석한다.
func Resolve(s *Scope, service string) (*Result, error) {
	r := &resolver{s: s, done: map[key]string{}, visit: map[key]bool{}, missing: map[string]bool{}}
	own := s.Services[service]
	out := make(map[string]string, len(own))
	for name := range own {
		v, err := r.resolve(key{scope: service, name: name})
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	res := &Result{Vars: out, Missing: []string{}}
	for m := range r.missing {
		res.Missing = append(res.Missing, m)
	}
	sort.Strings(res.Missing)
	return res, nil
}

// References는 값에 들어 있는 참조 목록을 돌려준다(예: "postgres.DATABASE_URL").
func References(value string) []string {
	var out []string
	for _, m := range refRe.FindAllStringSubmatch(value, -1) {
		out = append(out, m[1])
	}
	return out
}

// RenameService는 값 안의 ${{ old.X }} 참조를 ${{ new.X }}로 바꾼다(템플릿 이름 충돌, 서비스 이름 변경).
func RenameService(value, oldName, newName string) string {
	return refRe.ReplaceAllStringFunc(value, func(m string) string {
		ref := refRe.FindStringSubmatch(m)[1]
		if scope, name, ok := strings.Cut(ref, "."); ok && scope == oldName {
			return fmt.Sprintf("${{%s.%s}}", newName, name)
		}
		return m
	})
}
