#!/usr/bin/env bash
# Runs the shell sandbox integration tests on Linux inside a container:
# once as root, once as an unprivileged user, and once without bubblewrap
# installed. Docker is only the test environment here.
#
# --privileged is needed because Docker's default seccomp profile and
# dropped capabilities stop bubblewrap from creating the user, mount, pid
# and network namespaces it confines commands with.
set -euo pipefail

cd "$(dirname "$0")/.."
go_version=$(awk '/^go /{print $2}' go.mod)
image=${GO_IMAGE:-golang:${go_version}}

exec docker run --rm --privileged \
  -v "$PWD":/src:ro -w /src \
  -v quill-sandbox-gomod:/go/pkg/mod -v quill-sandbox-gocache:/gocache \
  -v quill-sandbox-apt:/var/cache/apt/archives \
  -e QUILL_SANDBOX_IT=1 -e DEBIAN_FRONTEND=noninteractive \
  "$image" bash -euo pipefail -c '
    # Keep downloaded packages in the volume; nodejs and npm are slow to fetch.
    rm -f /etc/apt/apt.conf.d/docker-clean
    apt-get update -qq
    apt-get install -y -qq --no-install-recommends bubblewrap curl git python3 python3-venv nodejs npm >/dev/null
    export GOFLAGS=-buildvcs=false GOCACHE=/gocache
    tests="./backend/ossandbox/ ./backend/ ./code/agent/"

    echo "== root =="
    go test -count=1 -v -run Sandbox $tests | grep -E "^(--- |ok|FAIL|panic)"
    go build -o /usr/local/bin/quill ./cmd/quill

    echo "== quill sandbox smoke (root) =="
    cd /tmp && git init -q smoke && cd smoke
    ! quill sandbox -- touch /root/x
    ! quill sandbox -- sh -c "echo x > .git/hooks/x"
    ! quill sandbox -- curl -sS -m5 -o /dev/null https://example.com
    quill sandbox -- sh -c "echo ok > in.txt" && test -f in.txt

    echo "== network allowlist (root) =="
    allow="quill sandbox -network allowlist --"
    ! $allow curl -sS -m10 -o /dev/null https://example.com
    mkdir gop && cd gop && printf "module example.com/v\n\ngo 1.22\n" > go.mod
    printf "package v\nimport (\"testing\"; \"rsc.io/quote\")\nfunc TestQ(t *testing.T) { if quote.Hello() == \"\" { t.Fatal() } }\n" > q_test.go
    $allow go get rsc.io/quote@v1.5.2
    $allow go test ./...
    cd .. && mkdir np && cd np
    echo "require(\"left-pad\")" > index.js
    echo "{\"name\":\"v\",\"version\":\"1.0.0\",\"scripts\":{\"test\":\"node index.js\"}}" > package.json
    $allow npm install --no-audit --no-fund left-pad
    $allow npm test
    cd .. && mkdir py && cd py && echo "def test_x(): assert 1" > test_x.py
    $allow sh -c "python3 -m venv .venv && .venv/bin/pip install -q pytest && .venv/bin/pytest -q"
    cd /src

    echo "== unprivileged user =="
    useradd -m tester
    chmod -R a+rwX /go/pkg/mod
    su tester -s /bin/bash -c "
      export PATH=/usr/local/go/bin:\$PATH GOMODCACHE=/go/pkg/mod GOCACHE=/tmp/gocache-tester \
        GOFLAGS=-buildvcs=false QUILL_SANDBOX_IT=1
      go test -count=1 -v -run Sandbox $tests | grep -E \"^(--- |ok|FAIL|panic)\"
      cd /tmp && mkdir -p smoke-tester && cd smoke-tester
      ! quill sandbox -- touch \$HOME/x
      test ! -e \$HOME/x
      quill sandbox -network allowlist -- curl -sS -m20 -o /dev/null https://proxy.golang.org/
      ! quill sandbox -network allowlist -- curl -sS -m10 -o /dev/null https://example.com
    "

    echo "== without bubblewrap =="
    apt-get remove -y -qq bubblewrap >/dev/null
    QUILL_SANDBOX_EXPECT_MISSING=1 go test -count=1 -v -run NativeReportsMissing ./backend/ossandbox/ | grep -E "^(--- |ok|FAIL)"
    ! quill sandbox -- true
    echo "all sandbox linux tests passed"
  '
