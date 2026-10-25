package deploy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// 수동 배포는 clone 전까지 SHA가 없어 initializing 알림이 commit status로 가지 못한다.
// clone으로 SHA를 알게 되면 진행 중 상태를 한 번 알려야 GitHub에 pending → success 순서가 남는다(e2e에서 success만 올라가던 버그).
func TestSetCommitNotifiesWhenSHAFirstKnown(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var got []store.Deployment
	m := &Manager{Store: st, StatusHook: func(d store.Deployment) { got = append(got, d) }}
	d := &store.Deployment{ID: "dep_x", Status: store.DepInitializing, Spec: store.Spec{Branch: "main"}}
	m.setCommit(context.Background(), d, &node.Commit{SHA: "0123456789abcdef0123456789abcdef01234567", Message: "m", Author: "a"})
	if len(got) != 1 || got[0].CommitSHA == nil || *got[0].CommitSHA != "0123456789abcdef0123456789abcdef01234567" || got[0].Status != store.DepInitializing {
		t.Fatalf("want one in-progress notification with the SHA, got %+v", got)
	}
	if d.Branch == nil || *d.Branch != "main" {
		t.Fatalf("branch not recorded: %v", d.Branch)
	}

	// webhook 배포는 이미 SHA가 있어 queued/initializing 때 알렸으니 다시 알리지 않는다.
	got = nil
	sha := "fedcba9876543210fedcba9876543210fedcba98"
	d2 := &store.Deployment{ID: "dep_y", Status: store.DepInitializing, CommitSHA: &sha}
	m.setCommit(context.Background(), d2, &node.Commit{SHA: sha})
	if len(got) != 0 {
		t.Fatalf("unexpected notification: %+v", got)
	}
}
