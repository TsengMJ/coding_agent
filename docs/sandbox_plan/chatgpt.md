目標：為 Coding Agent 加入安全、可替換、低耦合的 Sandbox 執行層。

## Phase 1：核心 Sandbox

建立統一介面：

```go
type Sandbox interface {
    Exec(ctx context.Context, cmd string, args ...string) (*ExecResult, error)
    ReadFile(path string) ([]byte, error)
    WriteFile(path string, data []byte) error
    Close() error
}
```

第一版實作：

```text
DockerSandbox
```

設計原則：

```text
每個 Task 一個獨立 Sandbox
Task 結束後銷毀
Agent 不直接接觸 Host
```

## Phase 2：Filesystem Isolation

Sandbox 內：

```text
/workspace   rw
/tmp         rw
/cache       ro / controlled
```

禁止存取：

```text
~/.ssh
~/.aws
~/.gnupg
/etc
/var/run/docker.sock
host filesystem
```

所有 Agent 的：

```text
read_file
write_file
edit_file
execute
```

都必須經過 Sandbox。

## Phase 3：Execution Limits

每個 command 設定：

```text
timeout
CPU limit
memory limit
PID limit
disk limit
stdout/stderr limit
```

例如：

```text
command timeout: 120s
memory: 4~8 GB
CPU: 2~4 cores
PID: 256
```

防止：

```text
infinite loop
fork bomb
OOM
超大 log
```

## Phase 4：Command Output Handling

禁止直接把巨大 stdout/stderr 回傳給 LLM。

流程：

```text
command
↓
full output → artifact store
↓
extract useful preview
↓
return structured result
```

例如：

```json
{
  "exit_code": 1,
  "duration_ms": 1200,
  "stdout_preview": "...",
  "stderr_preview": "...",
  "truncated": true,
  "artifact_id": "exec_123"
}
```

需要完整 log 時再按需讀取。

## Phase 5：Network Policy

預設：

```text
network = deny
```

需要 dependency download 時，只允許：

```text
github.com
proxy.golang.org
registry.npmjs.org
pypi.org
```

未來再增加：

```text
Egress Proxy
Domain Allowlist
Request Policy
```

## Phase 6：Secret Isolation

禁止直接把：

```text
GitHub Token
AWS Key
DB Password
API Key
```

注入 Sandbox environment。

未來需要外部 API 時：

```text
Sandbox
↓
Trusted Proxy / Credential Broker
↓
inject secret
↓
external service
```

Agent 永遠拿不到真實 secret。

## Phase 7：Multi-Agent Permission

第一版採：

```text
parallel read
single writer
```

權限：

```text
Main Agent
→ workspace read/write

Explore Agent
→ read-only

Verifier Agent
→ read-only

Research Agent
→ no workspace write
```

避免 Subagent 互相修改檔案。

## Phase 8：Task Lifecycle

完整流程：

```text
Create Sandbox
↓
Prepare / Clone Repo
↓
Mount Workspace
↓
Run Coding Agent
↓
Edit / Test / Repair
↓
Collect git diff + artifacts
↓
Destroy Sandbox
```

第一版先完成：

```text
Docker per task
Filesystem isolation
Resource limits
Network default deny
Structured command output
Secret isolation
Single-writer policy
Cleanup lifecycle
```

暫時不要優先做：

```text
Firecracker
Kubernetes
Snapshot
Multi-writer worktree
Secret Broker
Complex approval system
```

等基礎 Sandbox 穩定後再擴充。

核心原則：

```text
Agent 不應該知道底層是 Docker。

Agent 只依賴 Sandbox interface。

未來可以自由替換：

DockerSandbox
LocalSandbox
RemoteSandbox
MicroVMSandbox
```