VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: all web server agents cli clis test clean notices

all: web server agents cli clis

web:
	cd web && npm ci && npm run build
	rm -rf internal/webui/dist && cp -r web/out internal/webui/dist && touch internal/webui/dist/.gitkeep

server:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/aven-server ./cmd/aven-server
	ln -sf aven-server bin/keel-server

# aven-server serves these at /downloads/aven-agent-linux-{amd64,arm64} (from AVEN_AGENT_BIN_DIR, default: next to aven-server).
agents:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/aven-agent-linux-amd64 ./cmd/aven-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/aven-agent-linux-arm64 ./cmd/aven-agent
	ln -sf aven-agent-linux-amd64 bin/keel-agent-linux-amd64
	ln -sf aven-agent-linux-arm64 bin/keel-agent-linux-arm64

test:
	go test ./...

clean:
	rm -rf bin web/out

cli:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/aven ./cmd/aven
	ln -sf aven bin/keel

# aven-server serves these at /downloads/aven-<os>-<arch>[.exe] and lists them on the web UI's CLI page (/cli/).
CLI_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
clis:
	mkdir -p bin
	@for t in $(CLI_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build bin/aven-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/aven-$$os-$$arch$$ext ./cmd/aven || exit 1; \
		ln -sf aven-$$os-$$arch$$ext bin/keel-$$os-$$arch$$ext || exit 1; \
	done

# THIRD_PARTY_NOTICES.md: needs go-licenses (go install github.com/google/go-licenses/v2@latest) and web/node_modules.
notices:
	python3 scripts/third-party-notices.py
