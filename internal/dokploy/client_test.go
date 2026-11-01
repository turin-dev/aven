package dokploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetch(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("x-api-key"))
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/project.all":
			w.Write([]byte(`[{"projectId":"p1","name":"Shop","env":"A=1","environments":[{"environmentId":"e1","name":"production",
				"applications":[{"applicationId":"a1"},{"applicationId":""}],"compose":[{"composeId":"c1"}],
				"postgres":[{"postgresId":"pg1"}],"redis":[],"mysql":null}]}]`))
		case "/api/settings.getDokployVersion":
			w.WriteHeader(500) // 버전을 못 읽어도 계속한다
		case "/api/server.all":
			w.Write([]byte(`[{"serverId":"s1","name":"remote","ipAddress":"10.0.0.2","serverType":"deploy"}]`))
		case "/api/application.one":
			if q.Get("applicationId") != "a1" {
				w.WriteHeader(404)
				w.Write([]byte(`{"message":"not found"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"applicationId": "a1", "name": "web", "domains": nil, "mounts": nil})
		case "/api/compose.one":
			w.Write([]byte(`{"composeId":"c1","name":"stack","composeFile":"services: {}"}`))
		case "/api/postgres.one":
			w.Write([]byte(`{"name":"db","appName":"db-1","databasePassword":"pw"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	snap, err := (&Client{URL: srv.URL + "/", Key: "k1"}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k != "k1" {
			t.Fatalf("api key header = %q", k)
		}
	}
	if len(snap.Projects) != 1 || len(snap.Servers) != 1 || snap.Version != "" {
		t.Fatalf("snapshot = %+v", snap)
	}
	e := snap.Projects[0].Environments[0]
	if len(e.Applications) != 1 || e.Applications[0].Name != "web" || len(e.Composes) != 1 || len(e.Databases) != 1 {
		t.Fatalf("environment = %+v", e)
	}
	if db := e.Databases[0]; db.Kind != "postgres" || db.ID != "pg1" || db.Password != "pw" {
		t.Errorf("db = %+v", db)
	}
}

func TestFetchErrors(t *testing.T) {
	if _, err := (&Client{}).Fetch(context.Background()); err == nil {
		t.Error("missing URL/key accepted")
	}
	for code, want := range map[int]string{401: "DOKPLOY_API_KEY", 403: "DOKPLOY_API_KEY", 500: "500 boom"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"message":"boom"}`))
		}))
		_, err := (&Client{URL: srv.URL, Key: "secret-key"}).Fetch(context.Background())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "secret-key") {
			t.Errorf("%d: err = %v", code, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`not json`)) }))
	defer srv.Close()
	if _, err := (&Client{URL: srv.URL, Key: "k"}).Fetch(context.Background()); err == nil {
		t.Error("invalid JSON accepted")
	}
}

func TestIDsOfSkipsEmpty(t *testing.T) {
	type row struct {
		ID string `json:"postgresId"`
	}
	if got := idsOf([]row{{"a"}, {""}, {"b"}}); strings.Join(got, ",") != "a,b" {
		t.Errorf("idsOf = %v", got)
	}
}
