package api

import (
	"net/http"
	"strings"

	"github.com/graphql-go/graphql"
)

// TCP 프록시 GraphQL(Railway: tcpProxies, tcpProxyCreate, tcpProxyDelete). aven은 서비스 인스턴스마다 TCP 프록시가 최대 하나라
// id는 "tcp_<instanceID>"다. 모두 REST(…/tcp-proxy)로 옮겨 부르므로 권한 검사는 REST가 한다.

const tcpProxyIDPrefix = "tcp_"

func tcpProxyNode(view any, instanceID, sid, eid string) map[string]any {
	v := asMap(view)
	if v == nil || v["enabled"] != true {
		return nil
	}
	return map[string]any{
		"id":              tcpProxyIDPrefix + instanceID,
		"domain":          v["host"],
		"proxyPort":       v["publicPort"],
		"applicationPort": v["containerPort"],
		"serviceId":       sid,
		"environmentId":   eid,
	}
}

func fetchTCPProxy(p graphql.ResolveParams, sid, eid string) (map[string]any, error) {
	in, err := getInstance(p, sid, eid)
	if err != nil {
		return nil, err
	}
	v, err := rest(p, http.MethodGet, instancePath(sid, eid)+"/tcp-proxy", nil)
	if err != nil {
		return nil, err
	}
	return tcpProxyNode(v, asString(in["id"]), sid, eid), nil
}

// addTCPProxyFields는 스키마에 TCP 프록시 query/mutation을 더한다.
func addTCPProxyFields(query, mutation *graphql.Object) {
	tcpProxy := graphql.NewObject(graphql.ObjectConfig{Name: "TCPProxy", Fields: graphql.Fields{
		"id":              field(nn(graphql.String)),
		"domain":          field(optStr),
		"proxyPort":       field(graphql.Int),
		"applicationPort": field(graphql.Int),
		"serviceId":       field(optStr),
		"environmentId":   field(optStr),
	}})
	query.AddFieldConfig("tcpProxies", resolved(nn(list(tcpProxy)), args("serviceId", reqStr, "environmentId", reqStr), func(p graphql.ResolveParams) (any, error) {
		n, err := fetchTCPProxy(p, asString(p.Args["serviceId"]), asString(p.Args["environmentId"]))
		if err != nil {
			return nil, err
		}
		if n == nil {
			return []any{}, nil
		}
		return []any{n}, nil
	}))
	mutation.AddFieldConfig("tcpProxyCreate", resolved(nn(tcpProxy), args("input", nn(inputObject("TCPProxyCreateInput",
		"serviceId", reqStr, "environmentId", reqStr, "applicationPort", nn(graphql.Int),
	))), func(p graphql.ResolveParams) (any, error) {
		in := asMap(p.Args["input"])
		sid, eid := asString(in["serviceId"]), asString(in["environmentId"])
		if _, err := rest(p, http.MethodPost, instancePath(sid, eid)+"/tcp-proxy", map[string]any{"container_port": in["applicationPort"]}); err != nil {
			return nil, err
		}
		n, err := fetchTCPProxy(p, sid, eid)
		if err != nil {
			return nil, err
		}
		if n == nil {
			return nil, gqlErr(http.StatusInternalServerError, "TCP proxy was not created")
		}
		return n, nil
	}))
	mutation.AddFieldConfig("tcpProxyDelete", resolved(nn(graphql.Boolean), args("id", reqStr), func(p graphql.ResolveParams) (any, error) {
		instanceID, ok := strings.CutPrefix(asString(p.Args["id"]), tcpProxyIDPrefix)
		if !ok || instanceID == "" {
			return nil, gqlErr(http.StatusBadRequest, "invalid TCP proxy id")
		}
		exec, ok := p.Context.Value(graphQLContextKey{}).(*graphQLExecution)
		if !ok || exec.store == nil {
			return nil, gqlErr(http.StatusServiceUnavailable, "store unavailable")
		}
		in, err := exec.store.GetInstance(p.Context, instanceID)
		if err != nil {
			return nil, gqlErr(http.StatusNotFound, "TCP proxy not found")
		}
		// REST가 권한을 검사한다(토큰 범위 밖이면 403/404).
		return restOK(p, http.MethodDelete, instancePath(in.ServiceID, in.EnvironmentID)+"/tcp-proxy", nil)
	}))
}
