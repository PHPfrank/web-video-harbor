GO ?= go

.PHONY: build test check

build:
	cd helper && $(GO) build -o bin/web-video-harbor-helper ./cmd/web-video-harbor-helper

test:
	cd helper && $(GO) test ./...

check: test
	node --check extension/background.js
	node --check extension/content.js
