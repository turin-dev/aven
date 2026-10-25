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

func TestRunAPIUsesGraphQLEndpointAndBearerToken(t *testing.T) {
	t.Setenv("KEEL_TOKEN", "api-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql/v2" {
			t.Errorf("request target: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer api-token" {
			t.Errorf("authorization %q", got)
		}
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Query != "query($id: ID!) { project(id: $id) { id } }" || request.Variables["id"] != "prj_1" {
			t.Errorf("GraphQL body: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"project":{"id":"prj_1"}}}`)
	}))
	defer server.Close()

	var output strings.Builder
	err := runAPI(&config{URL: server.URL}, []string{
		"--variables", `{"id":"prj_1"}`,
		`query($id: ID!) { project(id: $id) { id } }`,
	}, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"id": "prj_1"`) {
		t.Fatalf("output not formatted JSON: %s", output.String())
	}
}

func TestRunAPIReadsQueryFromStdinAndReportsGraphQLErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql/v2" || r.Header.Get("Cookie") != "keel_session=session-cookie" {
			t.Errorf("request: %s %s cookie=%q", r.Method, r.URL.Path, r.Header.Get("Cookie"))
		}
		io.WriteString(w, `{"data":null,"errors":[{"message":"not found"}]}`)
	}))
	defer server.Close()

	var output strings.Builder
	err := runAPI(&config{URL: server.URL, Cookie: "session-cookie"}, nil, strings.NewReader("{ projects { totalCount } }"), &output)
	if err == nil || !strings.Contains(err.Error(), "returned errors") {
		t.Fatalf("expected GraphQL errors, got %v", err)
	}
	if !strings.Contains(output.String(), `"message": "not found"`) {
		t.Fatalf("GraphQL errors not printed: %s", output.String())
	}
}

func TestRunAPIFieldsAndFiles(t *testing.T) {
	t.Setenv("KEEL_TOKEN", "")
	dir := t.TempDir()
	queryFile := dir + "/q.graphql"
	if err := os.WriteFile(queryFile, []byte("query($id: String!, $n: Int) { project(id: $id) { id } }"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"data":{}}`)
	}))
	defer server.Close()
	var output strings.Builder
	err := runAPI(&config{URL: server.URL}, []string{"@" + queryFile, "-F", "id=prj_1", "--field=n=3", "-F", "ok=true", "--raw"}, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Query, "query($id: String!") || got.Variables["id"] != "prj_1" || got.Variables["n"] != float64(3) || got.Variables["ok"] != true {
		t.Fatalf("request: %+v", got)
	}
	if strings.TrimSpace(output.String()) != `{"data":{}}` {
		t.Fatalf("--raw output: %q", output.String())
	}
	if err := runAPI(&config{URL: server.URL}, []string{"-F", "novalue", "{ me { id } }"}, nil, io.Discard); err == nil {
		t.Fatal("-F without = accepted")
	}
}
