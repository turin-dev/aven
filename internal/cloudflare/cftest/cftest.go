// Package cftest는 테스트용 가짜 Cloudflare API다(존, DNS 레코드, 로드밸런서, pool, monitor).
package cftest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/turin-dev/aven/internal/cloudflare"
)

// Server는 가짜 API다. 필드는 mu를 잡고 읽고 쓴다.
type Server struct {
	*httptest.Server
	Token string
	// NoLB면 계정의 Load Balancing 엔드포인트가 403이다(구독 없음).
	NoLB bool

	Mu       sync.Mutex
	Zones    []cloudflare.Zone
	Records  map[string][]cloudflare.DNSRecord // zone id → records
	LBs      map[string][]cloudflare.LoadBalancer
	Pools    []cloudflare.Pool
	Monitors []cloudflare.Monitor
	Writes   []string // "POST /zones/z1/dns_records" 등 쓰기 요청
	next     int
}

// New는 존 하나(example.com, 계정 acc1)가 있는 서버다.
func New(token string) *Server {
	s := &Server{Token: token, Records: map[string][]cloudflare.DNSRecord{}, LBs: map[string][]cloudflare.LoadBalancer{}}
	z := cloudflare.Zone{ID: "z1", Name: "example.com", Status: "active"}
	z.Account.ID, z.Account.Name, z.Plan.Name = "acc1", "Acme", "Free Website"
	s.Zones = []cloudflare.Zone{z}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *Server) id() string { s.next++; return fmt.Sprintf("id%03d", s.next) }

func reply(w http.ResponseWriter, code int, result any, errs ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	var es []map[string]any
	for _, e := range errs {
		es = append(es, map[string]any{"code": 1000 + code, "message": e})
	}
	json.NewEncoder(w).Encode(map[string]any{"success": code < 300, "errors": es, "result": result,
		"result_info": map[string]int{"page": 1, "total_pages": 1}})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		reply(w, 401, nil, "Invalid API Token")
		return
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method != http.MethodGet {
		s.Writes = append(s.Writes, r.Method+" "+r.URL.Path)
	}
	dec := func(v any) bool {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			reply(w, 400, nil, err.Error())
			return false
		}
		return true
	}
	switch {
	case r.URL.Path == "/user/tokens/verify":
		reply(w, 200, map[string]string{"status": "active"})
	case r.URL.Path == "/zones":
		reply(w, 200, s.Zones)
	case len(p) >= 3 && p[0] == "zones" && p[2] == "dns_records":
		z := p[1]
		switch {
		case r.Method == "GET":
			reply(w, 200, append([]cloudflare.DNSRecord{}, s.Records[z]...))
		case r.Method == "POST":
			var rec cloudflare.DNSRecord
			if dec(&rec) {
				rec.ID = s.id()
				s.Records[z] = append(s.Records[z], rec)
				reply(w, 200, rec)
			}
		case r.Method == "PUT" && len(p) == 4:
			var rec cloudflare.DNSRecord
			if dec(&rec) {
				for i := range s.Records[z] {
					if s.Records[z][i].ID == p[3] {
						rec.ID = p[3]
						s.Records[z][i] = rec
					}
				}
				reply(w, 200, rec)
			}
		case r.Method == "DELETE" && len(p) == 4:
			list := s.Records[z][:0:0]
			for _, rec := range s.Records[z] {
				if rec.ID != p[3] {
					list = append(list, rec)
				}
			}
			s.Records[z] = list
			reply(w, 200, map[string]string{"id": p[3]})
		}
	case len(p) >= 3 && p[0] == "zones" && p[2] == "load_balancers":
		if s.NoLB {
			reply(w, 403, nil, "Load Balancing is not enabled")
			return
		}
		z := p[1]
		switch {
		case r.Method == "GET":
			reply(w, 200, append([]cloudflare.LoadBalancer{}, s.LBs[z]...))
		case r.Method == "POST":
			var lb cloudflare.LoadBalancer
			if dec(&lb) {
				lb.ID = s.id()
				s.LBs[z] = append(s.LBs[z], lb)
				reply(w, 200, lb)
			}
		case r.Method == "PUT":
			var lb cloudflare.LoadBalancer
			if dec(&lb) {
				for i := range s.LBs[z] {
					if s.LBs[z][i].ID == p[3] {
						lb.ID = p[3]
						s.LBs[z][i] = lb
					}
				}
				reply(w, 200, lb)
			}
		case r.Method == "DELETE":
			list := s.LBs[z][:0:0]
			for _, lb := range s.LBs[z] {
				if lb.ID != p[3] {
					list = append(list, lb)
				}
			}
			s.LBs[z] = list
			reply(w, 200, map[string]string{"id": p[3]})
		}
	case len(p) >= 4 && p[0] == "accounts" && p[2] == "load_balancers":
		if s.NoLB {
			reply(w, 403, nil, "Load Balancing is not enabled for this account")
			return
		}
		switch p[3] {
		case "pools":
			switch {
			case r.Method == "GET":
				reply(w, 200, append([]cloudflare.Pool{}, s.Pools...))
			case r.Method == "POST":
				var pl cloudflare.Pool
				if dec(&pl) {
					pl.ID = s.id()
					s.Pools = append(s.Pools, pl)
					reply(w, 200, pl)
				}
			case r.Method == "PUT":
				var pl cloudflare.Pool
				if dec(&pl) {
					for i := range s.Pools {
						if s.Pools[i].ID == p[4] {
							pl.ID = p[4]
							s.Pools[i] = pl
						}
					}
					reply(w, 200, pl)
				}
			case r.Method == "DELETE":
				for _, lbs := range s.LBs {
					for _, lb := range lbs {
						for _, id := range lb.DefaultPools {
							if id == p[4] {
								reply(w, 400, nil, "pool is in use by "+lb.Name)
								return
							}
						}
					}
				}
				list := s.Pools[:0:0]
				for _, pl := range s.Pools {
					if pl.ID != p[4] {
						list = append(list, pl)
					}
				}
				s.Pools = list
				reply(w, 200, map[string]string{"id": p[4]})
			}
		case "monitors":
			switch r.Method {
			case "GET":
				reply(w, 200, append([]cloudflare.Monitor{}, s.Monitors...))
			case "POST":
				var mo cloudflare.Monitor
				if dec(&mo) {
					mo.ID = s.id()
					s.Monitors = append(s.Monitors, mo)
					reply(w, 200, mo)
				}
			case "DELETE":
				list := s.Monitors[:0:0]
				for _, mo := range s.Monitors {
					if mo.ID != p[4] {
						list = append(list, mo)
					}
				}
				s.Monitors = list
				reply(w, 200, map[string]string{"id": p[4]})
			}
		}
	default:
		reply(w, 404, nil, "not found: "+r.Method+" "+r.URL.Path)
	}
}
