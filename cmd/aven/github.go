package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ---- github: 대시보드의 GitHub 연결(OAuth)과 저장소 웹훅 ----

type githubConn struct {
	AppConfigured    bool   `json:"app_configured"`
	Connected        bool   `json:"connected"`
	Source           string `json:"source"`
	Login            string `json:"login"`
	Scopes           string `json:"scopes"`
	ConnectedAt      string `json:"connected_at"`
	CallbackURL      string `json:"callback_url"`
	WebhookURL       string `json:"webhook_url"`
	SecretConfigured bool   `json:"secret_configured"`
}

func cmdGitHub(c *config, args []string) {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	asJSON := len(args) > 0 && args[len(args)-1] == "--json"
	if asJSON {
		args = args[:len(args)-1]
	}
	switch sub {
	case "status":
		var g githubConn
		if err := c.call("GET", "/github/connection", nil, &g); err != nil {
			die("%v", err)
		}
		if asJSON {
			b, _ := json.MarshalIndent(g, "", "  ")
			fmt.Println(string(b))
			return
		}
		switch {
		case g.Connected:
			fmt.Printf("connected as %s (scopes: %s, since %s)\n", g.Login, g.Scopes, g.ConnectedAt)
		case g.Source == "env":
			fmt.Println("not connected; using AVEN_GITHUB_TOKEN from the server environment")
		default:
			fmt.Println("not connected: private repositories cannot be cloned")
		}
		if !g.AppConfigured {
			fmt.Printf("OAuth App not configured: create one on GitHub (Settings › Developer settings › OAuth Apps) with callback URL\n  %s\nand set AVEN_GITHUB_CLIENT_ID / AVEN_GITHUB_CLIENT_SECRET on the server (KEEL_GITHUB_CLIENT_ID / KEEL_GITHUB_CLIENT_SECRET still work)\n", g.CallbackURL)
		}
		fmt.Printf("webhook URL: %s (secret %s)\n", g.WebhookURL, map[bool]string{true: "configured", false: "MISSING"}[g.SecretConfigured])
	case "connect":
		u := strings.TrimRight(c.URL, "/") + "/api/github/oauth/start"
		fmt.Printf("open this URL in a browser where you are logged in to aven (workspace admin):\n  %s\n", u)
		openBrowser(u)
	case "disconnect":
		if err := c.call("DELETE", "/github/connection", nil, nil); err != nil {
			die("%v", err)
		}
		fmt.Println("GitHub disconnected (the grant was revoked on GitHub as well)")
	case "repos":
		q := ""
		if len(args) > 0 {
			q = "?q=" + url.QueryEscape(args[0])
		}
		var repos []struct {
			FullName string `json:"full_name"`
			Private  bool   `json:"private"`
			Branch   string `json:"default_branch"`
			Admin    bool   `json:"admin"`
		}
		if err := c.call("GET", "/github/repos"+q, nil, &repos); err != nil {
			die("%v", err)
		}
		for _, r := range repos {
			vis := "public"
			if r.Private {
				vis = "private"
			}
			fmt.Printf("%-45s %-8s %s\n", r.FullName, vis, r.Branch)
		}
	case "webhook":
		if len(args) == 0 {
			die("usage: aven github webhook <owner/repo | URL>...")
		}
		for _, r := range args {
			var res struct {
				Repo    string `json:"repo"`
				HookID  int64  `json:"hook_id"`
				Created bool   `json:"created"`
				URL     string `json:"url"`
			}
			if err := c.call("POST", "/github/webhooks", map[string]string{"repo": r}, &res); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", r, err)
				continue
			}
			verb := "updated"
			if res.Created {
				verb = "created"
			}
			fmt.Printf("%s: webhook %d %s → %s\n", res.Repo, res.HookID, verb, res.URL)
		}
	default:
		die("usage: aven github status|connect|disconnect|repos [query]|webhook <repo>...")
	}
}
