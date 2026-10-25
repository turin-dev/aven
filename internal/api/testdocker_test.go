package api

import "github.com/turin-dev/aven/internal/node"

// testDocker는 없는 docker 실행 파일을 가리킨다. 테스트가 호스트의 실제 Docker(CI 러너 등)를 건드리지 않고 항상 실패 경로를 탄다.
func testDocker() *node.Docker { return &node.Docker{Bin: "/nonexistent/aven-test-docker"} }
