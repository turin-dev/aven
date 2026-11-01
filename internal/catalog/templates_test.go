package catalog

import (
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/imageupdate"
)

// 내장 템플릿이 카탈로그에 보여 줄 정보와 고정된(재현 가능한) 이미지를 갖췄는지 본다.
func TestBuiltinTemplatesWellFormed(t *testing.T) {
	if len(Keys()) == 0 {
		t.Fatal("no built-in templates")
	}
	for _, key := range Keys() {
		tpl := Templates[key]
		t.Run(key, func(t *testing.T) {
			if strings.TrimSpace(tpl.Name) == "" || strings.TrimSpace(tpl.Description) == "" || tpl.Icon == "" || tpl.Category == "" {
				t.Errorf("missing metadata: name=%q desc=%q icon=%q category=%q", tpl.Name, tpl.Description, tpl.Icon, tpl.Category)
			}
			if err := tpl.Definition.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			defs := tpl.Services()
			if len(defs) == 0 {
				t.Fatal("template has no services")
			}
			names := map[string]bool{}
			for _, d := range defs {
				if names[d.Name] {
					t.Errorf("duplicate service name %s", d.Name)
				}
				names[d.Name] = true
				if d.Port < 0 || d.Port > 65535 {
					t.Errorf("%s: port %d", d.Name, d.Port)
				}
				if d.SourceType == "database" {
					if _, ok := Databases[d.DatabaseKind]; !ok && d.DatabaseKind != BucketKind {
						t.Errorf("%s: unknown database kind %q", d.Name, d.DatabaseKind)
					}
				}
				if d.Image == "" {
					continue
				}
				ref, err := imageupdate.Parse(d.Image)
				if err != nil {
					t.Errorf("%s: image %q: %v", d.Name, d.Image, err)
					continue
				}
				if ref.Digest == "" && (ref.Tag == "latest" || !strings.Contains(d.Image, ":")) {
					t.Errorf("%s: image %q is not pinned to a version", d.Name, d.Image)
				}
			}
			if p := tpl.Definition.PrimaryService(); p != "" && !names[p] {
				t.Errorf("primary service %q is not one of %v", p, names)
			}
		})
	}
}

func TestDatabaseImagesPinned(t *testing.T) {
	for kind, db := range Databases {
		ref, err := imageupdate.Parse(db.Image)
		if err != nil || ref.Tag == "" || ref.Tag == "latest" {
			t.Errorf("%s: image %q not pinned (%v)", kind, db.Image, err)
		}
		if db.Port <= 0 || db.Port > 65535 || db.DataPath == "" || !strings.HasPrefix(db.DataPath, "/") {
			t.Errorf("%s: port %d data path %q", kind, db.Port, db.DataPath)
		}
		if db.Variables == nil {
			t.Errorf("%s: no variables", kind)
			continue
		}
		vars := db.Variables("pw")
		found := false
		for _, v := range vars {
			if strings.Contains(v, "pw") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: password is not used by any variable", kind)
		}
	}
}
