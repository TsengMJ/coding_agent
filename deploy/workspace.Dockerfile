# Default workspace image (quill-workspace:latest), used for repositories
# without .quill/Dockerfile or a devcontainer image. Build it on the server:
#   docker build -f deploy/workspace.Dockerfile -t quill-workspace:latest deploy
#
# The agent's commands run here, so it carries common toolchains, and
# Chromium at /usr/bin/chromium for the workspace browser
# (QUILL_WORKSPACE_BROWSER=1). Projects that need more should add
# .quill/Dockerfile.
FROM golang:1.27-bookworm
# Debian's Node is too old for Playwright; node, npm and npx come from the
# official image (merged into /usr/local next to Go).
COPY --from=node:24-bookworm-slim /usr/local/ /usr/local/
RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		build-essential ca-certificates curl git jq less make procps ripgrep unzip \
		python3 python3-pip python3-venv \
		chromium fonts-liberation \
	&& rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
