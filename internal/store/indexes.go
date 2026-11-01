package store

import "context"

// hotIndexes는 자주 도는 조회와 외래 키 자식 열의 인덱스다.
//
// SQLite는 부모 행을 지울 때(ON DELETE CASCADE / SET NULL) 자식 표에서 그 키를 찾는다. 자식 열에 인덱스가 없으면
// 부모 한 행마다 자식 표 전체를 훑는다(프로젝트·환경 삭제가 볼륨, 토큰, 알림 규칙 표 크기에 비례해 느려진다).
// TestForeignKeysIndexed가 새 외래 키에도 인덱스가 있는지 확인한다.
var hotIndexes = []string{
	// 캔버스·배포 경로: 환경의 인스턴스, 볼륨(환경·인스턴스·프로젝트별), 볼륨 백업 목록
	`CREATE INDEX IF NOT EXISTS instances_environment ON instances(environment_id)`,
	`CREATE INDEX IF NOT EXISTS instances_node ON instances(node_id)`,
	`CREATE INDEX IF NOT EXISTS volumes_environment ON volumes(environment_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS volumes_instance ON volumes(instance_id)`,
	`CREATE INDEX IF NOT EXISTS volumes_project ON volumes(project_id)`,
	`CREATE INDEX IF NOT EXISTS backups_volume ON backups(volume_id, created_at DESC)`,
	// cron 실행 기록은 지우지 않아 계속 쌓인다: 서비스별 목록과 재시작 때 running 조회
	`CREATE INDEX IF NOT EXISTS cron_runs_instance ON cron_runs(instance_id, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS cron_runs_running ON cron_runs(status) WHERE status='running'`,
	// 전체 프로젝트 사용량(QueryUsage(""))은 시간 범위로만 찾는다
	`CREATE INDEX IF NOT EXISTS usage_hourly_hour ON usage_hourly(hour)`,
	// 매시 정리(PruneEvents, PruneSessions)
	`CREATE INDEX IF NOT EXISTS events_created ON events(created_at)`,
	`CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires_at)`,
	// 나머지 외래 키 자식 열
	`CREATE INDEX IF NOT EXISTS stack_envs_environment ON stack_envs(environment_id)`,
	`CREATE INDEX IF NOT EXISTS invites_workspace ON invites(workspace_id)`,
	`CREATE INDEX IF NOT EXISTS invites_project ON invites(project_id)`,
	`CREATE INDEX IF NOT EXISTS api_tokens_workspace ON api_tokens(workspace_id)`,
	`CREATE INDEX IF NOT EXISTS api_tokens_project ON api_tokens(project_id)`,
	`CREATE INDEX IF NOT EXISTS api_tokens_environment ON api_tokens(environment_id)`,
	`CREATE INDEX IF NOT EXISTS alert_rules_environment ON alert_rules(environment_id)`,
	`CREATE INDEX IF NOT EXISTS alert_rules_service ON alert_rules(service_id)`,
	`CREATE INDEX IF NOT EXISTS templates_workspace ON templates(workspace_id)`,
	`CREATE INDEX IF NOT EXISTS feature_flag_values_environment ON feature_flag_values(environment_id)`,
	`CREATE INDEX IF NOT EXISTS webauthn_challenges_user ON webauthn_challenges(user_id)`,
	`CREATE INDEX IF NOT EXISTS oauth_pending_user ON oauth_pending_requests(user_id)`,
	`CREATE INDEX IF NOT EXISTS oauth_pending_app ON oauth_pending_requests(app_id)`,
	`CREATE INDEX IF NOT EXISTS oauth_codes_grant ON oauth_authorization_codes(grant_id)`,
	`CREATE INDEX IF NOT EXISTS oauth_codes_user ON oauth_authorization_codes(user_id)`,
	`CREATE INDEX IF NOT EXISTS oauth_codes_app ON oauth_authorization_codes(app_id)`,
	`CREATE INDEX IF NOT EXISTS sandboxes_project ON sandboxes(project_id)`,
	`CREATE INDEX IF NOT EXISTS sandbox_checkpoints_project ON sandbox_checkpoints(project_id)`,
	`CREATE INDEX IF NOT EXISTS ha_clusters_project ON ha_clusters(project_id)`,
	`CREATE INDEX IF NOT EXISTS projects_github_user ON projects(github_user_id)`,
	`CREATE INDEX IF NOT EXISTS session_view_as_user ON session_view_as(user_id)`,
}

// migrateHotIndexes는 hotIndexes를 만든다(IF NOT EXISTS라 다시 돌려도 된다).
func migrateHotIndexes(ctx context.Context, tx *Store) error {
	for _, q := range hotIndexes {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateOrphanSettings는 인스턴스를 지울 때 그 인스턴스의 도메인 basic auth 해시(settings의 DomainAuthKey)도 지우는
// 트리거를 만들고, 이미 남아 있는 것을 정리한다. 설정 표에는 외래 키를 걸 수 없어 서비스·환경·프로젝트를 지우면
// (인스턴스는 CASCADE로 지워진다) bcrypt 해시가 영원히 남았다. 트리거는 CASCADE로 지워질 때도 돈다.
func migrateOrphanSettings(ctx context.Context, tx *Store) error {
	for _, q := range []string{
		`CREATE TRIGGER IF NOT EXISTS instances_domain_auth_deleted AFTER DELETE ON instances
			BEGIN DELETE FROM settings WHERE key >= 'domain_auth:' || OLD.id || ':' AND key < 'domain_auth:' || OLD.id || ';'; END`,
		`DELETE FROM settings WHERE key LIKE 'domain_auth:%' AND instr(substr(key, 13), ':') > 0
			AND substr(key, 13, instr(substr(key, 13), ':') - 1) NOT IN (SELECT id FROM instances)`,
	} {
		if _, err := tx.q.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
