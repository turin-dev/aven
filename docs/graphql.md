# aven Public GraphQL API

`POST /graphql/v2` is a GraphQL endpoint shaped like Railway's Public API (`https://backboard.railway.com/graphql/v2`, see <https://docs.railway.com/integrations/api>). Most of the Railway cookbook queries for projects, services, deployments, variables, environments, domains and volumes run unchanged. Only the URL and the token are different.

- **Auth.** Same as REST v1: the `keel_session` cookie (requests that change something must be same-origin, as in REST) or `Authorization: Bearer <keel_… API token | AVEN_API_TOKEN>`. Without credentials the endpoint returns HTTP `401`. Railway's `Project-Access-Token` / `Team-Access-Token` headers are not supported. Use a project- or environment-scoped aven API token instead.
- **Implementation.** Each resolver calls the REST v1 route in-process with the caller's credentials. Permissions (`authz`), secret masking (viewers see variable values as `•••`, sealed variables read `<sealed>`), validation, audit events and rate limits are therefore exactly the same as REST. A GraphQL request may make at most 500 internal REST calls. Past that, the remaining fields fail with `RATE_LIMITED`.
- **Request.** `{"query": string, "operationName"?: string, "variables"?: object}`, at most 1 MiB. The response is always HTTP 200 with `{data, errors}` unless the request itself is malformed (`400`) or unauthenticated (`401`).
- **Errors.** A REST error becomes a GraphQL error with the REST message and `extensions: {code, status}`. `code` is one of `BAD_USER_INPUT` (400), `UNAUTHENTICATED`, `FORBIDDEN`, `NOT_FOUND`, `CONFLICT`, `RATE_LIMITED`, `INTERNAL_SERVER_ERROR`.
- **Introspection** is enabled. `GET /graphql/v2` serves a small playground with example queries, a schema browser and an optional token field, using the session cookie by default. The token is not stored.
- **IDs** are aven IDs (`prj_…`, `svc_…`, `env_…`, `dep_…`, `vol_…`; `tcp_…` and `pne_…` wrap a service instance id). ID arguments are typed `String`, so `$id: String!` works. Domains have no id in aven. The API returns an opaque `dom_…` id that encodes service, environment and host, which you then pass to the update and delete mutations.
- **Lists** are Relay connections (`edges { node }`, `pageInfo { hasNextPage hasPreviousPage endCursor }`, `totalCount`) with `first` (default 100, max 500) and `after`. They are paginated in memory over the REST list.
- **Field names** are the REST JSON keys in camelCase (`created_at` → `createdAt`), plus Railway aliases where the names differ (`rootDirectory`, `numReplicas`, `restartPolicyType`, `sleepApplication`, `watchPatterns`, `source { image repo }`, …).

## Queries

| Field | Notes |
| --- | --- |
| `me: User!` | `id email name role` |
| `projects(workspaceId, first, after): ProjectConnection!` | Projects visible to the caller. `workspaceId` is accepted and ignored because there is one workspace |
| `project(id): Project` | `environments`, `services`, `volumes` connections |
| `service(id): Service` | `project`, `serviceInstances` connection, `serviceInstance(environmentId)` |
| `environments(projectId, isEphemeral)` / `environment(id)` | `isEphemeral` = PR preview environment. `Environment.serviceInstances`, `Environment.volumes` |
| `serviceInstance(serviceId, environmentId): ServiceInstance` | Settings, `domains { serviceDomains customDomains }`, `latestDeployment`, `volumes`, `pendingChanges`, `privateDomain`, `ipv6EgressEnabled: Boolean!` (outbound IPv6; `true` unless turned off) |
| `deployments(input: {serviceId, environmentId?, projectId?, status: {successfulOnly}}, first, after)` | Newest first, last 500. Without `environmentId` the project's default environment is used |
| `deployment(id)` | `status` uses Railway names (`SUCCESS` = aven `live`; `pre_deploy`/`deploying`/`healthchecking` → `DEPLOYING`; others upper-cased); `keelStatus` is the raw status. `meta` is a JSON object `{commitHash, commitMessage, commitAuthor, branch, image, trigger}` |
| `variables(projectId, environmentId, serviceId?, unrendered?)` | JSON object. Resolved values by default; `unrendered: true` keeps `${{ … }}` references. Without `serviceId` it returns the environment's shared variables |
| `variablesForServiceDeployment(projectId, environmentId, serviceId)` | Resolved variables, including the ones aven injects |
| `domains(projectId, environmentId, serviceId): AllDomains!` | `serviceDomains` (generated) and `customDomains` with `status { verified dnsRecords { recordType hostlabel fqdn requiredValue currentValue status } cdnProvider certificateStatus keelStatus }`. `verified` is true for aven `active` and `proxied` (behind Cloudflare or a tunnel). Record `status` is `VALID` (aven `ok`/`proxied`), `PENDING` or `INVALID` (`mismatch`); `currentValue` is the values seen in the last check, comma-separated, or null. `cdnProvider` is `CLOUDFLARE` or null, `certificateStatus` `VALID`/`INVALID`/null, `keelStatus` the raw REST status |
| `tcpProxies(serviceId, environmentId): [TCPProxy!]!` | `id domain proxyPort applicationPort serviceId environmentId`. At most one per service instance; `id` is `tcp_<instanceId>` |
| `privateNetworkEndpoint(serviceId, environmentId, privateNetworkId?): PrivateNetworkEndpoint` | `publicId dnsName privateIps serviceInstanceId tags` plus aven's `fqdn aliases serviceId environmentId`. `dnsName` is the name without suffix (the custom private name or the service name, configured value), `fqdn` is `<dnsName>.<env>.aven.internal`, `privateIps` are the first replica's IPv6 and IPv4 addresses on the environment network (empty when nothing runs), `tags` is `["ipv4", "ipv6"]`. `publicId` is `pne_<instanceId>`. Reads REST `GET …/networking` |
| `privateNetworkEndpointNameAvailable(environmentId, prefix, privateNetworkId?, serviceId?): Boolean!` | Whether `prefix` can be used as a private name in the environment. With `serviceId` (aven extension) the service's own names count as free (`GET …/instances/{eid}/private-dns/available`); without it every service name in the environment is taken (`GET /environments/{eid}/private-dns/available`) |
| `customDomain(id, projectId)` | One domain by its `dom_…` id |

## Mutations

| Mutation | REST |
| --- | --- |
| `projectCreate(input: {name, description})`, `projectUpdate(id, input: {name, description, defaultEnvironmentId})`, `projectDelete(id)` | `/api/projects…` |
| `environmentCreate(input: {projectId, name, sourceEnvironmentId})`, `environmentRename(id, input: {name})`, `environmentDelete(id)` | `sourceEnvironmentId` → `copy_from` |
| `serviceCreate(input: {projectId, name, source: {repo \| image \| databaseKind}, branch, environmentId, icon, variables})` | `repo` may be `owner/repo` (→ `https://github.com/owner/repo`). Without a source an image service is created. `variables` are set on the instance in `environmentId` (default environment if not set) |
| `serviceUpdate(id, input: {name, icon})`, `serviceDelete(id)` | |
| `serviceInstanceUpdate(serviceId, environmentId, input): Boolean` | `startCommand preDeployCommand rootDirectory healthcheckPath healthcheckTimeout numReplicas restartPolicyType(ON_FAILURE\|ALWAYS\|NEVER) restartPolicyMaxRetries cronSchedule sleepApplication dockerfilePath watchPatterns branch autoDeploy port source {image repo} ipv6EgressEnabled`. Changes are staged (pending changes) like `PATCH …/instances/{eid}`, except `ipv6EgressEnabled` (→ `outbound_ipv6`), which applies immediately; `false` fails with `CONFLICT` when a node of the service cannot block IPv6 egress |
| `serviceInstanceDeploy(serviceId, environmentId): Boolean` / `serviceInstanceDeployV2(…): String` (deployment id) | Deploy the current settings (`POST …/deploy`) |
| `serviceInstanceRedeploy(serviceId, environmentId): Boolean` | Redeploy the latest deployment's image |
| `deploymentRedeploy(id)`, `deploymentRollback(id): Deployment`, `deploymentCancel(id)`, `deploymentRestart(id)`, `deploymentStop(id): Boolean` | Restart/stop act on the deployment's service instance |
| `variableUpsert(input: {environmentId, serviceId?, name, value, skipDeploys})`, `variableCollectionUpsert(input: {environmentId, serviceId?, variables, replace, skipDeploys, sealed})`, `variableDelete(input: {environmentId, serviceId?, name})` | Without `serviceId` they change shared variables. `replace: true` keeps only the given keys. `sealed` (aven extension) lists names to seal. Unless `skipDeploys: true`, a service variable change redeploys the instance when it already has a deployment (never-deployed services are not built) |
| `serviceDomainCreate(input: {serviceId, environmentId, targetPort})`, `serviceDomainUpdate(input: {serviceDomainId, domain?, targetPort})`, `serviceDomainDelete(id)` | Generated domain. A `domain` different from the one in `serviceDomainId` renames it (the suffix must stay the same) |
| `customDomainCreate(input: {serviceId, environmentId, domain, targetPort})`, `customDomainUpdate(id, environmentId, targetPort)`, `customDomainDelete(id)` | DNS records to set are in `status.dnsRecords` |
| `tcpProxyCreate(input: {serviceId, environmentId, applicationPort}): TCPProxy!`, `tcpProxyDelete(id): Boolean!` | `POST`/`DELETE …/tcp-proxy`. Creating on a Cloudflare tunnel node fails with `CONFLICT` (no public address); creating again changes the application port |
| `privateNetworkEndpointRename(id?, dnsName, environmentId, privateNetworkId?, serviceId?): Boolean!` | Sets the private name (`PATCH …/instances/{eid}` `{private_dns_name}`). Pick the endpoint with `id` (`pne_<instanceId>`, Railway) or `serviceId` (aven). Staged like the REST field: it shows in `pendingChanges` and resolves after the next deploy. `dnsName` equal to the service name (or empty) resets it. Name errors are `BAD_USER_INPUT` |
| `volumeCreate(input: {projectId, serviceId, mountPath, environmentId?, name?, sizeLimitMb?})`, `volumeUpdate(volumeId, input: {name, sizeLimitMb})`, `volumeInstanceUpdate(volumeId, input: {mountPath, serviceId}, environmentId)`, `volumeDelete(volumeId)` | aven volumes belong to one environment, so a volume is also its own "volume instance". `serviceId: ""` detaches it. Delete needs a detached volume, as in REST |

`projectCreate`/`projectUpdate` ignore Railway-only inputs (`isPublic`, `prDeploys`, `repo`, `workspaceId`). aven has one private network per environment, so `privateNetworkId` arguments are accepted and ignored (there is no `privateNetworks` query).

Not available: logs (`buildLogs`, `deploymentLogs`, `httpLogs`, `environmentLogs`; use the SSE REST endpoints or `aven logs`), subscriptions, `customDomainAvailable`, templates, volume backups, project members/transfer, staged-changes commits (`environmentPatchCommitStaged`), regions and resource limits.

## Example

```sh
curl -s https://aven.example.com/graphql/v2 \
  -H "Authorization: Bearer $AVEN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"query($id: String!) { project(id: $id) { name services { edges { node { id name } } } } }","variables":{"id":"prj_123"}}'

aven api -F id=prj_123 'query($id: String!) { project(id: $id) { name } }'
```
