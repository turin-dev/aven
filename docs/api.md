# aven HTTP API (v0, M1)

모든 경로의 prefix는 `/api`이고 JSON을 쓴다. 인증은 `POST /api/auth/login`이 발급하는 `keel_session` 쿠키(HttpOnly)
또는 `Authorization: Bearer <token>` 헤더로 한다.
에러 응답 형식: `{"error": "message"}` (4xx/5xx)

## Auth
- `POST /api/auth/login` `{ "password": string }` → `204` + Set-Cookie
- `POST /api/auth/logout` → `204`
- `GET  /api/auth/me` → `{ "user": "admin" }` (미인증이면 `401`)

## Types
```ts
type App = {
  id: string;              // "app_xxxx"
  name: string;            // [a-z0-9-], 고유
  repo_url: string;        // git URL
  branch: string;          // 기본 "main"
  dockerfile: string;      // 기본 "Dockerfile" (repo 내 경로)
  build_context: string;   // 기본 "."
  port: number;            // 컨테이너 내부 포트
  healthcheck_path: string;// 기본 "/" (HTTP GET, 2xx/3xx면 healthy)
  domains: string[];       // 예: ["app.example.com"]
  env: Record<string, string>;
  status: "idle" | "deploying" | "running" | "failed" | "stopped";
  current_deployment_id: string | null;
  created_at: string;      // RFC3339 UTC
  updated_at: string;
};

type DeploymentStatus =
  "queued" | "cloning" | "building" | "starting" | "healthchecking" |
  "live" | "failed" | "cancelled" | "superseded" | "rolled_back";

type Deployment = {
  id: string;              // "dep_xxxx"
  app_id: string;
  status: DeploymentStatus;
  commit_sha: string | null;
  image: string | null;    // 예: "keel/myapp:dep_xxxx"
  error: string | null;
  trigger: "manual" | "rollback" | "webhook";
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
};
```

## Apps
- `GET    /api/apps` → `App[]`
- `POST   /api/apps` `Partial<App>` (name, repo_url, port 필수) → `201 App`
- `GET    /api/apps/{id}` → `App`
- `PATCH  /api/apps/{id}` `Partial<App>` → `App`
- `DELETE /api/apps/{id}` → `204` (컨테이너·라우트 제거)
- `POST   /api/apps/{id}/deploy` → `202 Deployment`
- `POST   /api/apps/{id}/stop` → `204`
- `POST   /api/apps/{id}/rollback` `{ "deployment_id": string }` → `202 Deployment`
  (이전 live 이미지를 재사용해 새 deployment 생성)
- `GET    /api/apps/{id}/deployments` → `Deployment[]` (최신순)
- `GET    /api/apps/{id}/logs` → **SSE** 런타임 컨테이너 로그
  - `event: log` `data: <한 줄 텍스트>`

## Deployments
- `GET  /api/deployments/{id}` → `Deployment`
- `POST /api/deployments/{id}/cancel` → `204`
- `GET  /api/deployments/{id}/logs` → **SSE** 빌드/배포 로그 (기존 로그 replay 후 실시간 전송)
  - `event: log`    `data: <한 줄 텍스트>`
  - `event: status` `data: <DeploymentStatus>`
  - `event: done`   `data: <최종 DeploymentStatus>` (이후 서버가 스트림 종료)

## System
- `GET /api/system` → `{ "version": string, "node": string, "proxy": "ok" | "error", "docker": "ok" | "error" }`
