package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/graphql-go/graphql"
)

// 사설 네트워크 GraphQL(Railway: privateNetworkEndpoint, privateNetworkEndpointNameAvailable, privateNetworkEndpointRename).
// aven은 환경마다 사설 네트워크가 하나라 privateNetworkId는 받기만 하고 쓰지 않는다. 서비스 인스턴스마다 endpoint가 하나라
// endpoint id(publicId)는 "pne_<instanceID>"다. 모두 REST로 옮겨 부르므로 권한 검사는 REST가 한다.

const privateEndpointIDPrefix = "pne_"

func privateEndpointNode(networking any, instanceID, sid, eid string) map[string]any {
	priv := asMap(asMap(networking)["private"])
	ips := []any{}
	ips = append(ips, asList(priv["ipv6"])...)
	ips = append(ips, asList(priv["ipv4"])...)
	return map[string]any{
		"publicId":          privateEndpointIDPrefix + instanceID,
		"dnsName":           priv["dnsName"],
		"fqdn":              priv["fqdn"],
		"aliases":           asList(priv["aliases"]),
		"privateIps":        ips,
		"serviceInstanceId": instanceID,
		"serviceId":         sid,
		"environmentId":     eid,
		"tags":              []any{"ipv4", "ipv6"},
	}
}

// endpointInstance는 privateNetworkEndpointRename 인자에서 (서비스, 환경)을 고른다.
// serviceId(aven 확장)가 있으면 그것을, 없으면 endpoint id(pne_<instanceID>, PrivateNetworkEndpoint.publicId)로 저장소에서 찾는다.
func endpointInstance(p graphql.ResolveParams) (sid, eid string, err error) {
	sid, eid = asString(p.Args["serviceId"]), asString(p.Args["environmentId"])
	if sid != "" {
		return sid, eid, nil
	}
	instanceID, ok := strings.CutPrefix(asString(p.Args["id"]), privateEndpointIDPrefix)
	if !ok || instanceID == "" {
		return "", "", gqlErr(http.StatusBadRequest, "id or serviceId is required")
	}
	exec, ok := p.Context.Value(graphQLContextKey{}).(*graphQLExecution)
	if !ok || exec.store == nil {
		return "", "", gqlErr(http.StatusServiceUnavailable, "store unavailable")
	}
	in, err := exec.store.GetInstance(p.Context, instanceID)
	if err != nil {
		return "", "", gqlErr(http.StatusNotFound, "private network endpoint not found")
	}
	if eid != "" && eid != in.EnvironmentID {
		return "", "", gqlErr(http.StatusBadRequest, "private network endpoint belongs to another environment")
	}
	return in.ServiceID, in.EnvironmentID, nil
}

// addPrivateNetworkFields는 스키마에 사설 네트워크 query/mutation을 더한다.
func addPrivateNetworkFields(query, mutation *graphql.Object) {
	endpoint := graphql.NewObject(graphql.ObjectConfig{Name: "PrivateNetworkEndpoint", Fields: graphql.Fields{
		"publicId":          field(nn(graphql.String)),
		"dnsName":           field(nn(graphql.String)),
		"privateIps":        field(nn(list(graphql.String))),
		"serviceInstanceId": field(nn(graphql.String)),
		"tags":              field(nn(list(graphql.String))),
		// aven 확장
		"fqdn":          field(optStr),
		"aliases":       field(nn(list(graphql.String))),
		"serviceId":     field(optStr),
		"environmentId": field(optStr),
	}})
	query.AddFieldConfig("privateNetworkEndpoint", resolved(endpoint, args("serviceId", reqStr, "environmentId", reqStr, "privateNetworkId", optStr), func(p graphql.ResolveParams) (any, error) {
		sid, eid := asString(p.Args["serviceId"]), asString(p.Args["environmentId"])
		in, err := getInstance(p, sid, eid)
		if err != nil {
			return nil, err
		}
		v, err := rest(p, http.MethodGet, instancePath(sid, eid)+"/networking", nil)
		if err != nil {
			return nil, err
		}
		return privateEndpointNode(v, asString(in["id"]), sid, eid), nil
	}))
	// serviceId(aven 확장)가 있으면 그 서비스가 이미 쓰는 이름(자기 서비스 이름 포함)은 쓸 수 있다고 본다.
	query.AddFieldConfig("privateNetworkEndpointNameAvailable", resolved(nn(graphql.Boolean),
		args("environmentId", reqStr, "prefix", reqStr, "privateNetworkId", optStr, "serviceId", optStr), func(p graphql.ResolveParams) (any, error) {
			q := "?name=" + url.QueryEscape(asString(p.Args["prefix"]))
			path := "/api/environments/" + esc(p.Args["environmentId"]) + "/private-dns/available" + q
			if sid := asString(p.Args["serviceId"]); sid != "" {
				path = instancePath(sid, p.Args["environmentId"]) + "/private-dns/available" + q
			}
			v, err := rest(p, http.MethodGet, path, nil)
			if err != nil {
				return nil, err
			}
			return asMap(v)["available"] == true, nil
		}))
	// 이름 변경은 PATCH instance {private_dns_name}과 같다: 대기 변경으로 저장되고 다음 배포부터 풀린다.
	// 서비스 이름과 같은 값이나 빈 값은 기본값(서비스 이름만)으로 되돌린다.
	mutation.AddFieldConfig("privateNetworkEndpointRename", resolved(nn(graphql.Boolean),
		args("id", optStr, "dnsName", reqStr, "environmentId", reqStr, "privateNetworkId", optStr, "serviceId", optStr),
		func(p graphql.ResolveParams) (any, error) {
			sid, eid, err := endpointInstance(p)
			if err != nil {
				return nil, err
			}
			return restOK(p, http.MethodPatch, instancePath(sid, eid), map[string]any{"private_dns_name": p.Args["dnsName"]})
		}))
}
