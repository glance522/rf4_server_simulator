# Windows 构建与测试目标（GNU Make）。

GO ?= go
BIN := bin
VERSION ?= $(shell date +%Y%m%d%H%M%S)
BUILDTS ?= $(shell date -u +%Y%m%d%H%M%S)
# 检出目录也编进二进制：多个 worktree 共用同一个 bin/，只靠时间戳版本号
# 无法回答"这份 exe 是哪个检出构建的、是不是我部署的那份"。
SOURCEDIR ?= $(subst \,/,$(CURDIR))
# 提交短码也编进二进制：同一检出目录在不同提交上构建出的 exe 要靠 commit 区分。
GITCOMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X 'rf4server/internal/buildinfo.Version=$(VERSION)' -X 'rf4server/internal/buildinfo.BuildTs=$(BUILDTS)' -X 'rf4server/internal/buildinfo.GitCommit=$(GITCOMMIT)' -X 'rf4server/internal/buildinfo.SourceDir=$(SOURCEDIR)'

CMDS := gateway gameserver seed
ifeq ($(OFFLINE_TOOLS),1)
CMDS += import-character local-membership
endif

.PHONY: build test vet corpus clean doccheck hooks

build:
	@mkdir -p $(BIN)
	@for c in $(CMDS); do \
		$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/$$c.exe ./cmd/$$c || exit 1; \
	done
	$(GO) build -trimpath -ldflags "$(LDFLAGS) -H=windowsgui" -o $(BIN)/launcher.exe ./cmd/launcher
ifeq ($(OFFLINE_TOOLS),1)
	$(GO) build -trimpath -ldflags "$(LDFLAGS) -H=windowsgui" -o "启动游戏.exe" ./cmd/local-launcher
endif
	@rm -f $(BIN)/admin.exe
	@echo "build -> $(BIN)/"

test: vet doccheck
	$(GO) test ./...

vet:
	$(GO) vet ./...

doccheck:
	$(GO) run ./tools/doccheck

hooks:
	git config core.hooksPath .githooks
	@chmod +x .githooks/pre-commit .githooks/pre-push 2>/dev/null || true
	@echo "hooks -> .githooks（pre-commit: doccheck；pre-push: build + vet）"
	@echo "没有 GNU Make 时用：powershell -File tools/hooks.ps1"

corpus:
	$(GO) test ./internal/wiregen/ -run TestCorpusRoundtrip -v

clean:
	rm -rf $(BIN)
