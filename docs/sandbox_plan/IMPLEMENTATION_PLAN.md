# OS 層 Process Sandbox 實作計劃

shell 子程序（`execute`、`run_tests`、`diagnostics`）在 OS 層 sandbox 內執行：macOS 用 Seatbelt，Linux 用 bubblewrap。agent 本身不進 sandbox。全部完成後，重點併入 [docs/intros/08_safety_and_permissions.md](../intros/08_safety_and_permissions.md)，並刪除本檔。

## 主流做法對照

| | 隔離機制 | 預設檔案權限 | 網路 | 和核准的關係 |
|---|---|---|---|---|
| Claude Code（`@anthropic-ai/sandbox-runtime`） | macOS `sandbox-exec` + 動態 Seatbelt profile；Linux bubblewrap + 獨立 network namespace | 可讀整台機器（有 deny 清單），只能寫 cwd 與 tmp | sandbox 外的 HTTP/SOCKS proxy，domain allowlist | `autoAllowBashIfSandboxed`；`dangerouslyDisableSandbox` 要核准；`excludedCommands` |
| Codex CLI | macOS Seatbelt；Linux Landlock + seccomp（helper binary），新版也支援 bubblewrap | `read-only` / `workspace-write` / `danger-full-access`；workspace-write 可寫 cwd、`$TMPDIR`、`/tmp`，`.git` 唯讀 | workspace-write 預設斷網 | approval policy 獨立（`untrusted` / `on-failure` / `on-request` / `never`）；被擋下時可詢問是否在 sandbox 外重跑；`codex sandbox macos` 除錯指令 |
| Cursor | macOS Seatbelt；Linux Landlock / seccomp | workspace 可寫 | 預設斷網 | 被擋下時提示在 sandbox 外重跑 |

共通設計：只包住 shell 子程序；policy 跨平台、各平台各自實作；預設只能寫 workspace、斷網、敏感目錄不能讀；sandbox 與核准分開，sandbox 內可以少問；被擋下時要說明，逃生口需要核准。

## 設計

- `backend/ossandbox`：`Policy`（Mode、WritableRoots、DenyRead、DenyWrite、Network、AllowedDomains）、`Sandbox` interface（`Command` 產生包好的 `*exec.Cmd`）；實作 `seatbelt`（darwin）、`bwrap`（linux）、`none`。
- `LocalShellOptions.Sandbox` / `Policy`：沒有設定時行為不變。
- 預設 workspace-write：可寫專案根目錄、暫存目錄、語言工具 cache；`.git/hooks`、`.git/config`、`.quill/` 不可寫；`~/.ssh`、`~/.aws` 等 credential 目錄不可讀；網路預設 none。

## 測試方式

- 單元測試：`make check`。
- macOS 整合測試：`make test-sandbox`（`QUILL_SANDBOX_IT=1`，真的呼叫 `sandbox-exec`）。
- Linux 整合測試：`make test-sandbox-linux`，在 `golang` 容器內安裝 bubblewrap 後跑同一組測試，root 與非 root 各一次。Docker 只是測試環境。
- 不跑 benchmark（quillbench、regress subset、bench-smoke）。

## Stage S1: Policy 模型 + macOS Seatbelt
**Goal**: `Policy`、`Sandbox` interface 與 Seatbelt profile 產生器；`LocalShellBackend.Execute` 透過 sandbox 建立指令；`quill sandbox [-mode m] -- <cmd>` 除錯指令。
**Success Criteria**: `make check` 通過；`make test-sandbox` 在 macOS 通過；`quill sandbox -- touch ~/x` 失敗、`quill sandbox -- go test ./backend/` 成功。
**Tests**: profile 產生（路徑跳脫、symlink 解析、deny 優先）；darwin 整合：寫 workspace 成功、寫 `$HOME` 與 `.git/hooks` 失敗、讀假的 `~/.ssh` 失敗、`git commit` / `git stash` 成功、`curl` 失敗、逾時後背景子程序被 kill。
**Status**: Complete（macOS 上 `make test-sandbox` 通過；手動 smoke：`touch ~/x`、`cat ~/.ssh/config`、寫 `.git/hooks`、`curl` 都被擋，`go test`、python3、node 正常）

## Stage S2: Linux bubblewrap
**Goal**: bwrap 實作同一個 Policy（`--ro-bind / /`、WritableRoots `--bind`、deny-read 以 tmpfs / 空檔蓋掉、`--unshare-net/pid`、`--die-with-parent`、`--new-session`）；偵測 bwrap 與 user namespace，不可用時明確報錯。
**Success Criteria**: `make test-sandbox-linux` 通過（root 與非 root）；沒有 bwrap 時回明確錯誤。
**Tests**: bwrap 參數產生；和 S1 相同的整合測試案例。
**Status**: Complete（`make test-sandbox-linux` 通過：root、非 root、移除 bwrap 後明確報錯）。差異：bwrap 下被 deny-read 的目錄顯示為空目錄（tmpfs），Seatbelt 則直接拒絕；不存在的 deny-write 路徑（例如沒有 `.git/hooks`）在 bwrap 下無法預先擋住。

## Stage S3: 設定、模式與檔案工具的一致性
**Goal**: `sandbox.mode`、`sandbox.writable_roots`、`sandbox.deny_read`、`sandbox.on_unavailable` 設定與 `-sandbox` flag；read-only 模式下檔案寫入工具也拒絕；`NewBackend` 組出 policy；更新 `--help` 與 README。
**Success Criteria**: `make check`、`make test-sandbox`、`make test-sandbox-linux` 通過；verify gate 在 workspace-write 下可正常 stash / reset。
**Tests**: config 解析；模式到 policy 的對應；read-only 檔案工具錯誤；sandbox 內的 git stash / reset。
**Status**: Complete（`make check`、`make test-sandbox`、`make test-sandbox-linux` 通過；`TestSandboxVerifyGateBaseline` 在兩個平台的 sandbox 內跑 HEAD baseline）。只有 `~/.quill/config.toml` 能設定 `[sandbox]`；檔案工具由 `sandboxFileGuard` 擋（filesystem 的 permissions 不能和可執行的 backend 併用）：read-only 拒絕所有寫入工具；workspace-write 下檔案工具同樣不可寫 `.git/hooks`、`.git/config`。

## Stage S4: 和核准機制連動
**Goal**: sandbox 啟用時 `execute`、`run_tests`、`diagnostics` 免核准（破壞性指令仍一律核准）；`execute` 的 `sandbox: false` 逃生口一律核准、headless 自動核准時拒絕、可用 `sandbox.allow_unsandboxed = false` 關掉；被 sandbox 擋下時附提示；trace 記錄 sandbox 狀態。
**Success Criteria**: `make check` 通過。
**Tests**: HITL 設定矩陣；逃生口在 manual / headless 下的行為；擋下提示（含不誤判）。
**Status**: Complete（`make check`、`make test-sandbox`、`make test-sandbox-linux` 通過）。沙箱開啟時 `execute`/`run_tests`/`diagnostics` 自動放行，破壞性指令與 `sandbox: false` 逃生口一律核准；`-n` 無人核准時拒絕逃生口；`allow_unsandboxed = false` 關掉逃生口。被擋判斷用輸出關鍵字（EPERM、read-only file system、DNS 失敗等），屬啟發式；`ExecuteArtifact` 記錄 `sandbox`、`sandbox_blocked`。

## Stage S5: 網路 allowlist、驗證、預設開啟
**Goal**: quill 行程內的 HTTP CONNECT proxy 依 domain allowlist 放行並擋內網；Seatbelt 只允許連 proxy port；bwrap 用 unix socket + `quill __netbridge` 轉接；`sandbox.network`、`sandbox.allowed_domains`；驗證後把預設改成 workspace-write。
**Success Criteria**: 兩個平台的整合測試通過（allowlist 內的 domain 成功、其他失敗）；手動以 `quill sandbox --` 跑 Go / Node / Python 常見指令。
**Tests**: proxy 允許 / 拒絕 / 內網 IP；兩平台網路整合測試。
**Status**: Not Started

## Backlog

- Linux Landlock + seccomp（不依賴 bwrap 的 fallback）
- Windows（restricted token / AppContainer）
- subagent 使用不同 policy
- `ExecuteWithOffload`、credential broker
