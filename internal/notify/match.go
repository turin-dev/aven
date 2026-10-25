// Package notify는 이벤트를 알림 채널(webhook, Slack, Discord, 이메일)과 사용자 알림함으로 보낸다.
// 외부로 나가는 HTTP 요청은 SSRF 방지(Guard)를 거치고, 실패하면 백오프로 다시 보내며, 채널마다 전달 기록을 남긴다.
package notify

import (
	"path"
	"regexp"
)

// KnownKinds는 이벤트 버스가 내는 kind 목록이다(구독 UI와 문서용; 구독 패턴은 이 목록 밖의 kind도 받을 수 있다).
var KnownKinds = []string{
	"project.created",
	"environment.created", "environment.deleted", "environment.synced", "environment.preview_created", "environment.preview_deleted",
	"service.created", "service.updated", "service.deleted", "service.restarted", "service.stopped", "service.offline",
	"service.sleeping", "service.woke", "service.exec", "service.scaled",
	"variables.updated", "domain.added", "tcp_proxy.enabled", "deploykey.created", "template.deployed",
	"stack.applied", "stack.deleted", "stack.redeploy",
	"member.added", "member.removed",
	"deploy.requested", "deploy.waiting", "deploy.skipped", "deploy.started", "deploy.live", "deploy.failed", "deploy.crashed", "deploy.rollback",
	"cron.started", "cron.succeeded", "cron.failed",
	"volume.created", "volume.near_limit", "volume.limit_exceeded",
	"backup.done", "backup.failed", "backup.restored", "backup.restore_failed", "volume.backup.failed",
	"dbbackup.done", "dbbackup.failed", "dbbackup.restored", "dbbackup.restore_failed", "database.query", "database.edit",
	"postgres.pooling.deploying", "postgres.pooling.enabled", "postgres.pooling.updated", "postgres.pooling.failed", "postgres.pooling.removed",
	"postgres.upgrade.started", "postgres.upgrade.succeeded", "postgres.upgrade.failed", "postgres.upgrade.reverting",
	"postgres.upgrade.reverted", "postgres.upgrade.revert_failed",
	"alert.firing", "alert.resolved", "usage.threshold_reached",
	"usage.limit_reached", "usage.unsuspended", "usage.resumed",
	"node.unavailable",
	"image.updated", "image.update_failed",
	"notification.test",
}

var patternRe = regexp.MustCompile(`^[a-z0-9_.*]{1,100}$`)

// ValidPattern은 구독 패턴(정확한 kind, "deploy.*", "*")인지 본다.
func ValidPattern(p string) bool {
	if !patternRe.MatchString(p) {
		return false
	}
	_, err := path.Match(p, "x")
	return err == nil
}

// Match는 kind가 패턴 중 하나에 맞는지 본다. "*"는 점을 포함한 아무 문자열이다("deploy.*"는 "deploy.failed"에 맞는다).
func Match(patterns []string, kind string) bool {
	for _, p := range patterns {
		if p == kind {
			return true
		}
		if ok, err := path.Match(p, kind); err == nil && ok {
			return true
		}
	}
	return false
}
