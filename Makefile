GO ?= go

.PHONY: build test check fetch-platform-tools

build:
	cd helper && $(GO) build -o bin/web-video-harbor-helper ./cmd/web-video-harbor-helper

test:
	cd helper && $(GO) test ./...

check: test
	node --check extension/background.js
	node --check extension/content.js

fetch-platform-tools:
	zsh scripts/fetch-yt-dlp.zsh
	zsh scripts/fetch-deno.zsh
