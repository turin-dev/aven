package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/turin-dev/aven/internal/featureflags"
	"github.com/turin-dev/aven/internal/store"
)

var featureFlagNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var featureFlagRuleIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type featureFlagSnapshot struct {
	ProjectID     string               `json:"project_id"`
	EnvironmentID string               `json:"environment_id"`
	Flags         []*store.FeatureFlag `json:"flags"`
}

func validFeatureFlagType(t string) bool {
	switch t {
	case store.FeatureFlagBool, store.FeatureFlagString, store.FeatureFlagNumber, store.FeatureFlagJSON:
		return true
	default:
		return false
	}
}

func normalizeFlagValue(flagType string, raw json.RawMessage) (json.RawMessage, error) {
	if !validFeatureFlagType(flagType) {
		return nil, fmt.Errorf("type must be bool, string, number or json")
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, errors.New("value must be valid JSON")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("value must be valid JSON")
	}
	switch flagType {
	case store.FeatureFlagBool:
		if _, ok := value.(bool); !ok {
			return nil, errors.New("bool flags need a true or false value")
		}
	case store.FeatureFlagString:
		if _, ok := value.(string); !ok {
			return nil, errors.New("string flags need a string value")
		}
	case store.FeatureFlagNumber:
		n, ok := value.(json.Number)
		if !ok {
			return nil, errors.New("number flags need a number value")
		}
		f, err := n.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errors.New("number flag value must be finite")
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, errors.New("value must be valid JSON")
	}
	return json.RawMessage(compact.Bytes()), nil
}

func normalizeFlagRules(flagType string, rules []store.FeatureFlagRule) ([]store.FeatureFlagRule, error) {
	if len(rules) > 100 {
		return nil, errors.New("a flag can have at most 100 targeting rules")
	}
	seen := make(map[string]bool, len(rules))
	for i := range rules {
		if rules[i].ID == "" {
			rules[i].ID = fmt.Sprintf("rule-%d", i+1)
		}
		if !featureFlagRuleIDRE.MatchString(rules[i].ID) || seen[rules[i].ID] {
			return nil, errors.New("rule ids must be unique and use letters, numbers, dots, dashes or underscores")
		}
		seen[rules[i].ID] = true
		rules[i].When = strings.TrimSpace(rules[i].When)
		if err := featureflags.Validate(rules[i].When); err != nil {
			return nil, fmt.Errorf("rule %q: %w", rules[i].ID, err)
		}
		value, err := normalizeFlagValue(flagType, rules[i].Value)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rules[i].ID, err)
		}
		rules[i].Value = value
	}
	if rules == nil {
		rules = []store.FeatureFlagRule{}
	}
	return rules, nil
}

func (s *Server) featureFlagEnvironment(w http.ResponseWriter, r *http.Request, projectID string) (*store.Environment, bool) {
	requested := r.URL.Query().Get("environment_id")
	if requested == "" {
		p := principalFrom(r)
		if p != nil && p.Token != nil && p.Token.Scope == store.ScopeEnvironment && p.Token.EnvironmentID != nil {
			requested = *p.Token.EnvironmentID
		} else {
			project, err := s.Store.GetProject(r.Context(), projectID)
			if err != nil {
				s.storeErr(w, err)
				return nil, false
			}
			requested = project.DefaultEnvironmentID
		}
	}
	env, err := s.Store.GetEnvironment(r.Context(), requested)
	if err != nil || env.ProjectID != projectID {
		writeErr(w, 404, "environment not found")
		return nil, false
	}
	if p := principalFrom(r); p != nil && p.Token != nil && p.Token.Scope == store.ScopeEnvironment {
		if p.Token.EnvironmentID == nil || *p.Token.EnvironmentID != env.ID {
			writeErr(w, 404, "environment not found")
			return nil, false
		}
	}
	return env, true
}

func (s *Server) listFeatureFlags(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	env, ok := s.featureFlagEnvironment(w, r, pid)
	if !ok {
		return
	}
	s.writeFlagSnapshot(w, r, pid, env.ID)
}

func (s *Server) createFeatureFlag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string                  `json:"name"`
		Type          string                  `json:"type"`
		EnvironmentID string                  `json:"environment_id"`
		DefaultValue  json.RawMessage         `json:"default_value"`
		Rules         []store.FeatureFlagRule `json:"rules"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !featureFlagNameRE.MatchString(in.Name) {
		writeErr(w, 400, "name must use letters, numbers, dots, dashes or underscores (max 128 chars)")
		return
	}
	defaultValue, err := normalizeFlagValue(in.Type, in.DefaultValue)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rules, err := normalizeFlagRules(in.Type, in.Rules)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	pid := r.PathValue("pid")
	env, err := s.Store.GetEnvironment(r.Context(), in.EnvironmentID)
	if err != nil || env.ProjectID != pid {
		writeErr(w, 400, "environment_id must belong to this project")
		return
	}
	f := &store.FeatureFlag{ProjectID: pid, Name: in.Name, Type: in.Type, EnvironmentID: env.ID, DefaultValue: defaultValue, Rules: rules}
	if err := s.Store.CreateFeatureFlag(r.Context(), f); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "feature_flag.created", f.ID, "기능 플래그 "+f.Name+" 생성")
	writeJSON(w, 201, f)
}

func (s *Server) updateFeatureFlag(w http.ResponseWriter, r *http.Request) {
	pid, name := r.PathValue("pid"), r.PathValue("name")
	var in struct {
		EnvironmentID string                   `json:"environment_id"`
		Type          string                   `json:"type"`
		DefaultValue  json.RawMessage          `json:"default_value"`
		Rules         *[]store.FeatureFlagRule `json:"rules"`
		Force         bool                     `json:"force"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.EnvironmentID == "" {
		writeErr(w, 400, "environment_id is required")
		return
	}
	old, err := s.Store.GetFeatureFlag(r.Context(), pid, name, in.EnvironmentID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	flagType := old.Type
	if in.Type != "" {
		flagType = in.Type
	}
	if old.Type != flagType && !in.Force {
		writeErr(w, 409, "changing a flag type requires force=true and clears rules in every environment")
		return
	}
	defaultValue := old.DefaultValue
	if len(in.DefaultValue) > 0 {
		defaultValue = in.DefaultValue
	}
	defaultValue, err = normalizeFlagValue(flagType, defaultValue)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rules := old.Rules
	if old.Type != flagType && in.Force {
		rules = []store.FeatureFlagRule{}
	}
	if in.Rules != nil {
		rules = *in.Rules
	}
	rules, err = normalizeFlagRules(flagType, rules)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Store.UpdateFeatureFlag(r.Context(), pid, name, in.EnvironmentID, flagType, in.Force, defaultValue, rules); err != nil {
		s.storeErr(w, err)
		return
	}
	f, err := s.Store.GetFeatureFlag(r.Context(), pid, name, in.EnvironmentID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "feature_flag.updated", f.ID, "기능 플래그 "+f.Name+" 수정")
	writeJSON(w, 200, f)
}

func (s *Server) deleteFeatureFlag(w http.ResponseWriter, r *http.Request) {
	pid, name := r.PathValue("pid"), r.PathValue("name")
	f, err := s.Store.GetFeatureFlag(r.Context(), pid, name, "")
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.DeleteFeatureFlag(r.Context(), pid, name); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "feature_flag.deleted", f.ID, "기능 플래그 "+f.Name+" 삭제")
	w.WriteHeader(204)
}

// projectTokenSnapshot resolves the project (and environment) that owns a scoped token.
// An environment token is pinned to its environment.
func (s *Server) projectTokenSnapshot(w http.ResponseWriter, r *http.Request, envID string) (string, *store.Environment, bool) {
	p := principalFrom(r)
	if p == nil || p.Token == nil || p.Token.ProjectID == nil || (p.Token.Scope != store.ScopeProject && p.Token.Scope != store.ScopeEnvironment) {
		writeErr(w, 403, "a project-scoped API token is required")
		return "", nil, false
	}
	pid := *p.Token.ProjectID
	if p.Token.Scope == store.ScopeEnvironment {
		if p.Token.EnvironmentID == nil || (envID != "" && envID != *p.Token.EnvironmentID) {
			writeErr(w, 404, "environment not found")
			return "", nil, false
		}
		envID = *p.Token.EnvironmentID
	}
	lv, err := s.projectLevel(r.Context(), p, pid, envID)
	if err != nil {
		s.storeErr(w, err)
		return "", nil, false
	}
	if lv < lvViewer {
		writeErr(w, 404, "not found")
		return "", nil, false
	}
	if envID == "" {
		project, err := s.Store.GetProject(r.Context(), pid)
		if err != nil {
			s.storeErr(w, err)
			return "", nil, false
		}
		envID = project.DefaultEnvironmentID
	}
	env, err := s.Store.GetEnvironment(r.Context(), envID)
	if err != nil || env.ProjectID != pid {
		writeErr(w, 404, "environment not found")
		return "", nil, false
	}
	return pid, env, true
}

// projectFlagSnapshot infers the project from a project-scoped token for SDK polling.
func (s *Server) projectFlagSnapshot(w http.ResponseWriter, r *http.Request) {
	pid, env, ok := s.projectTokenSnapshot(w, r, r.URL.Query().Get("environment_id"))
	if !ok {
		return
	}
	s.writeFlagSnapshot(w, r, pid, env.ID)
}

// writeFlagSnapshot writes every flag of one environment. SDKs poll it; an ETag lets unchanged
// snapshots answer 304 without a body.
func (s *Server) writeFlagSnapshot(w http.ResponseWriter, r *http.Request, pid, eid string) {
	flags, err := s.Store.ListFeatureFlags(r.Context(), pid, eid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	body, err := json.Marshal(featureFlagSnapshot{ProjectID: pid, EnvironmentID: eid, Flags: flags})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

type flagEvaluation struct {
	Value  json.RawMessage `json:"value"`
	Reason string          `json:"reason"`
	Trace  []flagTrace     `json:"trace"`
}

type flagTrace struct {
	RuleID  string `json:"rule_id"`
	When    string `json:"when"`
	Matched bool   `json:"matched"`
}

func evaluateFeatureFlag(f *store.FeatureFlag, context map[string]any) flagEvaluation {
	result := flagEvaluation{Value: f.DefaultValue, Reason: "NO_MATCH", Trace: make([]flagTrace, 0, len(f.Rules))}
	var served json.RawMessage
	matched := 0
	conflict := false
	split := false
	for _, rule := range f.Rules {
		ok, err := featureflags.Evaluate(rule.When, f.Name, context)
		if err == nil && ok {
			result.Trace = append(result.Trace, flagTrace{RuleID: rule.ID, When: rule.When, Matched: true})
			if matched == 0 {
				served = rule.Value
			} else if !sameJSONValue(served, rule.Value) {
				conflict = true
			}
			matched++
			if strings.Contains(rule.When, "bucket(") {
				split = true
			}
		} else {
			result.Trace = append(result.Trace, flagTrace{RuleID: rule.ID, When: rule.When})
		}
	}
	if matched > 0 && !conflict {
		result.Value = served
		result.Reason = "TARGETING_MATCH"
		if split {
			result.Reason = "SPLIT"
		}
	} else if conflict {
		result.Reason = "CONFLICT"
	}
	return result
}

func sameJSONValue(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

func (s *Server) evaluateProjectFlag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string         `json:"name"`
		EnvironmentID string         `json:"environment_id"`
		Context       map[string]any `json:"context"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeErr(w, 400, "name is required")
		return
	}
	pid, env, ok := s.projectTokenSnapshot(w, r, in.EnvironmentID)
	if !ok {
		return
	}
	flag, err := s.Store.GetFeatureFlag(r.Context(), pid, in.Name, env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if k, ok := in.Context["key"]; ok && in.Context["targetingKey"] == nil {
		in.Context["targetingKey"] = k // SDK와 같이 key를 targetingKey로도 노출한다
	}
	evaluated := evaluateFeatureFlag(flag, in.Context)
	writeJSON(w, 200, struct {
		Name          string          `json:"name"`
		EnvironmentID string          `json:"environment_id"`
		Value         json.RawMessage `json:"value"`
		Reason        string          `json:"reason"`
		Trace         []flagTrace     `json:"trace"`
	}{flag.Name, env.ID, evaluated.Value, evaluated.Reason, evaluated.Trace})
}
