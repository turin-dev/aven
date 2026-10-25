package api

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/graphql-go/graphql"

	"github.com/turin-dev/aven/internal/store"
)

// GraphQL 스키마. 이름과 인자는 Railway Public API(https://docs.railway.com/integrations/api)를 따른다.

var githubShorthandRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func repoURL(v any) any {
	s := strings.TrimSpace(asString(v))
	if githubShorthandRe.MatchString(s) {
		return "https://github.com/" + s
	}
	return v
}

func field(t graphql.Output) *graphql.Field { return &graphql.Field{Type: t} }

// from은 source 맵의 다른 키를 내보내는 필드다.
func from(key string, t graphql.Output) *graphql.Field {
	return &graphql.Field{Type: t, Resolve: func(p graphql.ResolveParams) (any, error) { return asMap(p.Source)[key], nil }}
}

func resolved(t graphql.Output, args graphql.FieldConfigArgument, fn graphql.FieldResolveFn) *graphql.Field {
	return &graphql.Field{Type: t, Args: args, Resolve: fn}
}

func nn(t graphql.Type) *graphql.NonNull { return graphql.NewNonNull(t) }

func list(t graphql.Type) *graphql.List { return graphql.NewList(nn(t)) }

var (
	reqStr = nn(graphql.String)
	optStr = graphql.String
)

func args(kv ...any) graphql.FieldConfigArgument {
	out := graphql.FieldConfigArgument{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = &graphql.ArgumentConfig{Type: kv[i+1].(graphql.Input)}
	}
	return out
}

func inputObject(name string, kv ...any) *graphql.InputObject {
	fields := graphql.InputObjectConfigFieldMap{}
	for i := 0; i+1 < len(kv); i += 2 {
		fields[kv[i].(string)] = &graphql.InputObjectFieldConfig{Type: kv[i+1].(graphql.Input)}
	}
	return graphql.NewInputObject(graphql.InputObjectConfig{Name: name, Fields: fields})
}

func connection(name string, node graphql.Type, pageInfo *graphql.Object) *graphql.Object {
	edge := graphql.NewObject(graphql.ObjectConfig{Name: name + "Edge", Fields: graphql.Fields{
		"cursor": field(reqStr),
		"node":   field(nn(node)),
	}})
	return graphql.NewObject(graphql.ObjectConfig{Name: name + "Connection", Fields: graphql.Fields{
		"edges":      field(nn(list(edge))),
		"pageInfo":   field(nn(pageInfo)),
		"totalCount": field(nn(graphql.Int)),
	}})
}

// skippable은 목록을 모으며 건너뛸 오류다(그 환경/서비스에 인스턴스가 없거나 토큰 범위 밖).
func skippable(err error) bool {
	ge, ok := err.(*graphQLError)
	return ok && (ge.status == http.StatusNotFound || ge.status == http.StatusForbidden)
}

// projectDetail은 프로젝트 맵에 environments/services가 없으면(목록 응답) 상세를 불러온다.
func projectDetail(p graphql.ResolveParams, project map[string]any) (map[string]any, error) {
	if _, ok := project["services"]; ok {
		return project, nil
	}
	v, err := rest(p, http.MethodGet, "/api/projects/"+esc(project["id"]), nil)
	return asMap(v), err
}

func getInstance(p graphql.ResolveParams, sid, eid any) (map[string]any, error) {
	v, err := rest(p, http.MethodGet, instancePath(sid, eid), nil)
	if err != nil {
		return nil, err
	}
	return asMap(v), nil
}

// findInProject는 id로 프로젝트의 environments/services 중 하나를 찾는다(REST에 단건 조회가 없는 리소스).
func findInProject(p graphql.ResolveParams, pid, key, id string) (any, error) {
	v, err := rest(p, http.MethodGet, "/api/projects/"+esc(pid), nil)
	if err != nil {
		return nil, err
	}
	for _, it := range asList(asMap(v)[key]) {
		if asString(asMap(it)["id"]) == id {
			return it, nil
		}
	}
	return nil, gqlErr(http.StatusNotFound, "not found")
}

// instanceInputKeys는 ServiceInstanceUpdateInput 필드를 REST PATCH 키로 옮긴다.
var instanceInputKeys = map[string]string{
	"startCommand":            "start_command",
	"preDeployCommand":        "pre_deploy_command",
	"rootDirectory":           "root_dir",
	"healthcheckPath":         "healthcheck_path",
	"healthcheckTimeout":      "healthcheck_timeout_s",
	"numReplicas":             "replicas",
	"restartPolicyType":       "restart_policy",
	"restartPolicyMaxRetries": "restart_max_retries",
	"cronSchedule":            "cron_schedule",
	"sleepApplication":        "sleep_when_idle",
	"dockerfilePath":          "dockerfile_path",
	"watchPatterns":           "watch_paths",
	"branch":                  "branch",
	"autoDeploy":              "auto_deploy",
	"port":                    "port",
	"ipv6EgressEnabled":       "outbound_ipv6", // 저장하지 않고 바로 반영된다(REST와 같다)
}

func instancePatch(input map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range input {
		if key, ok := instanceInputKeys[k]; ok {
			out[key] = v
		}
	}
	if src := asMap(input["source"]); src != nil {
		if v, ok := src["image"]; ok {
			out["image"] = v
		}
		if v, ok := src["repo"]; ok {
			out["repo_url"] = repoURL(v)
		}
	}
	return out
}

// (한 번도 배포하지 않은 서비스를 변수 변경만으로 빌드하지 않는다).
func redeployIfLive(p graphql.ResolveParams, sid, eid any, skip bool) error {
	if skip {
		return nil
	}
	in, err := getInstance(p, sid, eid)
	if err != nil {
		return err
	}
	if asString(in["currentDeploymentId"]) == "" {
		return nil
	}
	_, err = rest(p, http.MethodPost, instancePath(sid, eid)+"/deploy", map[string]any{})
	return err
}

// sharedVars는 환경 공유 변수와 봉인 목록이다.
func sharedVars(p graphql.ResolveParams, eid any) (map[string]any, []any, error) {
	v, err := rest(p, http.MethodGet, "/api/environments/"+esc(eid)+"/shared-variables?detail=1", nil)
	if err != nil {
		return nil, nil, err
	}
	m := asMap(v)
	values := asMap(m["values"])
	if values == nil {
		values = map[string]any{}
	}
	return values, asList(m["sealed"]), nil
}

// upsertVariables는 variableUpsert/variableCollectionUpsert/variableDelete의 공통 구현이다.
// vars의 nil 값은 지우기다. serviceId가 없으면 환경 공유 변수를 바꾼다.
func upsertVariables(p graphql.ResolveParams, input map[string]any, vars map[string]any, replace bool) (any, error) {
	eid, sid := input["environmentId"], input["serviceId"]
	skip, _ := input["skipDeploys"].(bool)
	if asString(sid) != "" {
		patch := map[string]any{}
		if replace {
			clean := map[string]any{}
			for k, v := range vars {
				if v != nil {
					clean[k] = v
				}
			}
			patch["variables"] = clean
		} else {
			patch["variables_patch"] = vars
		}
		if sealed, ok := input["sealed"]; ok && sealed != nil {
			patch["sealed_variables"] = sealed
		}
		if _, err := rest(p, http.MethodPatch, instancePath(sid, eid), patch); err != nil {
			return nil, err
		}
		if err := redeployIfLive(p, sid, eid, skip); err != nil {
			return nil, err
		}
		return true, nil
	}
	values, sealed, err := sharedVars(p, eid)
	if err != nil {
		return nil, err
	}
	if replace {
		values = map[string]any{}
	}
	for k, v := range vars {
		if v == nil {
			delete(values, k)
		} else {
			values[k] = v
		}
	}
	body := map[string]any{"values": values, "sealed": sealed}
	if s, ok := input["sealed"]; ok && s != nil {
		body["sealed"] = append(sealed, asList(s)...)
	}
	return restOK(p, http.MethodPut, "/api/environments/"+esc(eid)+"/shared-variables", body)
}

func newGraphQLSchema() (graphql.Schema, error) {
	jsonScalar := newJSONScalar()
	pageInfo := graphql.NewObject(graphql.ObjectConfig{Name: "PageInfo", Fields: graphql.Fields{
		"hasNextPage":     field(nn(graphql.Boolean)),
		"hasPreviousPage": field(nn(graphql.Boolean)),
		"endCursor":       field(optStr),
	}})
	restartPolicy := graphql.NewEnum(graphql.EnumConfig{Name: "RestartPolicyType", Values: graphql.EnumValueConfigMap{
		"ON_FAILURE": &graphql.EnumValueConfig{Value: "on_failure"},
		"ALWAYS":     &graphql.EnumValueConfig{Value: "always"},
		"NEVER":      &graphql.EnumValueConfig{Value: "never"},
	}})

	user := graphql.NewObject(graphql.ObjectConfig{Name: "User", Fields: graphql.Fields{
		"id":    field(nn(graphql.ID)),
		"email": field(optStr),
		"name":  field(optStr),
		"role":  field(optStr),
		"avatar": resolved(optStr, nil, func(graphql.ResolveParams) (any, error) {
			return nil, nil
		}),
	}})

	// ---- 도메인 ----
	domainIDField := resolved(nn(graphql.String), nil, func(p graphql.ResolveParams) (any, error) {
		m := asMap(p.Source)
		return domainID(asString(m["serviceId"]), asString(m["environmentId"]), asString(m["host"])), nil
	})
	dnsRecord := graphql.NewObject(graphql.ObjectConfig{Name: "DNSRecords", Fields: graphql.Fields{
		"hostlabel":     from("name", optStr),
		"fqdn":          from("name", optStr),
		"recordType":    from("type", optStr),
		"requiredValue": from("value", optStr),
		"currentValue": resolved(optStr, nil, func(p graphql.ResolveParams) (any, error) {
			var vals []string
			for _, v := range asList(asMap(p.Source)["current"]) {
				vals = append(vals, asString(v))
			}
			if len(vals) == 0 {
				return nil, nil
			}
			return strings.Join(vals, ", "), nil
		}),
		"status": from("status", optStr),
	}})
	customDomainStatus := graphql.NewObject(graphql.ObjectConfig{Name: "CustomDomainStatus", Fields: graphql.Fields{
		"dnsRecords": resolved(nn(list(dnsRecord)), nil, func(p graphql.ResolveParams) (any, error) {
			d := asMap(p.Source)
			st := railwayDNSStatus(asString(d["status"]))
			out := []any{}
			for _, r := range asList(d["dns"]) {
				m := asMap(r)
				// 레코드별 상태(ok | proxied | mismatch | pending)가 있으면 그것을, 없으면 도메인 상태를 쓴다.
				if rs := asString(m["status"]); rs != "" {
					m["status"] = railwayRecordStatus(rs)
				} else {
					m["status"] = st
				}
				out = append(out, m)
			}
			return out, nil
		}),
		// proxied(Cloudflare·터널 뒤)도 정상 연결로 본다.
		"verified": resolved(nn(graphql.Boolean), nil, func(p graphql.ResolveParams) (any, error) {
			st := asString(asMap(p.Source)["status"])
			return st == "active" || st == "proxied", nil
		}),
		"keelStatus": from("status", optStr),
		// cdnProvider: CLOUDFLARE(프록시·터널) | null.
		"cdnProvider": resolved(optStr, nil, func(p graphql.ResolveParams) (any, error) {
			switch asString(asMap(p.Source)["proxy"]) {
			case "cloudflare", "tunnel":
				return "CLOUDFLARE", nil
			}
			return nil, nil
		}),
		// certificateStatus: VALID | INVALID | null(모름).
		"certificateStatus": resolved(optStr, nil, func(p graphql.ResolveParams) (any, error) {
			if c := asMap(asMap(p.Source)["cert"]); c != nil && asString(c["status"]) != "" {
				return strings.ToUpper(asString(c["status"])), nil
			}
			return nil, nil
		}),
	}})
	serviceDomain := graphql.NewObject(graphql.ObjectConfig{Name: "ServiceDomain", Fields: graphql.Fields{
		"id":     domainIDField,
		"domain": from("host", nn(graphql.String)),
		"suffix": resolved(optStr, nil, func(p graphql.ResolveParams) (any, error) {
			if _, suffix, ok := strings.Cut(asString(asMap(p.Source)["host"]), "."); ok {
				return suffix, nil
			}
			return nil, nil
		}),
		"targetPort":    field(graphql.Int),
		"serviceId":     field(optStr),
		"environmentId": field(optStr),
	}})
	customDomain := graphql.NewObject(graphql.ObjectConfig{Name: "CustomDomain", Fields: graphql.Fields{
		"id":            domainIDField,
		"domain":        from("host", nn(graphql.String)),
		"targetPort":    field(graphql.Int),
		"serviceId":     field(optStr),
		"environmentId": field(optStr),
		"status":        resolved(customDomainStatus, nil, func(p graphql.ResolveParams) (any, error) { return p.Source, nil }),
	}})
	allDomains := graphql.NewObject(graphql.ObjectConfig{Name: "AllDomains", Fields: graphql.Fields{
		"serviceDomains": field(nn(list(serviceDomain))),
		"customDomains":  field(nn(list(customDomain))),
	}})

	// ---- 볼륨 / 배포 ----
	volume := graphql.NewObject(graphql.ObjectConfig{Name: "Volume", Fields: graphql.Fields{
		"id":                field(nn(graphql.ID)),
		"name":              field(optStr),
		"projectId":         field(optStr),
		"environmentId":     field(optStr),
		"serviceInstanceId": field(optStr),
		"mountPath":         field(optStr),
		"sizeBytes":         field(graphql.Float),
		"currentSizeMB": resolved(graphql.Float, nil, func(p graphql.ResolveParams) (any, error) {
			if b, ok := asMap(p.Source)["sizeBytes"].(float64); ok {
				return b / (1 << 20), nil
			}
			return nil, nil
		}),
		"sizeLimitMb": field(graphql.Int),
		"createdAt":   field(optStr),
	}})
	deployment := graphql.NewObject(graphql.ObjectConfig{Name: "Deployment", Fields: graphql.Fields{
		"id":                field(nn(graphql.ID)),
		"serviceId":         field(optStr),
		"environmentId":     field(optStr),
		"serviceInstanceId": field(optStr),
		"status": resolved(optStr, nil, func(p graphql.ResolveParams) (any, error) {
			return railwayDeploymentStatus(asString(asMap(p.Source)["status"])), nil
		}),
		"keelStatus":    from("status", optStr),
		"trigger":       field(optStr),
		"image":         field(optStr),
		"branch":        field(optStr),
		"builderUsed":   field(optStr),
		"commitSha":     field(optStr),
		"commitMessage": field(optStr),
		"commitAuthor":  field(optStr),
		"error":         field(optStr),
		"createdAt":     field(optStr),
		"startedAt":     field(optStr),
		"finishedAt":    field(optStr),
		"meta": resolved(jsonScalar, nil, func(p graphql.ResolveParams) (any, error) {
			m := asMap(p.Source)
			return map[string]any{"commitHash": m["commitSha"], "commitMessage": m["commitMessage"], "commitAuthor": m["commitAuthor"],
				"branch": m["branch"], "image": m["image"], "trigger": m["trigger"]}, nil
		}),
	}})
	volumeConnection := connection("Volume", volume, pageInfo)
	deploymentConnection := connection("Deployment", deployment, pageInfo)

	// ---- 서비스 인스턴스 ----
	serviceSource := graphql.NewObject(graphql.ObjectConfig{Name: "ServiceSource", Fields: graphql.Fields{
		"image": field(optStr),
		"repo":  from("repoUrl", optStr),
	}})
	instance := graphql.NewObject(graphql.ObjectConfig{Name: "ServiceInstance", Fields: graphql.Fields{
		"id":                 field(nn(graphql.ID)),
		"serviceId":          field(optStr),
		"environmentId":      field(optStr),
		"status":             field(optStr),
		"source":             resolved(serviceSource, nil, func(p graphql.ResolveParams) (any, error) { return p.Source, nil }),
		"branch":             field(optStr),
		"rootDirectory":      from("rootDir", optStr),
		"startCommand":       field(optStr),
		"preDeployCommand":   field(optStr),
		"buildCommand":       field(optStr),
		"dockerfilePath":     field(optStr),
		"watchPatterns":      from("watchPaths", graphql.NewList(graphql.String)),
		"healthcheckPath":    field(optStr),
		"healthcheckTimeout": from("healthcheckTimeoutS", graphql.Int),
		"numReplicas":        from("replicas", graphql.Int),
		"currentReplicas":    field(graphql.Int),
		"restartPolicyType": resolved(optStr, nil, func(p graphql.ResolveParams) (any, error) {
			return strings.ToUpper(asString(asMap(p.Source)["restartPolicy"])), nil
		}),
		"restartPolicyMaxRetries": from("restartMaxRetries", graphql.Int),
		"cronSchedule":            field(optStr),
		"sleepApplication":        from("sleepWhenIdle", graphql.Boolean),
		"autoDeploy":              field(graphql.Boolean),
		"privateDomain":           field(optStr),
		// ipv6EgressEnabled는 아웃바운드 IPv6 설정(outbound_ipv6)이다. 값이 없으면 기본값(켜짐)이다.
		"ipv6EgressEnabled": resolved(nn(graphql.Boolean), nil, func(p graphql.ResolveParams) (any, error) {
			v, ok := asMap(p.Source)["outboundIpv6"].(bool)
			return !ok || v, nil
		}),
		"pendingChanges":      field(graphql.NewList(graphql.String)),
		"currentDeploymentId": field(optStr),
		"domains": resolved(nn(allDomains), nil, func(p graphql.ResolveParams) (any, error) {
			return splitDomains(asMap(p.Source)), nil
		}),
		"latestDeployment": resolved(deployment, nil, func(p graphql.ResolveParams) (any, error) {
			m := asMap(p.Source)
			v, err := rest(p, http.MethodGet, instancePath(m["serviceId"], m["environmentId"])+"/deployments?limit=1", nil)
			if err != nil {
				return nil, err
			}
			if l := asList(v); len(l) > 0 {
				return l[0], nil
			}
			return nil, nil
		}),
		"volumes": resolved(nn(list(volume)), nil, func(p graphql.ResolveParams) (any, error) {
			m := asMap(p.Source)
			v, err := rest(p, http.MethodGet, "/api/environments/"+esc(m["environmentId"])+"/volumes", nil)
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, it := range asList(v) {
				if asString(asMap(it)["serviceInstanceId"]) == asString(m["id"]) {
					out = append(out, it)
				}
			}
			return out, nil
		}),
	}})
	instanceConnection := connection("ServiceInstance", instance, pageInfo)

	// ---- 프로젝트 / 환경 / 서비스 (서로 참조하므로 thunk) ----
	var project, environment, service *graphql.Object
	var projectConnection, environmentConnection, serviceConnection *graphql.Object
	service = graphql.NewObject(graphql.ObjectConfig{Name: "Service", Fields: graphql.FieldsThunk(func() graphql.Fields {
		return graphql.Fields{
			"id":           field(nn(graphql.ID)),
			"projectId":    field(optStr),
			"name":         field(optStr),
			"icon":         field(optStr),
			"sourceType":   field(optStr),
			"databaseKind": field(optStr),
			"createdAt":    field(optStr),
			"project": resolved(project, nil, func(p graphql.ResolveParams) (any, error) {
				return rest(p, http.MethodGet, "/api/projects/"+esc(asMap(p.Source)["projectId"]), nil)
			}),
			"serviceInstance": resolved(instance, args("environmentId", reqStr), func(p graphql.ResolveParams) (any, error) {
				return getInstance(p, asMap(p.Source)["id"], p.Args["environmentId"])
			}),
			"serviceInstances": resolved(nn(instanceConnection), connectionArgs(nil), func(p graphql.ResolveParams) (any, error) {
				svc := asMap(p.Source)
				proj, err := rest(p, http.MethodGet, "/api/projects/"+esc(svc["projectId"]), nil)
				if err != nil {
					return nil, err
				}
				items := []any{}
				for _, env := range asList(asMap(proj)["environments"]) {
					in, err := getInstance(p, svc["id"], asMap(env)["id"])
					if skippable(err) {
						continue
					} else if err != nil {
						return nil, err
					}
					items = append(items, in)
				}
				return paginate(items, p.Args)
			}),
		}
	})})
	environment = graphql.NewObject(graphql.ObjectConfig{Name: "Environment", Fields: graphql.FieldsThunk(func() graphql.Fields {
		return graphql.Fields{
			"id":        field(nn(graphql.ID)),
			"projectId": field(optStr),
			"name":      field(optStr),
			"kind":      field(optStr),
			"isEphemeral": resolved(nn(graphql.Boolean), nil, func(p graphql.ResolveParams) (any, error) {
				return asString(asMap(p.Source)["kind"]) == "preview", nil
			}),
			"prNumber":  field(graphql.Int),
			"prUrl":     field(optStr),
			"prBranch":  field(optStr),
			"createdAt": field(optStr),
			"serviceInstances": resolved(nn(instanceConnection), connectionArgs(nil), func(p graphql.ResolveParams) (any, error) {
				env := asMap(p.Source)
				proj, err := rest(p, http.MethodGet, "/api/projects/"+esc(env["projectId"]), nil)
				if err != nil {
					return nil, err
				}
				items := []any{}
				for _, svc := range asList(asMap(proj)["services"]) {
					in, err := getInstance(p, asMap(svc)["id"], env["id"])
					if skippable(err) {
						continue
					} else if err != nil {
						return nil, err
					}
					items = append(items, in)
				}
				return paginate(items, p.Args)
			}),
			"volumes": resolved(nn(list(volume)), nil, func(p graphql.ResolveParams) (any, error) {
				v, err := rest(p, http.MethodGet, "/api/environments/"+esc(asMap(p.Source)["id"])+"/volumes", nil)
				return asList(v), err
			}),
		}
	})})
	project = graphql.NewObject(graphql.ObjectConfig{Name: "Project", Fields: graphql.FieldsThunk(func() graphql.Fields {
		return graphql.Fields{
			"id":                   field(nn(graphql.ID)),
			"name":                 field(optStr),
			"description":          field(optStr),
			"createdAt":            field(optStr),
			"defaultEnvironmentId": field(optStr),
			"environments": resolved(nn(environmentConnection), connectionArgs(nil), func(p graphql.ResolveParams) (any, error) {
				d, err := projectDetail(p, asMap(p.Source))
				if err != nil {
					return nil, err
				}
				return paginate(asList(d["environments"]), p.Args)
			}),
			"services": resolved(nn(serviceConnection), connectionArgs(nil), func(p graphql.ResolveParams) (any, error) {
				d, err := projectDetail(p, asMap(p.Source))
				if err != nil {
					return nil, err
				}
				return paginate(asList(d["services"]), p.Args)
			}),
			"volumes": resolved(nn(volumeConnection), connectionArgs(nil), func(p graphql.ResolveParams) (any, error) {
				d, err := projectDetail(p, asMap(p.Source))
				if err != nil {
					return nil, err
				}
				items := []any{}
				for _, env := range asList(d["environments"]) {
					v, err := rest(p, http.MethodGet, "/api/environments/"+esc(asMap(env)["id"])+"/volumes", nil)
					if skippable(err) {
						continue
					} else if err != nil {
						return nil, err
					}
					items = append(items, asList(v)...)
				}
				return paginate(items, p.Args)
			}),
		}
	})})
	projectConnection = connection("Project", project, pageInfo)
	environmentConnection = connection("Environment", environment, pageInfo)
	serviceConnection = connection("Service", service, pageInfo)

	query := graphql.NewObject(graphql.ObjectConfig{Name: "Query", Fields: graphql.Fields{
		"me": resolved(nn(user), nil, func(p graphql.ResolveParams) (any, error) {
			return rest(p, http.MethodGet, "/api/auth/me", nil)
		}),
		// Workspace filtering is enforced by the REST authorization boundary.
		"projects": resolved(nn(projectConnection), connectionArgs(args("workspaceId", optStr)), func(p graphql.ResolveParams) (any, error) {
			path := "/api/projects"
			if id := asString(p.Args["workspaceId"]); id != "" {
				path += "?workspace=" + url.QueryEscape(id)
			}
			v, err := rest(p, http.MethodGet, path, nil)
			if err != nil {
				return nil, err
			}
			return paginate(asList(v), p.Args)
		}),
		"project": resolved(project, args("id", reqStr), func(p graphql.ResolveParams) (any, error) {
			return rest(p, http.MethodGet, "/api/projects/"+esc(p.Args["id"]), nil)
		}),
		"service": resolved(service, args("id", reqStr), func(p graphql.ResolveParams) (any, error) {
			id := asString(p.Args["id"])
			pid, err := ownerProject(p, func(st *store.Store) (string, error) {
				svc, err := st.GetService(p.Context, id)
				if err != nil {
					return "", err
				}
				return svc.ProjectID, nil
			})
			if err != nil {
				return nil, err
			}
			return findInProject(p, pid, "services", id)
		}),
		"environments": resolved(nn(environmentConnection), connectionArgs(args("projectId", reqStr, "isEphemeral", graphql.Boolean)), func(p graphql.ResolveParams) (any, error) {
			v, err := rest(p, http.MethodGet, "/api/projects/"+esc(p.Args["projectId"]), nil)
			if err != nil {
				return nil, err
			}
			items := asList(asMap(v)["environments"])
			if want, ok := p.Args["isEphemeral"].(bool); ok {
				kept := []any{}
				for _, e := range items {
					if (asString(asMap(e)["kind"]) == "preview") == want {
						kept = append(kept, e)
					}
				}
				items = kept
			}
			return paginate(items, p.Args)
		}),
		"environment": resolved(environment, args("id", reqStr, "projectId", optStr), func(p graphql.ResolveParams) (any, error) {
			id := asString(p.Args["id"])
			pid, err := ownerProject(p, func(st *store.Store) (string, error) {
				env, err := st.GetEnvironment(p.Context, id)
				if err != nil {
					return "", err
				}
				return env.ProjectID, nil
			})
			if err != nil {
				return nil, err
			}
			return findInProject(p, pid, "environments", id)
		}),
		"serviceInstance": resolved(instance, args("serviceId", reqStr, "environmentId", reqStr), func(p graphql.ResolveParams) (any, error) {
			return getInstance(p, p.Args["serviceId"], p.Args["environmentId"])
		}),
		"deployments": resolved(nn(deploymentConnection), connectionArgs(args("input", nn(inputObject("DeploymentListInput",
			"projectId", optStr, "serviceId", reqStr, "environmentId", optStr,
			"status", inputObject("DeploymentStatusInput", "successfulOnly", graphql.Boolean),
		)))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			eid := asString(in["environmentId"])
			if eid == "" {
				pid := asString(in["projectId"])
				if pid == "" {
					return nil, gqlErr(http.StatusBadRequest, "environmentId or projectId is required")
				}
				v, err := rest(p, http.MethodGet, "/api/projects/"+esc(pid), nil)
				if err != nil {
					return nil, err
				}
				eid = asString(asMap(v)["defaultEnvironmentId"])
			}
			v, err := rest(p, http.MethodGet, instancePath(in["serviceId"], eid)+"/deployments?limit=500", nil)
			if err != nil {
				return nil, err
			}
			items := asList(v)
			if asMap(in["status"])["successfulOnly"] == true {
				kept := []any{}
				for _, d := range items {
					if asString(asMap(d)["status"]) == "live" {
						kept = append(kept, d)
					}
				}
				items = kept
			}
			return paginate(items, p.Args)
		}),
		"deployment": resolved(deployment, args("id", reqStr), func(p graphql.ResolveParams) (any, error) {
			return rest(p, http.MethodGet, "/api/deployments/"+esc(p.Args["id"]), nil)
		}),
		// variables는 기본으로 참조를 푼 값이다(unrendered: true면 ${{...}} 그대로). serviceId가 없으면 환경 공유 변수다.
		"variables": resolved(nn(jsonScalar), args("projectId", optStr, "environmentId", reqStr, "serviceId", optStr, "unrendered", graphql.Boolean), func(p graphql.ResolveParams) (any, error) {
			sid, eid := p.Args["serviceId"], p.Args["environmentId"]
			if asString(sid) == "" {
				values, _, err := sharedVars(p, eid)
				return values, err
			}
			if p.Args["unrendered"] == true {
				in, err := getInstance(p, sid, eid)
				if err != nil {
					return nil, err
				}
				return in["variables"], nil
			}
			return resolvedVariables(p, sid, eid)
		}),
		"variablesForServiceDeployment": resolved(nn(jsonScalar), args("projectId", optStr, "environmentId", reqStr, "serviceId", reqStr), func(p graphql.ResolveParams) (any, error) {
			return resolvedVariables(p, p.Args["serviceId"], p.Args["environmentId"])
		}),
		"domains": resolved(nn(allDomains), args("projectId", optStr, "environmentId", reqStr, "serviceId", reqStr), func(p graphql.ResolveParams) (any, error) {
			in, err := getInstance(p, p.Args["serviceId"], p.Args["environmentId"])
			if err != nil {
				return nil, err
			}
			return splitDomains(in), nil
		}),
		"customDomain": resolved(customDomain, args("id", reqStr, "projectId", optStr), func(p graphql.ResolveParams) (any, error) {
			return findDomain(p, asString(p.Args["id"]))
		}),
	}})

	// ---- mutations ----
	idArgs := args("id", reqStr)
	mutation := graphql.NewObject(graphql.ObjectConfig{Name: "Mutation", Fields: graphql.Fields{
		"projectCreate": resolved(project, args("input", nn(inputObject("ProjectCreateInput",
			"name", optStr, "description", optStr, "workspaceId", optStr,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			body := map[string]any{"name": in["name"], "description": in["description"], "workspace_id": in["workspaceId"]}
			if asString(in["name"]) == "" {
				body["name"] = "new-project"
			}
			return rest(p, http.MethodPost, "/api/projects", body)
		}),
		"projectUpdate": resolved(project, args("id", reqStr, "input", nn(inputObject("ProjectUpdateInput",
			"name", optStr, "description", optStr, "defaultEnvironmentId", optStr,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			body := map[string]any{}
			for k, key := range map[string]string{"name": "name", "description": "description", "defaultEnvironmentId": "default_environment_id"} {
				if v, ok := in[k]; ok {
					body[key] = v
				}
			}
			return rest(p, http.MethodPatch, "/api/projects/"+esc(p.Args["id"]), body)
		}),
		"projectDelete": resolved(nn(graphql.Boolean), idArgs, func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodDelete, "/api/projects/"+esc(p.Args["id"]), nil)
		}),

		"serviceCreate": resolved(service, args("input", nn(inputObject("ServiceCreateInput",
			"projectId", reqStr, "name", optStr, "icon", optStr, "branch", optStr, "environmentId", optStr, "variables", jsonScalar,
			"source", inputObject("ServiceSourceInput", "repo", optStr, "image", optStr, "databaseKind", optStr),
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])

			body := map[string]any{"source_type": "image"}
			for k, key := range map[string]string{"name": "name", "branch": "branch", "environmentId": "environment_id"} {
				if v, ok := in[k]; ok && v != nil {
					body[key] = v
				}
			}
			src := asMap(in["source"])
			switch {
			case src["repo"] != nil:
				body["source_type"], body["repo_url"] = "repo", repoURL(src["repo"])
			case src["image"] != nil:
				body["image"] = src["image"]
			case src["databaseKind"] != nil:
				body["source_type"], body["database_kind"] = "database", src["databaseKind"]
			}
			v, err := rest(p, http.MethodPost, "/api/projects/"+esc(in["projectId"])+"/services", body)
			if err != nil {
				return nil, err
			}
			svc := asMap(v)
			if icon, ok := in["icon"]; ok && icon != nil {
				if _, err := rest(p, http.MethodPatch, "/api/services/"+esc(svc["id"]), map[string]any{"icon": icon}); err != nil {
					return nil, err
				}
				svc["icon"] = icon
			}
			if vars := asMap(in["variables"]); len(vars) > 0 {
				eid := in["environmentId"]
				if asString(eid) == "" {
					proj, err := rest(p, http.MethodGet, "/api/projects/"+esc(in["projectId"]), nil)
					if err != nil {
						return nil, err
					}
					eid = asMap(proj)["defaultEnvironmentId"]
				}
				if _, err := rest(p, http.MethodPatch, instancePath(svc["id"], eid), map[string]any{"variables_patch": vars}); err != nil {
					return nil, err
				}
			}
			return svc, nil
		}),
		"serviceUpdate": resolved(service, args("id", reqStr, "input", nn(inputObject("ServiceUpdateInput", "name", optStr, "icon", optStr))), func(p graphql.ResolveParams) (any, error) {
			return rest(p, http.MethodPatch, "/api/services/"+esc(p.Args["id"]), p.Args["input"])
		}),
		"serviceDelete": resolved(nn(graphql.Boolean), args("id", reqStr, "environmentId", optStr), func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodDelete, "/api/services/"+esc(p.Args["id"]), nil)
		}),
		"serviceInstanceUpdate": resolved(nn(graphql.Boolean), args("serviceId", reqStr, "environmentId", reqStr, "input", nn(inputObject("ServiceInstanceUpdateInput",
			"startCommand", optStr, "preDeployCommand", optStr, "rootDirectory", optStr, "healthcheckPath", optStr,
			"healthcheckTimeout", graphql.Int, "numReplicas", graphql.Int, "restartPolicyType", restartPolicy,
			"restartPolicyMaxRetries", graphql.Int, "cronSchedule", optStr, "sleepApplication", graphql.Boolean,
			"dockerfilePath", optStr, "watchPatterns", graphql.NewList(reqStr), "branch", optStr, "autoDeploy", graphql.Boolean,
			"port", graphql.Int, "source", inputObject("ServiceSourceUpdateInput", "repo", optStr, "image", optStr),
			"ipv6EgressEnabled", graphql.Boolean,
		))), func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodPatch, instancePath(p.Args["serviceId"], p.Args["environmentId"]), instancePatch(asMap(p.Args["input"])))
		}),
		"serviceInstanceDeploy": resolved(nn(graphql.Boolean), args("serviceId", reqStr, "environmentId", reqStr, "latestCommit", graphql.Boolean), func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodPost, instancePath(p.Args["serviceId"], p.Args["environmentId"])+"/deploy", map[string]any{})
		}),
		"serviceInstanceDeployV2": resolved(nn(graphql.String), args("serviceId", reqStr, "environmentId", reqStr, "commitSha", optStr), func(p graphql.ResolveParams) (any, error) {
			v, err := rest(p, http.MethodPost, instancePath(p.Args["serviceId"], p.Args["environmentId"])+"/deploy", map[string]any{})
			if err != nil {
				return nil, err
			}
			return asMap(v)["id"], nil
		}),
		// serviceInstanceRedeploy는 가장 최근 배포를 같은 이미지로 다시 배포한다.
		"serviceInstanceRedeploy": resolved(nn(graphql.Boolean), args("serviceId", reqStr, "environmentId", reqStr), func(p graphql.ResolveParams) (any, error) {
			v, err := rest(p, http.MethodGet, instancePath(p.Args["serviceId"], p.Args["environmentId"])+"/deployments?limit=1", nil)
			if err != nil {
				return nil, err
			}
			l := asList(v)
			if len(l) == 0 {
				return nil, gqlErr(http.StatusBadRequest, "this service has no deployment to redeploy")
			}
			return restOK(p, http.MethodPost, "/api/deployments/"+esc(asMap(l[0])["id"])+"/redeploy", map[string]any{})
		}),

		"deploymentRedeploy": resolved(deployment, idArgs, func(p graphql.ResolveParams) (any, error) {
			return rest(p, http.MethodPost, "/api/deployments/"+esc(p.Args["id"])+"/redeploy", map[string]any{})
		}),
		"deploymentRollback": resolved(deployment, idArgs, func(p graphql.ResolveParams) (any, error) {
			return rest(p, http.MethodPost, "/api/deployments/"+esc(p.Args["id"])+"/rollback", map[string]any{})
		}),
		"deploymentCancel": resolved(nn(graphql.Boolean), idArgs, func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodPost, "/api/deployments/"+esc(p.Args["id"])+"/cancel", map[string]any{})
		}),
		"deploymentRestart": resolved(nn(graphql.Boolean), idArgs, func(p graphql.ResolveParams) (any, error) {
			return deploymentInstanceAction(p, "restart")
		}),
		"deploymentStop": resolved(nn(graphql.Boolean), idArgs, func(p graphql.ResolveParams) (any, error) {
			return deploymentInstanceAction(p, "stop")
		}),

		"environmentCreate": resolved(environment, args("input", nn(inputObject("EnvironmentCreateInput",
			"projectId", reqStr, "name", reqStr, "sourceEnvironmentId", optStr, "ephemeral", graphql.Boolean, "skipInitialDeploys", graphql.Boolean,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			body := map[string]any{"name": in["name"]}
			if src := asString(in["sourceEnvironmentId"]); src != "" {
				body["copy_from"] = src
			}
			return rest(p, http.MethodPost, "/api/projects/"+esc(in["projectId"])+"/environments", body)
		}),
		"environmentRename": resolved(nn(graphql.Boolean), args("id", reqStr, "input", nn(inputObject("EnvironmentRenameInput", "name", reqStr))), func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodPatch, "/api/environments/"+esc(p.Args["id"]), map[string]any{"name": asMap(p.Args["input"])["name"]})
		}),
		"environmentDelete": resolved(nn(graphql.Boolean), idArgs, func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodDelete, "/api/environments/"+esc(p.Args["id"]), nil)
		}),

		"variableUpsert": resolved(nn(graphql.Boolean), args("input", nn(inputObject("VariableUpsertInput",
			"projectId", optStr, "environmentId", reqStr, "serviceId", optStr, "name", reqStr, "value", reqStr, "skipDeploys", graphql.Boolean,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			return upsertVariables(p, in, map[string]any{asString(in["name"]): in["value"]}, false)
		}),
		// sealed는 aven 확장이다(값을 다시 읽을 수 없게 봉인할 변수 이름).
		"variableCollectionUpsert": resolved(nn(graphql.Boolean), args("input", nn(inputObject("VariableCollectionUpsertInput",
			"projectId", optStr, "environmentId", reqStr, "serviceId", optStr, "variables", nn(jsonScalar),
			"replace", graphql.Boolean, "skipDeploys", graphql.Boolean, "sealed", graphql.NewList(reqStr),
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			vars := asMap(in["variables"])
			if vars == nil {
				return nil, gqlErr(http.StatusBadRequest, "variables must be an object")
			}
			replace, _ := in["replace"].(bool)
			return upsertVariables(p, in, vars, replace)
		}),
		"variableDelete": resolved(nn(graphql.Boolean), args("input", nn(inputObject("VariableDeleteInput",
			"projectId", optStr, "environmentId", reqStr, "serviceId", optStr, "name", reqStr, "skipDeploys", graphql.Boolean,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			return upsertVariables(p, in, map[string]any{asString(in["name"]): nil}, false)
		}),

		"serviceDomainCreate": resolved(serviceDomain, args("input", nn(inputObject("ServiceDomainCreateInput",
			"serviceId", reqStr, "environmentId", reqStr, "targetPort", graphql.Int,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			return addDomain(p, in, nil)
		}),
		"serviceDomainUpdate": resolved(nn(graphql.Boolean), args("input", nn(inputObject("ServiceDomainUpdateInput",
			"serviceDomainId", optStr, "serviceId", optStr, "environmentId", optStr, "domain", optStr, "targetPort", graphql.Int,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			sid, eid, host := asString(in["serviceId"]), asString(in["environmentId"]), asString(in["domain"])
			body := optionalPort(in)
			if id := asString(in["serviceDomainId"]); id != "" {
				var err error
				if sid, eid, host, err = parseDomainID(id); err != nil {
					return nil, err
				}

				if d := strings.ToLower(asString(in["domain"])); d != "" && d != strings.ToLower(host) {
					body["host"] = d
				}
			}
			if sid == "" || eid == "" || host == "" {
				return nil, gqlErr(http.StatusBadRequest, "serviceDomainId (or serviceId, environmentId and domain) is required")
			}
			return restOK(p, http.MethodPatch, instancePath(sid, eid)+"/domains/"+esc(host), body)
		}),
		"serviceDomainDelete": resolved(nn(graphql.Boolean), idArgs, func(p graphql.ResolveParams) (any, error) {
			return deleteDomain(p, asString(p.Args["id"]))
		}),
		"customDomainCreate": resolved(customDomain, args("input", nn(inputObject("CustomDomainCreateInput",
			"projectId", optStr, "serviceId", reqStr, "environmentId", reqStr, "domain", reqStr, "targetPort", graphql.Int,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			return addDomain(p, in, in["domain"])
		}),
		"customDomainUpdate": resolved(nn(graphql.Boolean), args("id", reqStr, "environmentId", optStr, "targetPort", graphql.Int), func(p graphql.ResolveParams) (any, error) {
			sid, eid, host, err := parseDomainID(asString(p.Args["id"]))
			if err != nil {
				return nil, err
			}
			return restOK(p, http.MethodPatch, instancePath(sid, eid)+"/domains/"+esc(host), optionalPort(p.Args))
		}),
		"customDomainDelete": resolved(nn(graphql.Boolean), idArgs, func(p graphql.ResolveParams) (any, error) {
			return deleteDomain(p, asString(p.Args["id"]))
		}),

		"volumeCreate": resolved(volume, args("input", nn(inputObject("VolumeCreateInput",
			"projectId", reqStr, "serviceId", reqStr, "mountPath", reqStr, "environmentId", optStr, "name", optStr, "sizeLimitMb", graphql.Int,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			eid := asString(in["environmentId"])
			if eid == "" {
				v, err := rest(p, http.MethodGet, "/api/projects/"+esc(in["projectId"]), nil)
				if err != nil {
					return nil, err
				}
				eid = asString(asMap(v)["defaultEnvironmentId"])
			}
			body := map[string]any{"service_id": in["serviceId"], "mount_path": in["mountPath"]}
			if in["name"] != nil {
				body["name"] = in["name"]
			}
			if in["sizeLimitMb"] != nil {
				body["size_limit_mb"] = in["sizeLimitMb"]
			}
			return rest(p, http.MethodPost, "/api/environments/"+esc(eid)+"/volumes", body)
		}),
		"volumeUpdate": resolved(volume, args("volumeId", reqStr, "input", nn(inputObject("VolumeUpdateInput", "name", optStr, "sizeLimitMb", graphql.Int))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			body := map[string]any{}
			if v, ok := in["name"]; ok {
				body["name"] = v
			}
			if v, ok := in["sizeLimitMb"]; ok {
				body["size_limit_mb"] = v
			}
			return rest(p, http.MethodPatch, "/api/volumes/"+esc(p.Args["volumeId"]), body)
		}),

		"volumeInstanceUpdate": resolved(nn(graphql.Boolean), args("volumeId", reqStr, "environmentId", optStr, "input", nn(inputObject("VolumeInstanceUpdateInput",
			"mountPath", optStr, "serviceId", optStr,
		))), func(p graphql.ResolveParams) (any, error) {
			in := asMap(p.Args["input"])
			body := map[string]any{}
			if v, ok := in["mountPath"]; ok {
				body["mount_path"] = v
			}
			if v, ok := in["serviceId"]; ok && asString(v) == "" {
				body["service_instance_id"] = nil // serviceId: ""(또는 null 변수)는 서비스에서 떼기
			} else if sid := asString(v); sid != "" {
				eid := asString(p.Args["environmentId"])
				if eid == "" {
					return nil, gqlErr(http.StatusBadRequest, "environmentId is required to attach a volume to a service")
				}
				inst, err := getInstance(p, sid, eid)
				if err != nil {
					return nil, err
				}
				body["service_instance_id"] = inst["id"]
			}
			return restOK(p, http.MethodPatch, "/api/volumes/"+esc(p.Args["volumeId"]), body)
		}),
		"volumeDelete": resolved(nn(graphql.Boolean), args("volumeId", reqStr), func(p graphql.ResolveParams) (any, error) {
			return restOK(p, http.MethodDelete, "/api/volumes/"+esc(p.Args["volumeId"]), nil)
		}),
	}})
	addTCPProxyFields(query, mutation)
	addPrivateNetworkFields(query, mutation)
	return graphql.NewSchema(graphql.SchemaConfig{Query: query, Mutation: mutation})
}

// ownerProject는 REST 단건 조회가 없는 리소스(서비스, 환경)의 프로젝트 id를 저장소에서 찾는다.
// 값은 그 프로젝트의 REST 응답에서 꺼내므로 권한 검사는 REST가 한다.
func ownerProject(p graphql.ResolveParams, lookup func(*store.Store) (string, error)) (string, error) {
	exec, ok := p.Context.Value(graphQLContextKey{}).(*graphQLExecution)
	if !ok || exec.store == nil {
		return "", gqlErr(http.StatusServiceUnavailable, "store unavailable")
	}
	pid, err := lookup(exec.store)
	if err != nil {
		return "", gqlErr(http.StatusNotFound, "not found")
	}
	return pid, nil
}

// optionalPort는 targetPort가 주어졌을 때만 PATCH 본문에 넣는다.
func optionalPort(m map[string]any) map[string]any {
	body := map[string]any{}
	if v, ok := m["targetPort"]; ok {
		body["target_port"] = v
	}
	return body
}

// resolvedVariables는 참조를 푼 변수 값 맵이다.
func resolvedVariables(p graphql.ResolveParams, sid, eid any) (any, error) {
	v, err := rest(p, http.MethodGet, instancePath(sid, eid)+"/variables/resolved", nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, e := range asMap(v) {
		if m := asMap(e); m != nil {
			out[k] = m["value"]
		}
	}
	return out, nil
}

func addDomain(p graphql.ResolveParams, in map[string]any, host any) (any, error) {
	body := map[string]any{}
	if host != nil {
		body["host"] = host
	}
	if in["targetPort"] != nil {
		body["target_port"] = in["targetPort"]
	}
	sid, eid := asString(in["serviceId"]), asString(in["environmentId"])
	v, err := rest(p, http.MethodPost, instancePath(sid, eid)+"/domains", body)
	if err != nil {
		return nil, err
	}
	return withDomainOwner(v, sid, eid), nil
}

func findDomain(p graphql.ResolveParams, id string) (any, error) {
	sid, eid, host, err := parseDomainID(id)
	if err != nil {
		return nil, err
	}
	in, err := getInstance(p, sid, eid)
	if err != nil {
		return nil, err
	}
	for _, d := range asList(in["domains"]) {
		if strings.EqualFold(asString(asMap(d)["host"]), host) {
			return withDomainOwner(d, sid, eid), nil
		}
	}
	return nil, gqlErr(http.StatusNotFound, "domain not found")
}

func deleteDomain(p graphql.ResolveParams, id string) (any, error) {
	sid, eid, host, err := parseDomainID(id)
	if err != nil {
		return nil, err
	}
	return restOK(p, http.MethodDelete, instancePath(sid, eid)+"/domains/"+esc(host), nil)
}

// deploymentInstanceAction은 배포가 속한 인스턴스에 restart/stop을 한다.
func deploymentInstanceAction(p graphql.ResolveParams, action string) (any, error) {
	v, err := rest(p, http.MethodGet, "/api/deployments/"+esc(p.Args["id"]), nil)
	if err != nil {
		return nil, err
	}
	d := asMap(v)
	return restOK(p, http.MethodPost, instancePath(d["serviceId"], d["environmentId"])+"/"+action, map[string]any{})
}
