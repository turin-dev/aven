package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DefaultProjectName은 첫 실행 때 만드는 프로젝트 이름이다.
const DefaultProjectName = "default"

// migrations는 순서대로 한 번씩 실행된다. 이미 배포된 항목은 고치지 말고 새 항목을 추가한다.
var migrations = []func(ctx context.Context, tx *Store) error{
	migrateV1,
	migrateV2,
	migrateV3,
	migrateV4,
	migrateV5,
	migrateV6,
	migrateV7,
	migrateV8,
	migrateV9,
	migrateV10,
	migrateDBBackups,
	migratePreviewEnvs,
	migrateVolumeLimits,
	migrateUsers,
	migrateObservability,
	migrateTemplates,
	migrateMultiNodeReplicas,
	migrateFunctions,
	migrateSealedVariables,
	migrateUsageLimits,
	migrateAutoscaleState,
	migrateCanvasGroupsDashboards,
	migrateSkippedBuilds,
	migrateFocusedPreviewEnvs,
	migrateSAMLSSO,
	migrateImageUpdates,
	migrateFeatureFlags,
	migrateMFA,
	migrateIAC,
	migrateAgent,
	migrateOAuthProvider,
	migratePostgresOperations,
	migrateDBPITR,
	migrateNetworkLogs,
	migrateTracing,
	migrateEdgeSettings,
	migrateAccessControls,
	migrateSandboxes,
	migrateDatabaseHA,
	migrateWorkspaces,
	migratePermissionBoundaries,
	migrateResourceLimits,
	migrateDomainStates,
	migrateNodeIngress,
	migrateDomainIPv6,
	migrateOwnerTools,
	migrateServiceHolds,
}

// noForeignKeyMigrations는 외래 키 검사를 끄고 돌리는 마이그레이션이다(테이블을 다시 만드는 경우). PRAGMA foreign_keys는
// 트랜잭션 안에서 바뀌지 않으므로 트랜잭션 밖에서 끄고, 끝나면 foreign_key_check로 깨진 참조가 없는지 확인한 뒤 다시 켠다.
var noForeignKeyMigrations = map[int]bool{39: true, 40: true} // workspaces, permission boundaries

// migrateSandboxes는 환경에 묶인 일회용 샌드박스 컨테이너와 그 파일시스템 checkpoint(노드 로컬 이미지)를 기록한다.
func migrateSandboxes(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		// expires_at = last_activity_at + idle_timeout_minutes (NULL이면 유휴 삭제 안 함).
		`CREATE TABLE sandboxes (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			node_id TEXT NOT NULL,
			container_name TEXT NOT NULL UNIQUE,
			network_name TEXT NOT NULL,
			network_mode TEXT NOT NULL DEFAULT 'isolated',
			image TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'creating',
			memory_mb INTEGER NOT NULL,
			cpu_cores REAL NOT NULL,
			idle_timeout_minutes INTEGER NOT NULL,
			expires_at TEXT,
			last_activity_at TEXT NOT NULL,
			parent_id TEXT,
			checkpoint_name TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX sandboxes_environment ON sandboxes(environment_id, created_at DESC)`,
		`CREATE INDEX sandboxes_expiry ON sandboxes(expires_at) WHERE expires_at IS NOT NULL`,
		`CREATE TABLE sandbox_checkpoints (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			node_id TEXT NOT NULL,
			name TEXT NOT NULL,
			image TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			UNIQUE(environment_id, name)
		)`,
		`CREATE INDEX sandbox_checkpoints_environment ON sandbox_checkpoints(environment_id, created_at DESC)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migratePostgresOperations는 PostgreSQL 인스턴스별 PgBouncer 연결 풀링과 메이저 버전 업그레이드 상태 표를 만든다.
func migratePostgresOperations(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE postgres_states (
		instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
		pooler_service_id TEXT NOT NULL DEFAULT '',
		pooling_status TEXT NOT NULL DEFAULT '',
		pooling_error TEXT NOT NULL DEFAULT '',
		pool_mode TEXT NOT NULL DEFAULT 'transaction',
		default_pool_size INTEGER NOT NULL DEFAULT 20,
		max_client_conn INTEGER NOT NULL DEFAULT 1000,
		max_prepared_statements INTEGER NOT NULL DEFAULT -1,
		pooler_deployment_id TEXT NOT NULL DEFAULT '',
		upgrade_status TEXT NOT NULL DEFAULT '',
		upgrade_error TEXT NOT NULL DEFAULT '',
		upgrade_from_major INTEGER NOT NULL DEFAULT 0,
		upgrade_to_major INTEGER NOT NULL DEFAULT 0,
		upgrade_from_image TEXT NOT NULL DEFAULT '',
		upgrade_from_pgdata TEXT NOT NULL DEFAULT '',
		upgrade_from_mount_path TEXT NOT NULL DEFAULT '',
		upgrade_backup_id TEXT NOT NULL DEFAULT '',
		upgrade_post_backup_id TEXT NOT NULL DEFAULT '',
		upgrade_deployment_id TEXT NOT NULL DEFAULT '',
		upgrade_started_at TEXT,
		upgrade_finished_at TEXT,
		updated_at TEXT NOT NULL
	)`)
	return err
}

func migrateAgent(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE agent_threads (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		project_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		messages TEXT NOT NULL DEFAULT '[]',
		pending_call_id TEXT NOT NULL DEFAULT '',
		pending_action TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		return err
	}
	_, err = tx.q.ExecContext(ctx, `CREATE INDEX agent_threads_user_updated ON agent_threads(user_id, updated_at DESC)`)
	return err
}

// migrateFeatureFlags adds project-owned flag definitions and per-environment values.
func migrateFeatureFlags(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE feature_flags (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(project_id, name)
		)`,
		`CREATE TABLE feature_flag_values (
			flag_id TEXT NOT NULL REFERENCES feature_flags(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			default_value TEXT NOT NULL,
			rules TEXT NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			PRIMARY KEY(flag_id, environment_id)
		)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func migrateSAMLSSO(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE saml_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			metadata_xml TEXT NOT NULL DEFAULT '',
			metadata_url TEXT NOT NULL DEFAULT '',
			verified_domains TEXT NOT NULL DEFAULT '[]',
			default_role TEXT NOT NULL DEFAULT 'member',
			enforce_sso INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE saml_login_requests (
			relay_hash TEXT PRIMARY KEY,
			request_id TEXT NOT NULL,
			redirect_to TEXT NOT NULL DEFAULT '/',
			expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX saml_login_requests_expires ON saml_login_requests(expires_at)`,
		`CREATE TABLE saml_assertion_replays (
			assertion_id TEXT PRIMARY KEY,
			expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX saml_assertion_replays_expires ON saml_assertion_replays(expires_at)`,
		`ALTER TABLE sessions ADD COLUMN saml_authenticated INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateSkippedBuilds records build fingerprints and their image reuse source.
func migrateSkippedBuilds(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`ALTER TABLE deployments ADD COLUMN build_cache_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE deployments ADD COLUMN build_reused_from TEXT`,
		`ALTER TABLE deployments ADD COLUMN force_build INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX deployments_build_cache ON deployments(service_id, build_cache_key, created_at DESC) WHERE build_cache_key <> ''`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateFocusedPreviewEnvs stores the base environment for focused PR previews.
func migrateFocusedPreviewEnvs(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `ALTER TABLE environments ADD COLUMN base_environment_id TEXT`)
	return err
}

// migrateMultiNodeReplicas는 여러 노드에 나눠 띄운 배포의 다른 노드 컨테이너(placement)를 기록할 열을 추가한다.
// containers 열은 그대로 배포 노드(node_id)의 컨테이너다.
func migrateMultiNodeReplicas(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `ALTER TABLE deployments ADD COLUMN extra_containers TEXT NOT NULL DEFAULT '[]'`)
	return err
}

// migrateTemplates는 사용자 템플릿(마켓플레이스)과 내장 템플릿 배포 횟수를 추가한다.
func migrateTemplates(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE templates (
			id TEXT PRIMARY KEY,
			key TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			readme TEXT NOT NULL DEFAULT '',
			icon TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '[]',
			definition TEXT NOT NULL,
			visibility TEXT NOT NULL DEFAULT 'private',
			owner_user_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deploy_count INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX templates_owner ON templates(owner_user_id)`,
		`CREATE TABLE builtin_template_stats (
			key TEXT PRIMARY KEY,
			deploy_count INTEGER NOT NULL DEFAULT 0
		)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateObservability는 알림 채널(전달 기록 포함), 사용자 알림함과 구독 설정, 지표 알림 규칙, 시간별 사용량 집계,
// 프로젝트 월간 사용량 알림, 이벤트의 배포 ID를 추가한다. 예전 프로젝트 webhook(notify_webhook_url)은 그대로 두고
// 암묵적인 채널로 계속 보낸다(API 필드 호환).
func migrateObservability(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE notification_channels (
			id TEXT PRIMARY KEY,
			project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			name TEXT NOT NULL,
			config TEXT NOT NULL DEFAULT '',
			events TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX notification_channels_project ON notification_channels(project_id)`,
		`CREATE TABLE notification_deliveries (
			id TEXT PRIMARY KEY,
			channel_id TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
			event_id TEXT NOT NULL DEFAULT '',
			event_kind TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			status_code INTEGER,
			error TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX notification_deliveries_channel ON notification_deliveries(channel_id, created_at DESC)`,
		`CREATE TABLE user_notifications (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			event_id TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			environment_id TEXT,
			service_id TEXT,
			kind TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT NOT NULL,
			read_at TEXT
		)`,
		`CREATE INDEX user_notifications_user ON user_notifications(user_id, created_at DESC)`,
		`ALTER TABLE users ADD COLUMN notification_events TEXT`,
		`ALTER TABLE events ADD COLUMN deployment_id TEXT`,
		`CREATE TABLE alert_rules (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT REFERENCES environments(id) ON DELETE CASCADE,
			service_id TEXT REFERENCES services(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT '',
			metric TEXT NOT NULL,
			comparator TEXT NOT NULL,
			threshold REAL NOT NULL,
			for_seconds INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_state TEXT NOT NULL DEFAULT 'ok',
			last_value REAL,
			pending_since TEXT,
			last_fired_at TEXT,
			last_evaluated_at TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX alert_rules_project ON alert_rules(project_id)`,
		`CREATE TABLE usage_hourly (
			project_id TEXT NOT NULL,
			environment_id TEXT NOT NULL,
			service_id TEXT NOT NULL,
			hour INTEGER NOT NULL,
			cpu_core_seconds REAL NOT NULL DEFAULT 0,
			memory_gb_seconds REAL NOT NULL DEFAULT 0,
			net_egress_bytes INTEGER NOT NULL DEFAULT 0,
			disk_gb_hours REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (service_id, environment_id, hour)
		)`,
		`CREATE INDEX usage_hourly_project ON usage_hourly(project_id, hour)`,
		`ALTER TABLE projects ADD COLUMN usage_alert TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN usage_alert_state TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateUsers는 다중 사용자 인증(사용자, 서버 측 세션, 프로젝트 멤버, 초대, API 토큰, 2FA 복구 코드, 감사 기록)과
// 이벤트 actor를 추가한다. 기본 owner 계정(admin@local)은 KEEL_ADMIN_PASSWORD로 로그인하는 계정이다(비밀번호 해시 없음).
func migrateUsers(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			password_hash TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL,
			totp_secret TEXT NOT NULL DEFAULT '',
			totp_enabled INTEGER NOT NULL DEFAULT 0,
			totp_last_step INTEGER NOT NULL DEFAULT 0,
			builtin INTEGER NOT NULL DEFAULT 0,
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			last_login_at TEXT
		)`,
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			auth_tag TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			last_seen TEXT NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX sessions_user ON sessions(user_id)`,
		`CREATE TABLE project_members (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (project_id, user_id)
		)`,
		`CREATE INDEX project_members_user ON project_members(user_id)`,
		`CREATE TABLE invites (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			role TEXT NOT NULL,
			project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
			project_role TEXT,
			created_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE api_tokens (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			prefix TEXT NOT NULL,
			scope TEXT NOT NULL,
			project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT REFERENCES environments(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			expires_at TEXT,
			last_used_at TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX api_tokens_user ON api_tokens(user_id)`,
		`CREATE TABLE recovery_codes (
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			code_hash TEXT NOT NULL,
			PRIMARY KEY (user_id, code_hash)
		)`,
		`CREATE TABLE audit_log (
			id TEXT PRIMARY KEY,
			actor_id TEXT NOT NULL DEFAULT '',
			actor TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			target TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX audit_log_created ON audit_log(created_at DESC)`,
		`ALTER TABLE events ADD COLUMN actor TEXT`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	_, err := tx.q.ExecContext(ctx, `INSERT INTO users (id, email, name, role, builtin, created_at) VALUES (?,?,?,?,1,?)`,
		BuiltinUserID, BuiltinUserEmail, "admin", RoleOwner, ts(time.Now()))
	return err
}

// migrateVolumeLimits는 볼륨 크기 상한(soft limit)과 마지막으로 알린 상한 상태를 추가한다.
func migrateVolumeLimits(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`ALTER TABLE volumes ADD COLUMN size_limit_mb INTEGER`,
		`ALTER TABLE volumes ADD COLUMN limit_state TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migratePreviewEnvs는 PR 미리보기 환경(프로젝트 설정, 환경 종류/PR 정보)과 CI 대기 배포 조회 인덱스를 추가한다.
func migratePreviewEnvs(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`ALTER TABLE projects ADD COLUMN pr_settings TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE environments ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE environments ADD COLUMN pr_number INTEGER`,
		`ALTER TABLE environments ADD COLUMN pr_url TEXT`,
		`ALTER TABLE environments ADD COLUMN pr_branch TEXT`,
		`ALTER TABLE environments ADD COLUMN pr_repo TEXT`,
		`CREATE INDEX environments_pr ON environments(pr_repo, pr_number)`,
		`CREATE INDEX deployments_waiting ON deployments(status, commit_sha)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateV8은 agent 인증서 발급/폐기 기록(node_certs)을 추가한다.
func migrateV8(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `CREATE TABLE node_certs (serial TEXT PRIMARY KEY, node_id TEXT NOT NULL, not_after TEXT NOT NULL, issued_at TEXT NOT NULL, revoked_at TEXT)`)
	return err
}

// migrateV7은 원격 노드가 연결 때 보고하는 정보(버전, 자원)를 저장한다.
func migrateV7(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `ALTER TABLE nodes ADD COLUMN info TEXT NOT NULL DEFAULT '{}'`)
	return err
}

func migrateV6(ctx context.Context, tx *Store) error {
	for _, query := range []string{
		`ALTER TABLE nodes ADD COLUMN credential_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE nodes ADD COLUMN status TEXT NOT NULL DEFAULT 'offline'`,
		`ALTER TABLE nodes ADD COLUMN address TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE nodes ADD COLUMN last_seen_at TEXT`,
		`CREATE TABLE node_join_tokens (token_hash TEXT PRIMARY KEY, name TEXT NOT NULL, expires_at TEXT NOT NULL)`,
	} {
		if _, err := tx.q.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

// migrateV5는 노드 배치 식별자를 보존한다. 기존 인스턴스와 배포는 로컬 노드에 남는다.
func migrateV5(ctx context.Context, tx *Store) error {
	for _, query := range []string{
		`ALTER TABLE instances ADD COLUMN node_id TEXT NOT NULL DEFAULT 'node_local'`,
		`ALTER TABLE deployments ADD COLUMN node_id TEXT NOT NULL DEFAULT 'node_local'`,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, schedulable INTEGER NOT NULL DEFAULT 1, labels TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL)`,
		`INSERT INTO nodes (id, name, created_at) VALUES ('node_local', 'local', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
	} {
		if _, err := tx.q.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

// migrateV4는 프로젝트별 private registry 인증 정보를 추가한다.
func migrateV4(ctx context.Context, tx *Store) error {
	_, err := tx.q.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN registries TEXT NOT NULL DEFAULT '[]'`)
	return err
}

// migrateV3는 이벤트에 서비스 이름 스냅샷을 추가한다.
func migrateV3(ctx context.Context, tx *Store) error {
	if _, err := tx.q.ExecContext(ctx, `ALTER TABLE events ADD COLUMN service_name TEXT`); err != nil {
		return err
	}
	_, err := tx.q.ExecContext(ctx, `UPDATE events SET service_name = (SELECT name FROM services WHERE services.id = events.service_id) WHERE service_id IS NOT NULL`)
	return err
}

// migrateV2는 배포에 요청 설정 스냅샷(req_spec)을 추가한다. 기존 배포는 실제 설정으로 채운다.
func migrateV2(ctx context.Context, tx *Store) error {
	if _, err := tx.q.ExecContext(ctx, `ALTER TABLE deployments ADD COLUMN req_spec TEXT NOT NULL DEFAULT '{}'`); err != nil {
		return err
	}
	_, err := tx.q.ExecContext(ctx, `UPDATE deployments SET req_spec = spec`)
	return err
}

const schemaV1 = `
CREATE TABLE projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	default_environment_id TEXT NOT NULL DEFAULT '',
	notify_webhook_url TEXT,
	notify_events TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL
);
CREATE TABLE environments (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(project_id, name)
);
CREATE TABLE services (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	icon TEXT,
	source_type TEXT NOT NULL,
	database_kind TEXT,
	template TEXT,
	created_at TEXT NOT NULL,
	UNIQUE(project_id, name)
);
CREATE TABLE instances (
	id TEXT PRIMARY KEY,
	service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
	environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
	spec TEXT NOT NULL,
	variables TEXT NOT NULL,
	status TEXT NOT NULL,
	current_deployment_id TEXT,
	canvas TEXT,
	updated_at TEXT NOT NULL,
	UNIQUE(service_id, environment_id)
);
CREATE TABLE deployments (
	id TEXT PRIMARY KEY,
	service_id TEXT NOT NULL,
	environment_id TEXT NOT NULL,
	instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
	status TEXT NOT NULL,
	trigger TEXT NOT NULL,
	commit_sha TEXT, commit_message TEXT, commit_author TEXT, branch TEXT,
	image TEXT, builder_used TEXT, error TEXT,
	spec TEXT NOT NULL DEFAULT '{}',
	vars_raw TEXT NOT NULL DEFAULT '{}',
	env TEXT NOT NULL DEFAULT '{}',
	containers TEXT NOT NULL DEFAULT '[]',
	source TEXT NOT NULL DEFAULT '',
	changed_files TEXT NOT NULL DEFAULT 'null',
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT
);
CREATE INDEX deployments_instance ON deployments(instance_id, created_at DESC);
CREATE TABLE shared_variables (
	environment_id TEXT PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
	data TEXT NOT NULL
);
CREATE TABLE volumes (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
	instance_id TEXT REFERENCES instances(id) ON DELETE SET NULL,
	name TEXT NOT NULL,
	mount_path TEXT NOT NULL,
	docker_name TEXT NOT NULL UNIQUE,
	backup_cron TEXT,
	backup_keep INTEGER NOT NULL DEFAULT 7,
	created_at TEXT NOT NULL
);
CREATE TABLE backups (
	id TEXT PRIMARY KEY,
	volume_id TEXT NOT NULL REFERENCES volumes(id) ON DELETE CASCADE,
	path TEXT NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	error TEXT,
	created_at TEXT NOT NULL
);
CREATE TABLE events (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	environment_id TEXT,
	service_id TEXT,
	kind TEXT NOT NULL,
	message TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX events_project ON events(project_id, created_at DESC);
CREATE TABLE cron_runs (
	id TEXT PRIMARY KEY,
	deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
	instance_id TEXT NOT NULL,
	container TEXT NOT NULL,
	started_at TEXT NOT NULL,
	finished_at TEXT,
	exit_code INTEGER,
	status TEXT NOT NULL
);
CREATE INDEX cron_runs_dep ON cron_runs(deployment_id, started_at DESC);
CREATE TABLE metrics (
	instance_id TEXT NOT NULL,
	t INTEGER NOT NULL,
	res INTEGER NOT NULL,
	cpu REAL NOT NULL,
	mem INTEGER NOT NULL,
	mem_limit INTEGER NOT NULL,
	rx INTEGER NOT NULL,
	tx INTEGER NOT NULL,
	disk INTEGER,
	replicas INTEGER NOT NULL
);
CREATE INDEX metrics_instance_t ON metrics(instance_id, t);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	if err == sql.ErrNoRows {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for v := version; v < len(migrations); v++ {
		if noForeignKeyMigrations[v] {
			if err := s.migrateWithoutForeignKeys(ctx, v); err != nil {
				return fmt.Errorf("migration %d: %w", v+1, err)
			}
			continue
		}
		err := s.Tx(ctx, func(tx *Store) error {
			if err := migrations[v](ctx, tx); err != nil {
				return err
			}
			_, err := tx.q.ExecContext(ctx, `UPDATE schema_version SET version=?`, v+1)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
	}
	return s.ensureDefaultProject(ctx)
}

func (s *Store) migrateWithoutForeignKeys(ctx context.Context, v int) error {
	// 커넥션이 하나뿐이라(SetMaxOpenConns(1)) PRAGMA와 트랜잭션이 같은 커넥션에서 돈다.
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer s.db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
	return s.Tx(ctx, func(tx *Store) error {
		if err := migrations[v](ctx, tx); err != nil {
			return err
		}
		rows, err := tx.q.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			return err
		}
		broken := rows.Next()
		rows.Close()
		if broken {
			return errors.New("foreign key check failed after migration")
		}
		_, err = tx.q.ExecContext(ctx, `UPDATE schema_version SET version=?`, v+1)
		return err
	})
}

func (s *Store) tableExists(ctx context.Context, name string) bool {
	var n int
	s.q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0
}

// migrateV1은 v1 스키마를 만들고, v0(apps) 데이터가 있으면 기본 프로젝트의 production 환경으로 옮긴다.
func migrateV1(ctx context.Context, tx *Store) error {
	legacy := tx.tableExists(ctx, "apps")
	if legacy {
		if _, err := tx.q.ExecContext(ctx, `ALTER TABLE deployments RENAME TO v0_deployments`); err != nil {
			return err
		}
		tx.q.ExecContext(ctx, `DROP INDEX IF EXISTS deployments_app`)
	}
	if _, err := tx.q.ExecContext(ctx, schemaV1); err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	p := &Project{Name: DefaultProjectName}
	env, err := tx.CreateProject(ctx, p)
	if err != nil {
		return err
	}
	rows, err := tx.q.QueryContext(ctx, `SELECT id, name, repo_url, branch, dockerfile, build_context, port, healthcheck_path, domains, env, status, current_deployment_id FROM apps`)
	if err != nil {
		return err
	}
	type app struct {
		id, name, repo, branch, dockerfile, buildCtx, hc, domains, env, status string
		port                                                                   int
		cur                                                                    sql.NullString
	}
	var apps []app
	for rows.Next() {
		var a app
		if err := rows.Scan(&a.id, &a.name, &a.repo, &a.branch, &a.dockerfile, &a.buildCtx, &a.port, &a.hc, &a.domains, &a.env, &a.status, &a.cur); err != nil {
			rows.Close()
			return err
		}
		apps = append(apps, a)
	}
	rows.Close()
	statusMap := map[string]string{"idle": InstIdle, "deploying": InstFailed, "running": InstOnline, "failed": InstFailed, "stopped": InstOffline}
	depStatus := map[string]DeployStatus{"queued": DepFailed, "cloning": DepFailed, "building": DepFailed, "starting": DepFailed,
		"healthchecking": DepFailed, "live": DepLive, "failed": DepFailed, "cancelled": DepCancelled, "superseded": DepRemoved, "rolled_back": DepRemoved}
	for _, a := range apps {
		svc := &Service{ProjectID: p.ID, Name: a.name, SourceType: "repo", Icon: ptr("github")}
		if err := tx.CreateService(ctx, svc); err != nil {
			return err
		}
		var hosts []string
		json.Unmarshal([]byte(a.domains), &hosts)
		var vars map[string]string
		json.Unmarshal([]byte(a.env), &vars)
		repo := a.repo
		port := a.port
		in := &Instance{ServiceID: svc.ID, EnvironmentID: env.ID, Variables: vars, Status: statusMap[a.status]}
		in.Spec = Spec{RepoURL: &repo, Branch: a.branch, RootDir: "/" + trimDot(a.buildCtx), DockerfilePath: a.dockerfile,
			Port: &port, HealthcheckPath: a.hc, Builder: "dockerfile", RestartPolicy: "always"}
		for _, h := range hosts {
			in.Domains = append(in.Domains, Domain{Host: h, Kind: "custom"})
		}
		if in.Status == "" {
			in.Status = InstIdle
		}
		if err := tx.CreateInstance(ctx, in); err != nil {
			return err
		}
		drows, err := tx.q.QueryContext(ctx, `SELECT id, status, commit_sha, image, container, error, trigger, created_at, started_at, finished_at FROM v0_deployments WHERE app_id=?`, a.id)
		if err != nil {
			return err
		}
		var deps []*Deployment
		for drows.Next() {
			var d Deployment
			var st, created string
			var sha, image, ctr, errS, started, finished sql.NullString
			if err := drows.Scan(&d.ID, &st, &sha, &image, &ctr, &errS, &d.Trigger, &created, &started, &finished); err != nil {
				drows.Close()
				return err
			}
			d.Status = depStatus[st]
			if d.Status == "" {
				d.Status = DepFailed
			}
			d.CommitSHA, d.Image, d.Error = nullStr(sha), nullStr(image), nullStr(errS)
			if ctr.Valid {
				d.Containers = []string{ctr.String}
			}
			d.CreatedAt, d.StartedAt, d.FinishedAt = parseTS(created), parseNullTS(started), parseNullTS(finished)
			deps = append(deps, &d)
		}
		drows.Close()
		for _, d := range deps {
			b := "dockerfile"
			d.BuilderUsed = &b
			d.Branch = &in.Branch
			if _, err := tx.q.ExecContext(ctx, `INSERT INTO deployments (`+depColsV1+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				d.ID, svc.ID, env.ID, in.ID, d.Status, d.Trigger, d.CommitSHA, nil, nil, d.Branch, d.Image, d.BuilderUsed, d.Error,
				jsonStr(in.Spec), jsonStr(vars), jsonStr(vars), jsonStr(d.Containers), "", "null", ts(d.CreatedAt), nullTS(d.StartedAt), nullTS(d.FinishedAt)); err != nil {
				return err
			}
		}
		if a.cur.Valid {
			tx.q.ExecContext(ctx, `UPDATE instances SET current_deployment_id=? WHERE id=?`, a.cur.String, in.ID)
		}
	}
	if _, err := tx.q.ExecContext(ctx, `DROP TABLE v0_deployments; DROP TABLE apps;`); err != nil {
		return err
	}
	return nil
}

func trimDot(s string) string {
	for len(s) > 0 && (s[0] == '.' || s[0] == '/') {
		s = s[1:]
	}
	return s
}

func ptr[T any](v T) *T { return &v }

func (s *Store) ensureDefaultProject(ctx context.Context) error {
	ps, err := s.ListProjects(ctx)
	if err != nil || len(ps) > 0 {
		return err
	}
	if _, err := s.GetSetting(ctx, "default_project_created"); err == nil {
		return nil // 사용자가 지웠으면 다시 만들지 않는다
	}
	if _, err := s.CreateProject(ctx, &Project{Name: DefaultProjectName}); err != nil {
		return err
	}
	return s.SetSetting(ctx, "default_project_created", time.Now().UTC().Format(time.RFC3339))
}

const depColsV1 = `id, service_id, environment_id, instance_id, status, trigger, commit_sha, commit_message, commit_author, branch,
	image, builder_used, error, spec, vars_raw, env, containers, source, changed_files, created_at, started_at, finished_at`
