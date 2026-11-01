// Package iac defines Aven's project-level Infrastructure as Code definition: the portable,
// environment-scoped description of a project's services that `aven config` imports, plans

// The definition is deliberately additive for settings: a setting that is omitted is left
// untouched, so a file only manages what it declares. Variables are managed as a whole map
// once a service declares `variables`; omitting a declared key removes it.
package iac

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Version is the only definition format version.
const Version = 1

var PartialNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidPartial reports whether name is a valid named partial.
func ValidPartial(name string) bool { return PartialNameRe.MatchString(name) }

// Definition describes the services of one environment.
type Definition struct {
	Version int `json:"version" yaml:"version"`
	// ProjectName is informational: a different name produces a plan warning, never a rename.
	ProjectName string `json:"project_name,omitempty" yaml:"project_name,omitempty"`
	// Partial is the named partial that owns the declared resources. Empty = whole project.
	Partial  string    `json:"partial,omitempty" yaml:"partial,omitempty"`
	Services []Service `json:"services" yaml:"services"`
}

// Service is one service instance. Settings use store.Spec JSON names (repo_url, start_command…).
type Service struct {
	Name         string                     `json:"name" yaml:"name"`
	SourceType   string                     `json:"source_type,omitempty" yaml:"source_type,omitempty"`
	DatabaseKind string                     `json:"database_kind,omitempty" yaml:"database_kind,omitempty"`
	Settings     map[string]json.RawMessage `json:"settings,omitempty" yaml:"settings,omitempty"`
	// Variables nil = variables are not managed; non-nil (even empty) = exactly these keys.
	Variables *map[string]Variable `json:"variables,omitempty" yaml:"variables,omitempty"`
}

func (s Service) Address() string { return ServiceAddress(s.Name) }

// ServiceAddress is the ownership address for a service name.
func ServiceAddress(name string) string { return "service." + name }

// Variable is either a literal value or preserve(): keep the value already stored by Aven.
type Variable struct {
	Value    string
	Preserve bool
}

func (v *Variable) UnmarshalJSON(b []byte) error {
	var value string
	if err := json.Unmarshal(b, &value); err == nil {
		v.Value, v.Preserve = value, false
		return nil
	}
	var marker struct {
		Preserve bool `json:"preserve"`
	}
	if err := json.Unmarshal(b, &marker); err != nil || !marker.Preserve {
		return errors.New(`variable values must be strings or {"preserve":true}`)
	}
	v.Value, v.Preserve = "", true
	return nil
}

func (v Variable) MarshalJSON() ([]byte, error) {
	if v.Preserve {
		return []byte(`{"preserve":true}`), nil
	}
	return json.Marshal(v.Value)
}

// Validate checks the structure and normalizes nil collections (so hashing is stable). Errors name the offending
// path (services[2].name, services[0].settings.port) so a long file can be fixed without guessing.
func (d *Definition) Validate() error {
	if d.Version != Version {
		return fmt.Errorf("version: must be %d (got %d)", Version, d.Version)
	}
	if d.Partial != "" && !ValidPartial(d.Partial) {
		return errors.New("partial: must be 1-64 letters, digits, dots, underscores or hyphens")
	}
	if len(d.ProjectName) > 64 || strings.ContainsAny(d.ProjectName, "\n\r\t") {
		return errors.New("project_name: must be at most 64 characters and contain no line breaks")
	}
	if len(d.Services) > 500 {
		return errors.New("services: a configuration may contain at most 500 services")
	}
	seen := make(map[string]int, len(d.Services))
	for i := range d.Services {
		s := &d.Services[i]
		if s.Name == "" {
			return fmt.Errorf("services[%d].name: is required", i)
		}
		if first, dup := seen[s.Name]; dup {
			return fmt.Errorf("services[%d].name: service %q is already declared at services[%d]", i, s.Name, first)
		}
		seen[s.Name] = i
		if s.Settings == nil {
			s.Settings = map[string]json.RawMessage{}
		}
		for _, k := range sortedKeys(s.Settings) {
			if !json.Valid(s.Settings[k]) {
				return fmt.Errorf("services[%d].settings.%s: is not valid JSON", i, k)
			}
		}
		if s.Variables != nil && *s.Variables == nil {
			*s.Variables = map[string]Variable{}
		}
	}
	if d.Services == nil {
		d.Services = []Service{}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Decode reads JSON or YAML. YAML is normalized through JSON so the API and CLI share
// one type and one set of validation rules. Unknown fields are rejected.
func Decode(data []byte, format string) (*Definition, error) {
	var raw []byte
	switch format {
	case "json":
		raw = data
	case "yaml", "yml":
		var value any
		if err := yaml.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		var err error
		if raw, err = json.Marshal(value); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported configuration format %q", format)
	}
	return DecodeJSON(raw)
}

var (
	definitionFields = []string{"version", "project_name", "partial", "services"}
	serviceFields    = []string{"name", "source_type", "database_kind", "settings", "variables"}
)

// DecodeJSON strictly decodes and validates a JSON definition. Unlike encoding/json errors, every error carries the
// path of the offending value (services[1].variables.API_KEY: ...) and unknown keys list the allowed ones, so a typo
// such as "setings" fails instead of being silently ignored.
func DecodeJSON(raw []byte) (*Definition, error) {
	top, err := strictObject(raw, "definition", definitionFields)
	if err != nil {
		return nil, err
	}
	var d Definition
	if v, ok := top["version"]; ok {
		if err := json.Unmarshal(v, &d.Version); err != nil {
			return nil, errors.New("version: must be an integer")
		}
	}
	for key, dst := range map[string]*string{"project_name": &d.ProjectName, "partial": &d.Partial} {
		if v, ok := top[key]; ok && string(v) != "null" {
			if err := json.Unmarshal(v, dst); err != nil {
				return nil, fmt.Errorf("%s: must be a string", key)
			}
		}
	}
	if v, ok := top["services"]; ok && string(v) != "null" {
		var items []json.RawMessage
		if err := json.Unmarshal(v, &items); err != nil {
			return nil, errors.New("services: must be a list")
		}
		d.Services = make([]Service, 0, len(items))
		for i, item := range items {
			svc, err := decodeService(item, fmt.Sprintf("services[%d]", i))
			if err != nil {
				return nil, err
			}
			d.Services = append(d.Services, svc)
		}
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

func decodeService(raw json.RawMessage, path string) (Service, error) {
	var s Service
	obj, err := strictObject(raw, path, serviceFields)
	if err != nil {
		return s, err
	}
	for key, dst := range map[string]*string{"name": &s.Name, "source_type": &s.SourceType, "database_kind": &s.DatabaseKind} {
		if v, ok := obj[key]; ok && string(v) != "null" {
			if err := json.Unmarshal(v, dst); err != nil {
				return s, fmt.Errorf("%s.%s: must be a string", path, key)
			}
		}
	}
	if v, ok := obj["settings"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &s.Settings); err != nil {
			return s, fmt.Errorf("%s.settings: must be an object", path)
		}
	}
	if v, ok := obj["variables"]; ok && string(v) != "null" {
		var vars map[string]json.RawMessage
		if err := json.Unmarshal(v, &vars); err != nil {
			return s, fmt.Errorf("%s.variables: must be an object of string values", path)
		}
		out := make(map[string]Variable, len(vars))
		for _, k := range sortedKeys(vars) {
			var val Variable
			if err := val.UnmarshalJSON(vars[k]); err != nil {
				return s, fmt.Errorf("%s.variables.%s: %w", path, k, err)
			}
			out[k] = val
		}
		s.Variables = &out
	}
	return s, nil
}

// strictObject decodes a JSON object and rejects keys that are not in allowed.
func strictObject(raw []byte, path string, allowed []string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return nil, fmt.Errorf("%s: invalid JSON at byte %d: %v", path, syntax.Offset, err)
		}
		return nil, fmt.Errorf("%s: must be an object", path)
	}
	for _, k := range sortedKeys(obj) {
		if !slices.Contains(allowed, k) {
			return nil, fmt.Errorf("%s.%s: unknown field (allowed: %s)", path, k, strings.Join(allowed, ", "))
		}
	}
	return obj, nil
}

// MarshalJSON renders an indented JSON definition.
func MarshalJSON(d *Definition) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// MarshalYAML renders a YAML definition (through JSON, so key names match).
func MarshalYAML(d *Definition) ([]byte, error) {
	b, err := MarshalJSON(d)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	return yaml.Marshal(value)
}
