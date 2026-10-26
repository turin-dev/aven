# Permissions and workspaces

Instance operations and workspace membership are separate authorization boundaries.
The dashboard, REST API, GraphQL API, CLI and MCP use the same server checks.

## Roles

| Boundary | Source of authority | Permissions |
| --- | --- | --- |
| Instance | `users.role` | Owner/admin manage accounts, nodes, global security policies, OAuth apps, global notification channels, audit and instance usage. This role grants no workspace membership. Instance **owner** is additionally owner of every workspace and admin of every project (see *Instance owner*). |
| Workspace | `workspace_members.role` | Owner/admin administer the workspace and all its projects. Member can create projects. Member/viewer require direct or enabled group grants to access existing projects. |
| Project | `project_members.role`, workspace access groups | Admin manages the project and its members; member operates resources; viewer reads with secrets masked. Workspace membership is required even when a project grant exists. |
| Environment | Project access plus `restricted` | Restricted environments require the creator/user to have effective project admin permission; a token still has its own role ceiling. |

The `owner`, `admin`, `member`, `viewer` order is evaluated independently at each
boundary. An instance viewer can own a workspace, and an instance owner can be a
viewer or non-member in another workspace. `KEEL_SUPERADMINS` grants instance
owner authority.

### Instance owner

An instance **owner** (`users.role = owner` or `KEEL_SUPERADMINS`) is never
blocked: it is treated as **owner of every workspace** and **admin of every
project**, including restricted environments and secrets, without needing
membership rows. `GET /api/workspaces` lists every workspace for it (its own
first), and `/api/auth/me` reports `admin` for every project. Instance **admins**
do not get this; they keep instance operations only.

API tokens and OAuth grants created by an owner are still narrowed by their own
scope and role (an `instance` token still has no project access, a workspace
token only reaches its workspace). The legacy `KEEL_API_TOKEN` keeps its separate
unrestricted access.

**GitHub connection.** Deploys clone, post commit statuses and read PR files with the
project's GitHub connection (`projects.github_user_id`, default the creator), not the
caller's. An instance owner acting on a project it is not a member of therefore deploys
with someone else's GitHub credentials. This is allowed. `GET /api/projects/{pid}`
returns `github_borrowed: true` for that caller, and the canvas shows a warning naming
the connection's owner. Every such GitHub-source deployment requested through the API
is written to the audit log as `project.github_borrowed`. Webhook and scheduled deploys
are not attributed to anyone, so they are not logged. Once the owner is a real
workspace/project member, the connection counts as shared and no warning or audit is
produced.

In the dashboard, **어드민 → 작업 공간** lists each workspace's projects for
owners with **열기** and **삭제** (confirm by typing the project name).

Default (`ws_default`), personal and team workspaces all use explicit membership.
New accounts are not automatically added to the default workspace. A personal
workspace is created when a session first requests its workspace/account view.
Personal owners cannot be removed or demoted. Team/default workspaces must retain
an active owner, including when an instance administrator disables or deletes an
account. Only workspace owners can grant or change workspace owner roles.

Workspace/project administrators select existing people from the workspace
directory. The instance-wide user directory requires instance admin permission.
Workspace invitations create an instance **member** account and grant the requested
workspace role separately. Project invitations grant workspace viewer membership
plus the requested project role. An invitation's creator must still have the
required workspace/project authority when the link is accepted.

## Admin panel

Instance owners/admins, including `KEEL_SUPERADMINS`, see **어드민** in navigation.
`/admin/` manages account invitations, users, resource limits, nodes, global security, SAML, OAuth
apps, global notifications, audit, Aven Agent and system status independently of
the selected workspace or project. Account invitations do not require a team or
project and do not grant workspace membership. Workspace owners without instance
administration cannot open this panel or call its protected management APIs.

The default admin page is **전체 현황**: active/disabled account counts, valid
pending invitations, workspace/project/service counts, failed or crashed service
instances, and live server/agent connection state. It refreshes every 30 seconds
and supports manual refresh. Stopped instances are not counted as errors; node
connectivity is not a Docker/container health check.

**작업 공간** lists every workspace with member/project/service counts, name/ID
search and personal/team/default filters. Links open that workspace's resource
policy directly. Opening the workspace view requires membership, except for instance
owners, who can open and manage every workspace and project.
Server admins (instance owner/admin) also get **삭제** on each non-default
workspace. It deletes the workspace together with its projects (services,
containers, volumes, deployment history), members, invites, access groups and
workspace tokens. The admin must type the workspace name. Server admins need no
workspace membership for this, but deleting does not let them open the workspace
or read its contents. A deleted personal workspace is recreated empty the next
time its owner signs in. `DELETE /api/instance/workspaces/{wsid}` with
`{"confirm": "<name>"}` requires a browser session. API tokens and workspace
owners who lack the instance role get 403. The owner can withhold this
from admins with the `workspaces` capability. Each deleted project and the
workspace are written to the audit log as `project.deleted` / `workspace.deleted`.
**사용자·초대** supports name/email search and active/disabled/instance-role filters.
Searches and filters can be combined and reset without changing any account.

`GET /api/instance/overview` requires instance admin authority (also available to
an instance-scoped admin token). It returns `{users, pending_invites, workspaces,
nodes, generated_at}` with operating counts and workspace metadata. It does not
return project/service names, configuration, secrets or credentials. Workspace
tokens and workspace owners without instance authority cannot use this API.
This overview adds no database migration or workspace membership.

Project/workspace settings stay at `/settings/`; personal settings stay at
`/account/`. Old links to instance settings redirect to the admin panel.

## 서버 owner 전용 관리

Only an instance **owner** (`users.role = owner`, `KEEL_SUPERADMINS` or the built-in
`KEEL_ADMIN_PASSWORD` account) sees the **Owner ·** sections in **어드민**. Instance
admins, workspace owners/admins and every API token or OAuth grant (even one created by
an owner) get 403 from `/api/owner/*`. Calls require a browser session. Every change is
written to the audit log as `owner.*`.

| Section | What it does |
| --- | --- |
| **Owner · 점검·제어** | Maintenance mode, global announcement banner, aven-server info and restart, and emergency lockdown. |
| **Owner · 권한** | Owner list, promote/demote owners, ownership transfer, and the instance-admin capability scope. |
| **Owner · 세션·토큰** | Every user's sessions and API tokens: revoke one, all of a user's, or everything outside the owners. |
| **Owner · DB 백업** | Online copy of `keel.db` (`VACUUM INTO`) into `$KEEL_DATA/backups/`, plus list, download and delete. |

### Cross-boundary owner tools

These go past the normal workspace/project rules. All require an owner browser session
and are written to the audit log.

- **Assign another user's GitHub connection** (`PUT /api/projects/{pid}/github` with
  someone else's `user_id`). Other callers can only assign their own connection. The
  target must be active and have connected GitHub. Lookup: `GET /api/owner/github-connections`.
  Audit: `owner.github_assigned`.
- **Force a workspace owner** (`PUT /api/owner/workspaces/{wsid}/owner {user_id, previous}`
  with `previous` = `keep` | `demote` | `remove`). This bypasses the personal-owner and
  last-owner rules, for example when someone leaves. A personal workspace becomes a team
  workspace. Its former owner gets a fresh personal workspace on next sign-in. Shown in
  **어드민 → 작업 공간 → 소유권**. Audit: `owner.workspace_owner_changed`.
- **Suspend a project or workspace** (`POST /api/owner/holds {scope, target_id, reason}`,
  `DELETE /api/owner/holds/{scope}/{id}`, `GET /api/owner/holds`). Suspending stops every
  running instance. Until release it blocks new deploys (including GitHub webhooks),
  restarts, scaling and immediate cron runs for everyone, owners included. Members see
  the reason on the canvas (`owner_hold` in `GET /api/projects/{pid}`). Release restarts
  the stopped instances unless another hold still applies. Holds are removed with their
  target. Audit: `owner.hold_created` / `owner.hold_released`.
  Placing or releasing any hold (including the service admin tab's service stop and deploy
  block) emails the target workspace's owners and admins, one message each, when SMTP is
  configured. The response carries `emailed` (recipients reached); mail failures are only
  logged and never fail the hold. Audit: `hold.emailed`.
- **View as user** (`POST /api/owner/view-as {user_id, minutes}`, default 30, max 120). For
  the duration, the owner's session acts as that user to diagnose permissions. It is
  read-only: writes and WebSocket upgrades (shell, exec) get 403, and owner routes are
  unavailable. Other owners and disabled accounts cannot be viewed. The view ends with
  `POST /api/auth/view-as/stop` (banner button), on expiry, or if the viewer stops
  being an owner. `/api/auth/me` returns `view_as {by, until}`. Audit:
  `owner.view_as_started` / `owner.view_as_ended`.

**Admin capability scope.** The owner can withhold admin-panel features from instance
admins: `users`, `workspaces` (workspace deletion), `resources`, `nodes`, `security`, `sso`, `oauth_apps`, `notifications`,
`audit`, `agent`, `backups` and `usage`. Withheld features disappear from the admin
navigation (`/api/auth/me` → `admin_caps`), and their instance routes return 403 for
admins. The overview, workspace list and system status are always available. Owners are
never restricted. New capabilities default to allowed.

**Maintenance mode.** Every non-GET request from a principal that is not an owner returns
`503 {"error": <message>, "maintenance": true}`. Owners and `KEEL_API_TOKEN` are exempt,
as are marking notifications read and ending your own session. Reads, running services
and scheduled jobs are unaffected. Options:
- `block_logins` rejects new non-owner sessions (password, passkey, SAML) and invite
  acceptance. Invite acceptance is also refused whenever maintenance is on.
- `pause_webhooks` answers GitHub pushes, PRs and CI events with 202 `skipped` and does
  not deploy. GitHub does not resend them, so deploy manually afterwards.

**Lockdown** turns on maintenance with both options, then deletes every session and API
token belonging to non-owners. `KEEL_SUPERADMINS` accounts are treated as owners.

**Ownership transfer** (`POST /api/owner/transfer {user_id, stay_owner}`) promotes the
target. With `stay_owner: false`, it demotes the caller to instance admin in the same
transaction. The built-in account and `KEEL_SUPERADMINS` remain owners regardless.

**Restart** sends SIGTERM to aven-server only when it runs under systemd
(`INVOCATION_ID`). `Restart=always` starts it again, and `KillMode=process` keeps the
containers running.

The settings live in the `settings` table (`owner.admin_caps`, `owner.maintenance`,
`owner.announcement`, `owner.setup`). No migration is needed.

API (all `ownerOnly`, session only):
`GET /api/owner`, `PUT /api/owner/{admin-caps,maintenance,announcement}`,
`GET /api/owner/sessions`, `DELETE /api/owner/sessions/{id}`, `POST /api/owner/sessions/revoke {user_id | scope:"non_owners"}`,
`GET /api/owner/tokens`, `DELETE /api/owner/tokens/{id}`, `POST /api/owner/tokens/revoke {user_id | scope:"non_owners"}`,
`POST /api/owner/transfer`, `POST /api/owner/lockdown {message}`,
`GET /api/owner/setup` (first-run checklist: `items[{id, done, optional}]`, `required`, `required_done`, `complete`, `dismissed`), `PUT /api/owner/setup {dismissed}` (hide or show the first-login prompt for all owners),
`GET|POST /api/owner/backups`, `GET|DELETE /api/owner/backups/{name}`, `POST /api/owner/restart`.
`GET /api/instance/notice` (any signed-in user) returns the banner, the maintenance state
and whether the caller is exempt.

## Workspace resource limits

**어드민 → 자원 제한** sets CPU cores, memory MiB and volume allocation MiB per
workspace. Instance admins can see workspace names and aggregate allocated
capacity without gaining access to their projects or secrets (instance owners have full access anyway). Workspace members
can read their workspace's limits; changing them requires an instance admin session.

Limits default to unlimited. Each field accepts `null` for unlimited or `0` to
prevent allocations. A limit below current allocation, or a finite limit with
uncapped resources in that dimension, is rejected. Set individual service/volume
limits first, and redeploy running services to apply their new container limits.
Saving a workspace limit never stops or restarts existing services.

CPU and memory are the sum of each instance's maximum requested, queued or live
allocation, across all projects and environments, plus sandboxes. Replicas and
placement counts multiply per-container limits; enabled autoscaling reserves its
maximum replicas. Stopped instances retain their requested allocation. Reducing a
pending configuration does not release the allocation of its larger live deployment.
Detached volumes still count toward volume allocation.

This is a **steady-state allocation quota**. Temporary build/pre-deploy containers
and rolling-deployment overlap are not additional allocations, so it is not a hard
ceiling on simultaneous host usage. Docker still enforces individual service
container CPU/memory limits. Volume quotas sum configured `size_limit_mb` values;
the local Docker volume driver does **not** block writes at that size. Monthly
usage limits remain a separate feature.

For new instances, a capped dimension gets a default of 1 CPU core or 512 MiB,
bounded by the workspace's limit. New volumes default to 1024 MiB when volume
allocation is capped. Explicit individual values are never silently reduced.
Existing resources and newly created workspaces keep their existing/unlimited
policy until an administrator changes it.

Admission and allocation writes share a database transaction. Over-limit service
creation, environment copying, configuration edits, deployments/rollbacks,
scaling, sandbox/volume creation and project moves are rejected; REST callers get
HTTP 409. Runtime replica plans are checked again before containers start.

- `GET /api/instance/resource-limits`: all workspace policies and allocations;
  requires instance admin (an appropriately scoped read token is allowed).
- `PUT /api/instance/resource-limits/{wsid}`: replace the three limit fields;
  requires an instance admin session. Omitted fields become unlimited.
- `GET /api/workspaces/{wsid}/resource-limits`: one workspace's policy and
  allocation; requires membership and obeys token scope.

Both reads and a successful write return `{workspace, limits, allocated}` (the
instance list returns an array). `limits` contains nullable `cpu_cores`, `memory_mb`
and `volume_mb`; `allocated` contains those numeric totals, `unlimited_cpu`,
`unlimited_memory`, `unlimited_volume` counts and `instances`, `sandboxes`, `volumes`
counts. The UI displays memory/volume in GiB and converts to MiB for the API.

## API tokens

| Scope | Binding | Access |
| --- | --- | --- |
| `instance` | No workspace/project/environment | Instance operations allowed by the creator's current instance role and token role. No tenant project access. |
| `workspace` | One `workspace_id` | That workspace and projects the creator can access within it. No instance administration. |
| `project` | `workspace_id` + `project_id` | One project within its bound workspace. |
| `environment` | `workspace_id` + `project_id` + `environment_id` | One environment, with read-only access to project-level resources; role ceiling is member. |

Every request checks current user membership and limits it by the token's role.
Scoped tokens cannot manage credentials or membership. Instance tokens can read
the instance user directory, but account/membership mutations require a session.
OAuth `workspace:*` scopes are limited to the user's current memberships and never
grant instance administration; project scopes remain limited to consented projects.

The legacy server environment variable **`KEEL_API_TOKEN` remains an unrestricted
operator credential**, including access to all workspaces, for compatibility. It
is distinct from the new `scope: "instance"` API tokens.

## Resource boundaries and revocation

- Project names and access-group names are unique within a workspace. A group can
  contain only users and projects from that workspace. Group enforcement is also
  configured per workspace; direct project grants remain effective when it is off.
- Removing a workspace member deletes their direct project grants, group
  memberships, bound API tokens and pending workspace invitations they created in
  one transaction. Re-adding them does not restore those grants or credentials.
- Moving a project requires source project admin and target workspace member or
  higher. The move removes old direct grants, group associations, project/environment
  tokens and pending project invitations, clears the project's GitHub connection,
  and grants the mover project admin, in one transaction. A name conflict rolls all
  of that back. Runtime resources and stored application configuration move with
  the project; reconnect GitHub in the target workspace as needed.
- Templates store `workspace_id`. `private` is visible to the creator and that
  workspace's admins; `workspace` to that workspace's members; `public` to signed-in
  users. Editing requires workspace admin or the creator with at least member role.
- Workspace selection and capability checks use the same frontend resolver. An
  explicit invalid workspace/project URL displays an error instead of opening a
  different workspace. Management settings also work in an empty workspace.

## API contract

- `GET /api/auth/me` includes `instance_role`, `instance_admin`, `workspaces` with
  each local role, and effective `projects` roles. `role` remains an instance-role
  compatibility alias. `workspace_admin` refers only to `ws_default`; clients should
  use the selected workspace's `role`. `can_create_projects` indicates whether any
  accessible workspace permits creation, not permission for a particular target.
- `GET /api/workspaces` lists explicit memberships; `GET /api/workspaces/{wsid}`
  reads one. `/api/workspaces/{wsid}/members` manages local membership.
- `POST /api/workspaces/{wsid}/invites {email, role}` creates a workspace invitation.
- `GET|PATCH /api/workspaces/{wsid}/access-group-settings` reads/sets
  `{access_groups_enabled}`; mutations require a session and workspace admin.
- `GET|POST /api/workspaces/{wsid}/access-groups` and
  `PUT|DELETE /api/workspaces/{wsid}/access-groups/{gid}` manage local groups. Old
  `/api/access-groups` routes are aliases for the default workspace only.
- `/api/access-controls` manages instance guardrails. Its legacy
  `access_groups_enabled` field refers to the default workspace and updating that
  field additionally requires default-workspace admin permission.
- `GET|PATCH /api/instance/security` manages global MFA policy. The old
  `/api/workspace/security` route remains an alias with instance authorization.
- Creation accepts `workspace_id` in `POST /api/projects`, `POST /api/templates`
  and `POST /api/tokens`. Omitting it uses `ws_default` for session callers; project
  and template creation with a workspace token uses the token's own workspace.
  New accounts should pass an explicit workspace ID.
- GraphQL `projects(workspaceId: ...)` and
  `projectCreate(input: {workspaceId: ...})` use the same filtering and validation.

## Upgrading existing installations

Migration 42 (`migrateResourceLimits`) adds workspace policies and runtime replica
reservations. It starts every workspace without a quota and preserves existing
services, volumes and their individual resource settings.

Migration 41 (`migratePermissionBoundaries`) runs automatically when the updated
server opens its database. Existing users' instance roles are copied once into
default-workspace membership to preserve existing access. Subsequent instance-role
changes do not alter those memberships. Existing direct project members get any
missing workspace viewer membership so their project grants keep working.

Existing access groups, group enforcement and templates are assigned to
`ws_default`. Existing pending invitations gain explicit workspace roles.
Existing workspace tokens are bound to `ws_default`; project/environment tokens
are bound to their project's workspace. Foreign keys and existing group links are
preserved through the migration and checked in migration tests.

**Compatibility change:** an old workspace token no longer accesses other
workspaces or instance-operation endpoints. Create a separate instance token for
operational automation and explicit workspace/project tokens for tenant work:

```sh
aven tokens create --name operations --instance --role admin
aven tokens create --name team-ci --workspace ws_example --role member
aven init backend --workspace ws_example
aven templates publish stack.json --workspace ws_example --visibility workspace
```

The migration preserves project IDs, services, environments and stored data. Use
the normal database backup procedure before upgrading; this code change itself
does not deploy a server or open the production database.
