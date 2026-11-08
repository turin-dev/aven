package proxy

import (
	"net"
	"net/http/httputil"
	"net/url"
	"strings"
)

// WakeReverseProxy forwards a request that woke a sleeping app to that app at target, keeping the
// visitor's Host. It replaces NewSingleHostReverseProxy+Director (Director is deprecated since Go 1.26)
// and keeps the headers the app saw before: the X-Forwarded-For/-Host/-Proto that Caddy set are passed
// on, and the address of the hop that reached the wake handler is appended to X-Forwarded-For, as
// ReverseProxy did for a Director. SetXForwarded is not used: it would overwrite Caddy's
// X-Forwarded-Proto with the scheme of this internal hop.
func WakeReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = pr.In.Host
		for _, k := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
			if v, ok := pr.In.Header[k]; ok {
				pr.Out.Header[k] = v
			}
		}
		if ip, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
			if prior := pr.Out.Header["X-Forwarded-For"]; len(prior) > 0 {
				ip = strings.Join(prior, ", ") + ", " + ip
			}
			pr.Out.Header.Set("X-Forwarded-For", ip)
		}
	}}
}
