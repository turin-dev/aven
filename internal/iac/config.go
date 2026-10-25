// Package iac defines Aven's project-level Infrastructure as Code definition: the portable,
// environment-scoped description of a project's services that `aven config` imports, plans

// The definition is deliberately additive for settings: a setting that is omitted is left
// untouched, so a file only manages what it declares. Variables are managed as a whole map
// once a service declares `variables`; omitting a declared key removes it.
package iac

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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

// Validate checks the structure and normalizes nil collections (so hashing is stable).
func (d *Definition) Validate() error {
	if d.Version != Version {
		return fmt.Errorf("version must be %d", Version)
	}
	if d.Partial != "" && !ValidPartial(d.Partial) {
		return errors.New("partial must be 1-64 letters, digits, dots, underscores or hyphens")
	}
	if len(d.ProjectName) > 64 || strings.ContainsAny(d.ProjectName, "\n\r\t") {
		return errors.New("project_name must be at most 64 characters and contain no line breaks")
	}
	if len(d.Services) > 500 {
		return errors.New("a configuration may contain at most 500 services")
	}
	seen := make(map[string]bool, len(d.Services))
	for i := range d.Services {
		s := &d.Services[i]
		if s.Name == "" {
			return fmt.Errorf("services[%d].name is required", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("service %q is declared more than once", s.Name)
		}
		seen[s.Name] = true
		if s.Settings == nil {
			s.Settings = map[string]json.RawMessage{}
		}
		for k, raw := range s.Settings {
			if !json.Valid(raw) {
				return fmt.Errorf("service %q: setting %q is not valid JSON", s.Name, k)
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
	var d Definition
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
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
