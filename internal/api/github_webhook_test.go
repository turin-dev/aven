package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPushSHAOnlyTrustsFullHexSHAs(t *testing.T) {
	good := strings.Repeat("a", 40)
	for _, tc := range []struct {
		after, head, want string
	}{
		{good, "", good},
		{"--upload-pack=touch /tmp/x", "", ""},
		{"--upload-pack=x", good, good},
		{strings.Repeat("0", 40), "", ""}, // deleted ref marker
		{"abc123", "", ""},
		{strings.ToUpper(good), "", ""},
	} {
		ev := &pushEvent{After: tc.after}
		if tc.head != "" {
			ev.HeadCommit = &struct {
				ID      string `json:"id"`
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
				} `json:"author"`
			}{ID: tc.head}
		}
		if got := pushSHA(ev); got != tc.want {
			t.Errorf("after=%q head=%q: %q, want %q", tc.after, tc.head, got, tc.want)
		}
	}
}

func TestGitHubPushWithInvalidSHADeploysBranchHead(t *testing.T) {
	f := newGHFixture(t)
	p := pushPayload("main", "-c core.sshCommand=evil", "acme/app")
	p["head_commit"].(map[string]any)["id"] = "nothex"
	code, out := f.hook("push", p)
	if code != 202 {
		t.Fatalf("push: %d %v", code, out)
	}
	deps := out["deployments"].([]any)
	if len(deps) != 1 {
		t.Fatalf("deployments: %v", out)
	}
	d, err := f.st.GetDeployment(f.ctx, deps[0].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if d.CommitSHA != nil && *d.CommitSHA != "" {
		t.Errorf("commit sha %q must not come from an invalid payload", *d.CommitSHA)
	}
}

func TestGitHubWebhookWithoutSecretRejectsBeforeReading(t *testing.T) {
	s := &Server{}
	body := &countingReader{r: strings.NewReader(strings.Repeat("x", 1<<20))}
	r := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", body)
	w := httptest.NewRecorder()
	s.githubWebhook(w, r)
	if w.Code != 403 || body.n != 0 {
		t.Errorf("code %d, read %d bytes", w.Code, body.n)
	}
}

type countingReader struct {
	r interface{ Read([]byte) (int, error) }
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
