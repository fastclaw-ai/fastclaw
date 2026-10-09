.PHONY: build build-web bundle-skills clean release-local install test dev

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
# Stamp build identity into both the main package (legacy `fastclaw
# version` consumer) and internal/buildinfo (the agent runtime + system
# prompt reader). Keeping both in sync from one VERSION variable means
# release builds hand the model the same string the CLI reports.
BUILDINFO = github.com/fastclaw-ai/fastclaw/internal/buildinfo
LDFLAGS  = -s -w \
	-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE) \
	-X $(BUILDINFO).Version=$(VERSION) -X $(BUILDINFO).Commit=$(COMMIT) -X $(BUILDINFO).Date=$(DATE)

# Install destination. Default is the per-user XDG-style bin so a plain
# `make install` doesn't need sudo. Override with e.g.
#   make install PREFIX=/usr/local        (system-wide; needs sudo)
#   make install PREFIX=/opt/homebrew     (Apple Silicon brew layout)
PREFIX ?= $(HOME)/.local

build-web:
	cd web && pnpm install --frozen-lockfile && pnpm build
	rm -rf internal/setup/web
	cp -r web/out internal/setup/web

# bundle-skills syncs skills the binary should ship with into the embed
# tree under internal/agent/bundled_skills/. Source of truth lives at
# repo-root skills/<name>/ so editing happens in one place; this target
# overwrites the embed copy each build so drift can't accumulate.
# `go:embed` can't follow symlinks or escape the package dir, so a real
# copy is the only path that works.
bundle-skills:
	@rm -rf internal/agent/bundled_skills/skill-creator
	@cp -R skills/skill-creator internal/agent/bundled_skills/skill-creator
	@rm -rf internal/agent/bundled_skills/find-skills
	@cp -R skills/find-skills internal/agent/bundled_skills/find-skills
	@rm -rf internal/agent/bundled_skills/local-coding-agents
	@cp -R skills/local-coding-agents internal/agent/bundled_skills/local-coding-agents
	@echo "==> bundled skills synced"

# bundle-docs copies docs served by the binary into its embed tree
# (/skills/agent-integration/SKILL.md). TestIntegrationDocMatchesSkill fails when it's stale.
bundle-docs:
	@cp skills/agent-integration/SKILL.md internal/setup/apidocs/agent-integration.md
	@echo "==> bundled docs synced"

build: build-web bundle-skills bundle-docs
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/fastclaw ./cmd/fastclaw

install: build
	install -d $(PREFIX)/bin
	install -m 0755 bin/fastclaw $(PREFIX)/bin/fastclaw
	@echo
	@echo "==> installed: $(PREFIX)/bin/fastclaw"
	@case ":$$PATH:" in *":$(PREFIX)/bin:"*) ;; *) \
	  echo "    NOTE: $(PREFIX)/bin is not on your PATH."; \
	  echo "    Add to ~/.zshrc:  export PATH=\"$(PREFIX)/bin:\$$PATH\"" ;; \
	esac

test:
	go test ./...

# dev runs the Go gateway under air (Go-only rebuilds) and the web UI under
# `next dev` on WEB_PORT. The gateway proxies page requests to it, so keep
# using http://localhost:$(DEV_PORT) (printed once the gateway is up; ignore
# the URL next dev prints for itself) and web edits hot-reload. The embedded
# export is only built once so the binary compiles; `make build-web` refreshes it.
#
# Local secrets for dev (e.g. FASTCLAW_CONNANY_URL / FASTCLAW_CONNANY_API_KEY)
# go in an untracked .env.dev at the repo root; it's sourced if present.
#
# Dev data is isolated from the release install: its own FASTCLAW_HOME
# (sqlite db, workspaces, skills, pid, logs) and port, so both can run
# side by side. Point the CLI at it with the same two env vars, e.g.
#   FASTCLAW_HOME=~/.fastclaw-dev FASTCLAW_PORT=18955 fastclaw chat
WEB_PORT ?= 18954
DEV_PORT ?= 18955
DEV_HOME ?= $(HOME)/.fastclaw-dev
dev:
	@test -f internal/setup/web/index.html || $(MAKE) build-web
	@cd web && pnpm install --frozen-lockfile --silent
	@trap 'kill 0' EXIT INT TERM; \
	if [ -f .env.dev ]; then set -a; . ./.env.dev; set +a; fi; \
	(cd web && pnpm exec next dev --hostname 127.0.0.1 --port $(WEB_PORT)) & \
	(until curl -sf --noproxy '*' -o /dev/null http://127.0.0.1:$(DEV_PORT)/; do sleep 1; done; \
	 printf '\n\033[1;32m  ➜ FastClaw dev: http://localhost:%s\033[0m  (data: %s; :%s is next dev, behind the gateway)\n\n' \
	   $(DEV_PORT) $(DEV_HOME) $(WEB_PORT)) & \
	FASTCLAW_HOME=$(DEV_HOME) FASTCLAW_PORT=$(DEV_PORT) \
	FASTCLAW_DEV_SKIP_WEB=1 FASTCLAW_DEV_WEB_URL=http://127.0.0.1:$(WEB_PORT) air

clean:
	rm -rf bin/ dist/ tmp/

# Build all platforms
release-local: build-web bundle-skills bundle-docs
	@mkdir -p dist
	@# macOS
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/fastclaw_darwin_arm64/fastclaw  ./cmd/fastclaw
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/fastclaw_darwin_amd64/fastclaw  ./cmd/fastclaw
	@# Linux
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/fastclaw_linux_arm64/fastclaw   ./cmd/fastclaw
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/fastclaw_linux_amd64/fastclaw   ./cmd/fastclaw
	@# Windows
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/fastclaw_windows_amd64/fastclaw.exe ./cmd/fastclaw
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/fastclaw_windows_arm64/fastclaw.exe ./cmd/fastclaw
	@# Package: tar.gz for unix, zip for windows
	@cd dist && for d in fastclaw_darwin_* fastclaw_linux_*; do tar -czf "$${d}.tar.gz" -C "$$d" fastclaw; done
	@cd dist && for d in fastclaw_windows_*; do (cd "$$d" && zip -q "../$${d}.zip" fastclaw.exe); done
	@echo "Release artifacts:"
	@ls -lh dist/*.tar.gz dist/*.zip 2>/dev/null
