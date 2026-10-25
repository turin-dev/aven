package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEnvironmentRestrict(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/projects/prj_1":
			json.NewEncoder(w).Encode(map[string]any{"id": "prj_1", "name": "p", "environments": []map[string]any{
				{"id": "env_1", "name": "production"}, {"id": "env_2", "name": "staging"}}})
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/api/environments/"):
			b, _ := io.ReadAll(r.Body)
			got = append(got, r.URL.Path+" "+string(b))
			var in struct {
				Restricted bool `json:"restricted"`
			}
			json.Unmarshal(b, &in)
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/api/environments/"), "name": "x", "restricted": in.Restricted})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("KEEL_TOKEN", "tok")
	c := &config{URL: srv.URL, Links: map[string]*link{}}
	l := &link{ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "production"}
	stdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	cmdEnvironmentRestrict(c, l, true, nil)
	cmdEnvironmentRestrict(c, l, false, []string{"staging"})
	os.Stdout = stdout
	want := []string{`/api/environments/env_1 {"restricted":true}`, `/api/environments/env_2 {"restricted":false}`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
