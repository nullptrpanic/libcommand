SHELL := /bin/bash

.DEFAULT_GOAL := all

.PHONY: all libcommand playground clean

TARGET_ROOT := target
LIBCOMMAND_TARGET_DIR := $(TARGET_ROOT)/libcommand
LIBCOMMAND_TARGET := $(LIBCOMMAND_TARGET_DIR)/libcommand
PLAYGROUND_STAGE := cmd/playground/.assets
PLAYGROUND_TARGET_DIR := $(TARGET_ROOT)/playground
PLAYGROUND_TARGET := $(PLAYGROUND_TARGET_DIR)/playground

all: libcommand playground

libcommand:
	@set -euo pipefail; \
	rm -rf -- "$(LIBCOMMAND_TARGET_DIR)"; \
	mkdir -p -- "$(LIBCOMMAND_TARGET_DIR)"; \
	go build -trimpath -ldflags='-s -w' -o "$(LIBCOMMAND_TARGET)" ./cmd/libcommand; \
	printf 'libcommand binary built at %s\n' "$(LIBCOMMAND_TARGET)"

playground:
	@set -euo pipefail; \
	stage="$(PLAYGROUND_STAGE)"; \
	trap 'rm -rf -- "$$stage"' EXIT; \
	rm -rf -- "$$stage" "$(PLAYGROUND_TARGET_DIR)"; \
	mkdir -p -- "$$stage" "$(PLAYGROUND_TARGET_DIR)"; \
	GOOS=js GOARCH=wasm go build -o "$$stage/libcommand.wasm" ./cmd/playground-wasm; \
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" "$$stage/wasm_exec.js"; \
	cp playground/index.html playground/styles.css playground/model.js playground/handler.js playground/app.js playground/worker.js "$$stage/"; \
	go build -tags=playground_assets -trimpath -ldflags='-s -w' -o "$(PLAYGROUND_TARGET)" ./cmd/playground; \
	printf 'playground binary built at %s\n' "$(PLAYGROUND_TARGET)"

clean:
	rm -rf -- "$(TARGET_ROOT)" "$(PLAYGROUND_STAGE)" playground/.dist playground/.bin
