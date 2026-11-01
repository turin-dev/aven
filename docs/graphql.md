# aven Public GraphQL API

`POST /graphql/v2` is a GraphQL endpoint shaped like Railway's Public API (`https://backboard.railway.com/graphql/v2`, see <https://docs.railway.com/integrations/api>). Most of the Railway cookbook queries for projects, services, deployments, variables, environments, domains and volumes run unchanged. Only the URL and the token are different.

- **Auth.** Same as REST v1: the `aven_session` cookie (or the legacy `keel_session`) (requests that change something must be same-origin, as in REST) or `Authorization: Bearer <aven_… API token | AVEN_API_TOKEN>`. Without credentials the endpoint returns HTTP `401`. Railway's `Project-Access-Token` / `Team-Access-Token` headers are not supported. Use a project- or environment-scoped aven API token instead.
- **Implementation.** Each resolver calls the REST v1 route in-process with the caller's credentials. Permissions (`authz`), secret masking (viewers see variable values as `•••`, sealed variables read `<sealed>`), validation, audit events and rate limits are therefore exactly the same as REST.
- **Request.** `{"query": string, "operationName"?: string, "variables"?: object}`. The body is at most 1 MiB (`413` above that) and the query string at most 128 KiB.
- **Limits.** Before validation the query is measured with fragments expanded (each fragment is counted once, so nested fragment "bombs" are cheap to reject; introspection fields such as `__schema` and `__typename` are free): at most **15 levels** of nested selections and **1000 fields**. A request may also make at most 500 internal REST calls; past that the remaining fields fail with `RATE_LIMITED`. The standard GraphiQL introspection query fits these limits.
- **Errors.** As in Railway, always read the `errors` array. Each error has `message` and `extensions.code`:

  | HTTP | `extensions.code` | When |
  | --- | --- | --- |
  | 400 | `GRAPHQL_PARSE_FAILED` | The query is not valid GraphQL syntax |
  | 400 | `GRAPHQL_VALIDATION_FAILED` | Unknown field, type or argument, missing required argument, or a depth, field-count or query-size limit |
  | 400 | `BAD_USER_INPUT` | Malformed JSON body, empty query, a variable value of the wrong type, a missing required variable, unknown `operationName` |
  | 401 | | No credentials (plain REST error body) |
  | 200 | `BAD_USER_INPUT`, `UNAUTHENTICATED`, `FORBIDDEN`, `NOT_FOUND`, `CONFLICT`, `RATE_LIMITED`, `INTERNAL_SERVER_ERROR` | A resolver failed. The message is the REST message and `extensions.status` the REST status. Other fields of the same request still resolve |

  400 responses carry `"data": null`. Unlike Railway, an authorization denial is `FORBIDDEN` (or `NOT_FOUND` for resources outside your access, as in REST) rather than `INTERNAL_SERVER_ERROR` with `Not Authorized`. Railway's `traceId` is not sent.
- **Introspection** is enabled. `GET /graphql/v2` serves a small playground with example queries, a schema browser and an optional token field, using the session cookie by default. The token is not stored.
- **IDs** are aven IDs (`prj_…`, `svc_…`, `env_…`, `dep_…`, `vol_…`; `tcp_…` and `pne_…` wrap a service instance id). ID arguments are typed `String`, so `$id: String!` works. Domains have no id in aven. The API returns an opaque `dom_…` id that encodes service, environment and host, which you then pass to the update and delete mutations.
- **Lists** are Relay connections (`edges { node }`, `pageInfo { hasNextPage hasPreviousPage endCursor }`, `totalCount`) with `first` (default 100, max 500) and `after`. They are paginated in memory over the REST list.
- **Field names** are the REST JSON keys in camelCase (`created_at` → `createdAt`), plus Railway aliases where the names differ (`rootDirectory`, `numReplicas`, `restartPolicyType`, `sleepApplication`, `watchPatterns`, `source { image repo }`, …).

## Queries

| Field | Notes |
| --- | --- |
| `me: User!` | `id email name role` (`avatar` is always null) |
| `projects(workspaceId, first, after): ProjectConnection!` | Projects visible to the caller, optionally in one workspace. `Project.updatedAt` is accepted and always null |
| `project(id): Project` | `environments`, `services`, `volumes` connections |
| `service(id): Service` | `project`, `serviceInstances` connection, `serviceInstance(environmentId)` |
| `environments(projectId, isEphemeral)` / `environment(id)` | `isEphemeral` = PR preview environment. `Environment.serviceInstances`, `Environment.volumes` |
| `serviceInstance(serviceId, environmentId): ServiceInstance` | `serviceName`, settings, `domains { serviceDomains customDomains }`, `latestDeployment`, `volumes`, `pendingChanges`, `privateDomain`, `ipv6EgressEnabled: Boolean!` (outbound IPv6; `true` unless turned off). `region` is always null (aven places services with the `placement` setting). `buildCommand` comes from the service's config file and is read-only |
| `deployments(input: {serviceId, environmentId?, projectId?, status: {successfulOnly, in, notIn}}, first, after)` | Newest first, last 500. Without `environmentId` the project's default environment is used. `in`/`notIn` take Railway status names (`["FAILED", "CRASHED"]`). `includeDeleted` is accepted and ignored |
| `deployment(id)` | `status` uses Railway names (`SUCCESS` = aven `live`; `pre_deploy`/`deploying`/`healthchecking` → `DEPLOYING`; `cancelled` → `REMOVED`; others upper-cased); `keelStatus` is the raw status. `staticUrl` is the service's first public domain (generated domains first, no scheme) and `url` the same with `https://`; aven has no per-deployment URLs. `canRedeploy` is true when the deployment has an image, `canRollback` when it also went live (what REST `redeploy`/`rollback` accept). Also `projectId`, `service`, `environment` and `updatedAt` (finish, start or creation time). `meta` is a JSON object `{commitHash, commitMessage, commitAuthor, branch, image, trigger}` |
| `buildLogs(deploymentId, limit, filter, startDate, endDate): [Log!]!` / `deploymentLogs(…)` | A snapshot of the newest matching lines (no subscription). `Log { timestamp message severity attributes { key value } }`; `severity` is `error` for stderr and lines with error/fatal/panic, otherwise `info`. Runtime log `attributes` are `replica` and `stream`. `limit` 1–5000 (default 500). `filter` uses the dashboard search syntax (all words must match, `-word` excludes, `"quoted phrase"`); `startDate`/`endDate` are RFC 3339. With a filter or a date range the newest 10 000 lines are searched. Build log timestamps have second precision. Access is checked with `GET /api/deployments/{id}` (viewer) |
| `variables(projectId, environmentId, serviceId?, unrendered?)` | JSON object. Resolved values by default; `unrendered: true` keeps `${{ … }}` references. Without `serviceId` it returns the environment's shared variables |
| `variablesForServiceDeployment(projectId, environmentId, serviceId)` | Resolved variables, including the ones aven injects |
| `domains(projectId, environmentId, serviceId): AllDomains!` | `serviceDomains` (generated) and `customDomains` with `status { verified verificationToken verificationDnsHost dnsRecords { recordType hostlabel fqdn requiredValue currentValue status } cdnProvider certificateStatus keelStatus }`. `verificationToken` is the value of the ownership `TXT` record and `verificationDnsHost` its name. `verified` is true for aven `active` and `proxied` (behind Cloudflare or a tunnel). Record `status` is `VALID` (aven `ok`/`proxied`), `PENDING` or `INVALID` (`mismatch`); `currentValue` is the values seen in the last check, comma-separated, or null. `cdnProvider` is `CLOUDFLARE` or null, `certificateStatus` `VALID`/`INVALID`/null, `keelStatus` the raw REST status |
| `tcpProxies(serviceId, environmentId): [TCPProxy!]!` | `id domain proxyPort applicationPort serviceId environmentId`. At most one per service instance; `id` is `tcp_<instanceId>` |
| `privateNetworkEndpoint(serviceId, environmentId, privateNetworkId?): PrivateNetworkEndpoint` | `publicId dnsName privateIps serviceInstanceId tags` plus aven's `fqdn aliases serviceId environmentId`. `dnsName` is the name without suffix (the custom private name or the service name, configured value), `fqdn` is `<dnsName>.<env>.aven.internal`, `privateIps` are the first replica's IPv6 and IPv4 addresses on the environment network (empty when nothing runs), `tags` is `["ipv4", "ipv6"]`. `publicId` is `pne_<instanceId>`. Reads REST `GET …/networking` |
| `privateNetworkEndpointNameAvailable(environmentId, prefix, privateNetworkId?, serviceId?): Boolean!` | Whether `prefix` can be used as a private name in the environment. With `serviceId` (aven extension) the service's own names count as free (`GET …/instances/{eid}/private-dns/available`); without it every service name in the environment is taken (`GET /environments/{eid}/private-dns/available`) |
| `customDomain(id, projectId)` | One domain by its `dom_…` id |

## Mutations

| Mutation | REST |
| --- | --- |
| `projectCreate(input: {name, description, workspaceId, prDeploys, defaultEnvironmentName, repo: {fullRepoName, branch}})`, `projectUpdate(id, input: {name, description, defaultEnvironmentId, prDeploys})`, `projectDelete(id)` | `/api/projects…`. `prDeploys` turns PR preview environments on or off, `defaultEnvironmentName` renames the first environment and `repo` adds a repo service. `isPublic` is accepted and ignored |
| `environmentCreate(input: {projectId, name, sourceEnvironmentId})`, `environmentRename(id, input: {name})`, `environmentDelete(id)` | `sourceEnvironmentId` → `copy_from`. `ephemeral`, `skipInitialDeploys` and `stageInitialChanges` are accepted and ignored |
| `serviceCreate(input: {projectId, name, source: {repo \| image \| databaseKind}, branch, environmentId, icon, variables})` | `repo` may be `owner/repo` (→ `https://github.com/owner/repo`). Without a source an image service is created. `variables` are set on the instance in `environmentId` (default environment if not set) |
| `serviceUpdate(id, input: {name, icon})`, `serviceDelete(id)` | aven deletes a service in every environment, so `serviceDelete(id, environmentId)` fails with `BAD_USER_INPUT` instead of widening the deletion |
| `serviceInstanceUpdate(serviceId, environmentId, input): Boolean` | `startCommand preDeployCommand rootDirectory healthcheckPath healthcheckTimeout numReplicas restartPolicyType(ON_FAILURE\|ALWAYS\|NEVER) restartPolicyMaxRetries cronSchedule sleepApplication dockerfilePath watchPatterns branch autoDeploy port source {image repo} ipv6EgressEnabled`. `region` is accepted and ignored; `buildCommand` fails with `BAD_USER_INPUT` (set it in the config file). Changes are staged (pending changes) like `PATCH …/instances/{eid}`, except `ipv6EgressEnabled` (→ `outbound_ipv6`), which applies immediately; `false` fails with `CONFLICT` when a node of the service cannot block IPv6 egress |
| `serviceInstanceDeploy(serviceId, environmentId): Boolean` / `serviceInstanceDeployV2(…): String` (deployment id) / `environmentTriggersDeploy(input: {environmentId, projectId?, serviceId}): Boolean` | Deploy the current settings (`POST …/deploy`). aven builds the latest commit of the branch, so `serviceInstanceDeployV2(commitSha)` fails with `BAD_USER_INPUT` |
| `serviceInstanceRedeploy(serviceId, environmentId): Boolean` | Redeploy the latest deployment's image |
| `deploymentRedeploy(id, usePreviousImageTag?)`, `deploymentRollback(id): Deployment`, `deploymentCancel(id)`, `deploymentRestart(id)`, `deploymentStop(id): Boolean` | Redeploy always reuses the deployment's image. Rollback needs `canRollback: true`. Restart/stop act on the deployment's service instance |
| `variableUpsert(input: {environmentId, serviceId?, name, value, skipDeploys})`, `variableCollectionUpsert(input: {environmentId, serviceId?, variables, replace, skipDeploys, sealed})`, `variableDelete(input: {environmentId, serviceId?, name})` | Without `serviceId` they change shared variables. `replace: true` keeps only the given keys. `sealed` (aven extension) lists names to seal. Unless `skipDeploys: true`, a service variable change redeploys the instance when it already has a deployment (never-deployed services are not built) |
| `serviceDomainCreate(input: {serviceId, environmentId, targetPort})`, `serviceDomainUpdate(input: {serviceDomainId, domain?, targetPort})`, `serviceDomainDelete(id)` | Generated domain. A `domain` different from the one in `serviceDomainId` renames it (the suffix must stay the same) |
| `customDomainCreate(input: {serviceId, environmentId, domain, targetPort})`, `customDomainUpdate(id, environmentId, targetPort)`, `customDomainDelete(id)` | DNS records to set are in `status.dnsRecords` |
| `tcpProxyCreate(input: {serviceId, environmentId, applicationPort}): TCPProxy!`, `tcpProxyDelete(id): Boolean!` | `POST`/`DELETE …/tcp-proxy`. Creating on a Cloudflare tunnel node fails with `CONFLICT` (no public address); creating again changes the application port |
| `privateNetworkEndpointRename(id?, dnsName, environmentId, privateNetworkId?, serviceId?): Boolean!` | Sets the private name (`PATCH …/instances/{eid}` `{private_dns_name}`). Pick the endpoint with `id` (`pne_<instanceId>`, Railway) or `serviceId` (aven). Staged like the REST field: it shows in `pendingChanges` and resolves after the next deploy. `dnsName` equal to the service name (or empty) resets it. Name errors are `BAD_USER_INPUT` |
| `volumeCreate(input: {projectId, serviceId, mountPath, environmentId?, name?, sizeLimitMb?})`, `volumeUpdate(volumeId, input: {name, sizeLimitMb})`, `volumeInstanceUpdate(volumeId, input: {mountPath, serviceId}, environmentId)`, `volumeDelete(volumeId)` | `region` is accepted and ignored. aven volumes belong to one environment, so a volume is also its own "volume instance". `serviceId: ""` detaches it. Delete needs a detached volume, as in REST |

aven has one private network per environment, so `privateNetworkId` arguments are accepted and ignored (there is no `privateNetworks` query).

Not available: `httpLogs` and `environmentLogs` (use the SSE REST endpoints or `aven logs`), `deploymentRemove`, subscriptions, `customDomainAvailable`, templates, volume backups, project members/transfer, staged-changes commits (`environmentPatchCommitStaged`), regions and resource limits.

## Example

```sh
curl -s https://aven.example.com/graphql/v2 \
  -H "Authorization: Bearer $AVEN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"query($id: String!) { project(id: $id) { name services { edges { node { id name } } } } }","variables":{"id":"prj_123"}}'

aven api -F id=prj_123 'query($id: String!) { project(id: $id) { name } }'
```
