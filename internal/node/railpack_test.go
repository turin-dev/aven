package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNeedsContainerBuilder(t *testing.T) {
	yes := errors.New("docker buildx: exit status 1: ERROR: failed to build: failed to solve: requested experimental feature mergeop  has been disabled on the build server: only enabled with containerd image store backend")
	if !needsContainerBuilder(yes) {
		t.Error("mergeop error must trigger the fallback")
	}
	if needsContainerBuilder(errors.New("docker buildx: exit status 1: npm ERR! missing script: build")) {
		t.Error("ordinary build failures must not trigger the fallback")
	}
}

// 기본 builder가 merge op를 거부하면(classic image store) docker-container builder를 만들어 다시 빌드한다.
// (e2e: docker:28-dind에서 railpack 빌드가 항상 실패하던 버그)
func TestRailpackBuildFallsBackToContainerBuilder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	rp := filepath.Join(dir, "railpack")
	os.WriteFile(rp, []byte(`#!/bin/sh
# prepare <src> --plan-out <plan> --info-out <info>
echo '{"steps":[]}' > "$4"; echo '{"detectedProviders":["node"]}' > "$6"
`), 0o755)
	docker := filepath.Join(dir, "docker")
	os.WriteFile(docker, []byte(`#!/bin/sh
echo "$*" >> `+log+`
case "$*" in
  "buildx build --builder default"*) echo "ERROR: failed to solve: requested experimental feature mergeop  has been disabled on the build server: only enabled with containerd image store backend"; exit 1;;
  "buildx inspect aven-railpack") [ -f `+dir+`/created ] || exit 1;;
  "buildx inspect keel-railpack") exit 1;;
  "buildx create --name aven-railpack --driver docker-container") touch `+dir+`/created;;
  "buildx build --builder aven-railpack"*) [ "$AVEN_SECRET_TOKEN" = "s3cret" ] || exit 3; echo built;;
  *) exit 2;;
esac
`), 0o755)
	work := t.TempDir()
	d := &Docker{Bin: docker}
	var lines []string
	ri, err := d.RailpackBuild(context.Background(), rp, "fe:v1", dir, work, "aven/x:1", map[string]string{"TOKEN": "s3cret"}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("RailpackBuild: %v (output %q)", err, lines)
	}
	if len(ri.DetectedProviders) != 1 || ri.DetectedProviders[0] != "node" {
		t.Errorf("info: %+v", ri)
	}
	b, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(calls) != 5 || !strings.HasPrefix(calls[0], "buildx build --builder default ") || calls[1] != "buildx inspect aven-railpack" ||
		calls[2] != "buildx inspect keel-railpack" ||
		!strings.HasPrefix(calls[3], "buildx create ") || !strings.HasPrefix(calls[4], "buildx build --builder aven-railpack --load ") {
		t.Errorf("docker calls:\n%s", b)
	}
	if strings.Contains(string(b), "s3cret") {
		t.Error("secret value leaked into argv")
	}
	// 두 번째 빌드: builder가 이미 있으면 create하지 않는다
	os.Remove(log)
	if _, err := d.RailpackBuild(context.Background(), rp, "fe:v1", dir, work, "aven/x:2", map[string]string{"TOKEN": "s3cret"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if strings.Contains(string(b), "buildx create") {
		t.Errorf("builder created twice:\n%s", b)
	}
}

func TestRailpackBuilderReusesLegacy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	os.WriteFile(docker, []byte(`#!/bin/sh
case "$*" in
  "buildx inspect aven-railpack") exit 1;;
  "buildx inspect keel-railpack") exit 0;;
  *) exit 2;;
esac
`), 0o755)
	got, err := (&Docker{Bin: docker}).railpackBuilder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != legacyRailpackBuilder {
		t.Fatalf("builder %q, want %s", got, legacyRailpackBuilder)
	}
}
