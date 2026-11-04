# Windows 构建与测试目标（GNU Make）。

GO ?= go
BIN := bin
VERSION ?= $(shell date +%Y%m%d%H%M%S)
LDFLAGS := -s -w -X 'rf4server/internal/buildinfo.Version=$(VERSION)'

CMDS := gateway gameserver seed
ifeq ($(OFFLINE_TOOLS),1)
CMDS += import-character local-membership
endif

.PHONY: build test vet corpus clean

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

test: vet
	$(GO) test ./...

vet:
	$(GO) vet ./...

corpus:
	$(GO) test ./internal/wiregen/ -run TestCorpusRoundtrip -v

clean:
	rm -rf $(BIN)
