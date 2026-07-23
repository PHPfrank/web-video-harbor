GO ?= go

.PHONY: build test check

build:
	cd helper && $(GO) build -o bin/web-video-helper ./cmd/web-video-helper

test:
	cd helper && $(GO) test ./...

check: test
	node --check extension/background.js
	node --check extension/content.js
