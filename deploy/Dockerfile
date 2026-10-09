# quill team server image. Build from the repository root:
#   docker build -f deploy/Dockerfile -t quill:latest .
FROM golang:1.27-bookworm AS build
WORKDIR /src/apps/agent
COPY apps/agent/go.mod apps/agent/go.sum ./
RUN go mod download
COPY apps/agent/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/quill ./cmd/quill

# The docker CLI talks to the host daemon through the mounted socket: it
# starts the workspace containers. git clones repositories into workspaces.
FROM docker:29-cli AS dockercli

FROM debian:bookworm-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates git tini \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /out/quill /usr/local/bin/quill
# ~/.quill holds config.toml, the MCP config and skills; mount a volume
# there to keep them.
ENV HOME=/data
WORKDIR /data
EXPOSE 8123 9090
ENTRYPOINT ["tini", "--", "quill", "serve", "-public", "-addr", ":8123"]
