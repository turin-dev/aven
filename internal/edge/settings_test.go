package edge

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestDefaultSettingsAndValidation(t *testing.T) {
	s := DefaultSettings()
	if s.CDN.HTMLCaching != "auto" || s.CDN.DefaultTTLSeconds != 7200 || s.CDN.PurgeOnDeploy != "html" || !s.CDN.HonorSWR || s.Rules.Version != 1 {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Enabled() {
		t.Fatal("defaults should not route through the edge engine")
	}
	for _, bad := range []func(*Settings){
		func(s *Settings) { s.CDN.HTMLCaching = "sometimes" },
		func(s *Settings) { s.CDN.DefaultTTLSeconds = 60 },
		func(s *Settings) { s.CDN.PurgeOnDeploy = "maybe" },
		func(s *Settings) { s.WAF.RateLimit = &RateLimit{Requests: 0, WindowSeconds: 60} },
		func(s *Settings) { s.WAF.RateLimit = &RateLimit{Requests: 10, WindowSeconds: 0} },
		func(s *Settings) { s.WAF.DenyIPs = []string{"not-an-ip"} },
	} {
		c := DefaultSettings()
		bad(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted invalid settings %+v", c)
		}
	}
	s.WAF.RateLimit = &RateLimit{Requests: 10, WindowSeconds: 60}
	if err := s.Validate(); err != nil || !s.Enabled() {
		t.Fatalf("rate limit: %v %v", err, s.Enabled())
	}
}

func TestSettingsJSONKeepsExplicitFalse(t *testing.T) {
	s := DefaultSettings()
	if err := json.Unmarshal([]byte(`{"cdn":{"enabled":true,"honor_swr":false}}`), &s); err != nil {
		t.Fatal(err)
	}
	s.Normalize()
	if !s.CDN.Enabled || s.CDN.HonorSWR || s.CDN.DefaultTTLSeconds != 7200 {
		t.Fatalf("%+v", s.CDN)
	}
}

func TestCanonicalPrefixes(t *testing.T) {
	got, err := CanonicalPrefixes([]string{"203.0.113.9", " 203.0.113.0/24", "2001:db8::/32", "203.0.113.7/24", ""})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "203.0.113.9,203.0.113.0/24,2001:db8::/32" {
		t.Fatalf("canonical prefixes: %v", got)
	}
	if _, err := CanonicalPrefixes([]string{"not-an-ip"}); err == nil {
		t.Fatal("invalid prefix accepted")
	}
}

func TestUnderAttackActive(t *testing.T) {
	now := time.Now()
	if (WAF{}).UnderAttackActive(now) {
		t.Fatal("off by default")
	}
	if !(WAF{UnderAttack: &UnderAttack{Since: now}}).UnderAttackActive(now.Add(1000 * time.Hour)) {
		t.Fatal("forever mode expired")
	}
	until := now.Add(time.Hour)
	w := WAF{UnderAttack: &UnderAttack{Since: now, Until: &until}}
	if !w.UnderAttackActive(now) || w.UnderAttackActive(until) {
		t.Fatal("timed mode")
	}
}

func parseRuleset(t *testing.T, raw string) Ruleset {
	t.Helper()
	var rs Ruleset
	if err := json.Unmarshal([]byte(raw), &rs); err != nil {
		t.Fatal(err)
	}
	n := 0
	rs.Normalize(func() string { n++; return "rul_" + string(rune('a'+n)) })
	return rs
}

func TestRailwayExampleRuleset(t *testing.T) {
	rs := parseRuleset(t, `{
	  "version": 1,
	  "rules": [
	    {"description": "Bypass cache for API requests", "priority": 30, "enabled": true,
	     "if": {"attr": "http.path", "op": "matches", "value": "/api*"}, "then": {"action": "cache_override", "params": {"bypass": true}}},
	    {"description": "Allow office access to admin", "priority": 10, "enabled": true,
	     "if": {"and": [{"attr": "http.path", "op": "matches", "value": "/admin*"}, {"attr": "ipv4.src", "op": "in", "value": ["203.0.113.0/24", "198.51.100.7"]}]},
	     "then": {"action": "allow"}},
	    {"description": "Block other admin requests", "priority": 20, "enabled": true,
	     "if": {"attr": "http.path", "op": "matches", "value": "/admin*"}, "then": {"action": "block", "params": {"status": 403, "body": "Forbidden"}}}
	  ]}`)
	rules, err := rs.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 || rules[0].Priority != 10 || rs.Rules[0].ID == "" {
		t.Fatalf("order/ids: %+v", rs.Rules)
	}
	first := func(path, ip string) string {
		info := &reqInfo{ip: netip.MustParseAddr(ip), host: "example.com", path: path, method: "GET", header: http.Header{}}
		for _, r := range rules {
			if r.match(info) {
				return r.Then.Action
			}
		}
		return ""
	}
	if got := first("/admin/users", "203.0.113.50"); got != "allow" {
		t.Fatalf("office admin: %s", got)
	}
	if got := first("/admin", "192.0.2.1"); got != "block" {
		t.Fatalf("other admin: %s", got)
	}
	if got := first("/api/v1", "192.0.2.1"); got != "cache_override" {
		t.Fatalf("api: %s", got)
	}
	if got := first("/", "192.0.2.1"); got != "" {
		t.Fatalf("home: %s", got)
	}
	// 왕복해도 같은 JSON 형식이다(value 배열/문자열).
	b, _ := json.Marshal(rs)
	if !strings.Contains(string(b), `"value":["203.0.113.0/24","198.51.100.7"]`) || !strings.Contains(string(b), `"value":"/admin*"`) {
		t.Fatalf("marshal: %s", b)
	}
}

func TestRuleConditions(t *testing.T) {
	cases := []struct {
		expr string
		info reqInfo
		want bool
	}{
		{`{"attr":"http.header","key":"X-Client","op":"neq","value":"mobile"}`, reqInfo{header: http.Header{}}, false}, // 값이 없으면 부정도 false
		{`{"attr":"http.header","key":"x-client","op":"neq","value":"mobile"}`, reqInfo{header: http.Header{"X-Client": {"web"}}}, true},
		{`{"attr":"http.header","key":"x-client","op":"eq","value":"Mobile"}`, reqInfo{header: http.Header{"X-Client": {"mobile"}}}, false}, // 값은 대소문자 구분
		{`{"attr":"http.host","op":"eq","value":"API.Example.com"}`, reqInfo{host: "api.example.com"}, true},
		{`{"attr":"http.host","op":"matches","value":"*.example.com"}`, reqInfo{host: "a.b.example.com"}, true},
		{`{"attr":"http.path","op":"contains","value":"*"}`, reqInfo{path: "/a*b"}, true}, // contains의 *는 글자
		{`{"attr":"http.path","op":"matches","value":"/a*c*e"}`, reqInfo{path: "/abcde"}, true},
		{`{"attr":"http.path","op":"matches","value":"/a*c*e"}`, reqInfo{path: "/abcd"}, false},
		{`{"attr":"http.path","op":"eq","value":"/Admin"}`, reqInfo{path: "/admin"}, false},
		{`{"attr":"ipv4.src","op":"not_in","value":["10.0.0.0/8"]}`, reqInfo{ip: netip.MustParseAddr("2001:db8::1")}, false}, // IPv6는 ipv4.src와 맞지 않는다
		{`{"attr":"ip.src","op":"eq","value":"2001:db8::/32"}`, reqInfo{ip: netip.MustParseAddr("2001:db8::1")}, true},
		{`{"attr":"http.method","op":"in","value":["post","PUT"]}`, reqInfo{method: "POST"}, true},
		{`{"or":[{"attr":"http.path","op":"eq","value":"/x"},{"not":{"attr":"http.method","op":"eq","value":"GET"}}]}`, reqInfo{path: "/y", method: "DELETE"}, true},
	}
	for _, c := range cases {
		rs := parseRuleset(t, `{"version":1,"rules":[{"priority":1,"if":`+c.expr+`,"then":{"action":"allow"}}]}`)
		rules, err := rs.Compile()
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		info := c.info
		if info.header == nil {
			info.header = http.Header{}
		}
		if got := rules[0].match(&info); got != c.want {
			t.Errorf("%s on %+v = %v, want %v", c.expr, c.info, got, c.want)
		}
	}
}

func TestRuleValidation(t *testing.T) {
	bad := []string{
		`{"version":2,"rules":[]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"allow"}},{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"in","value":["/"]},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"ipv4.src","op":"in","value":"1.2.3.4"},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"ipv4.src","op":"eq","value":"2001:db8::1"},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.header","op":"eq","value":"x"},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"matches","value":"*a*b*c*d*e*f*g*h*i"},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"and":[{"and":[{"and":[{"and":[{"attr":"http.path","op":"eq","value":"/"}]}]}]}]},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"block","params":{"status":500}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"redirect","params":{"host":"a.com","location":"https://b.com"}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"redirect","params":{"location":"/relative"}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"cache_override","params":{"ttl_seconds":2592001}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"cache_override","params":{"ttl_seconds":60,"bypass":true}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"allow","params":{"status":200}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"rewrite","params":{"path":"relative"}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.path","op":"eq","value":"/"},"then":{"action":"set_header","params":{"name":"Host","value":"x"}}}]}`,
		`{"version":1,"rules":[{"priority":1,"if":{"attr":"http.cookie","op":"eq","value":"/"},"then":{"action":"allow"}}]}`,
		`{"version":1,"rules":[{"priority":1,"then":{"action":"allow"}}]}`,
	}
	for _, raw := range bad {
		var rs Ruleset
		if err := json.Unmarshal([]byte(raw), &rs); err != nil {
			t.Fatal(err)
		}
		if _, err := rs.Compile(); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestWildcardMatch(t *testing.T) {
	for _, c := range []struct {
		pat, s string
		want   bool
	}{
		{"/admin*", "/admin", true}, {"/admin*", "/admin/users", true}, {"/admin*", "/adm", false},
		{"*", "", true}, {"a*a", "a", false}, {"a*a", "aa", true}, {"*.js", "/x.json", false}, {"image/*", "image/png", true},
	} {
		if got := wildcardMatch(c.pat, c.s); got != c.want {
			t.Errorf("%q ~ %q = %v", c.pat, c.s, got)
		}
	}
}
