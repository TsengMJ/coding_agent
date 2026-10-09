# Implementation Plan

分階段建構 Quill。每完成一個 Stage，就同步更新本檔的 Status。已完成的 Stage 1–23、B1–B4、R0–R10、RL1–RL7、D1–D5 移到 [docs/history/implementation_plan_v1.md](docs/history/implementation_plan_v1.md)。

## 尚未結束的評測

### Stage B5: 第一輪基準線
**Goal**: 用官方 harness 研究中的開源模型跑 smoke50，和 mini-swe-agent / Pi / OpenCode 同題同模型比較，產出下一步優化清單。
**Status**: Not Started

### Stage RL8: 評測與 ablation
**Goal**: quillbench 記錄修復迴圈每個元件的觸發次數，用 `-disable` 比較開與關。
**Success Criteria**: 解題率不下降；步數與成本增加不超過 20%；觸發次數與效果寫回這裡。刪除元件前先由使用者看數據決定。
**Tests**: `TestTriggersCountsRepairLoopNotes`、`TestAnalyzeCountsRecordedReviews`、`TestReviewVerdictCountsBlockingFindings`。
**Status**: In Progress（第一輪只跑 2 題，等使用者看數據）

**Results**:
- `quillbench analyze` 新增「Repair loop」表：`repair` / `rollback` 呼叫、`[repair]` 的 delta 與失敗分類、micro 還原 / 編譯錯誤、repro、widen、stuck、checkpoint、reviewer 結果（`Process.Reviews`，經 `OnReview` 記錄）。
- 子集 `code/bench/subsets/hard-go.txt`（smoke50 中兩題 HARD Go），commit `2381a6b`，`openrouter:openai/gpt-6-luna-pro`，各 1 次：
  - A `20261007-151258-quill-hard-go`：修復迴圈全開。
  - B `20261007-161048-quill-hard-go`：`-disable failure_class,repair_state,micro,repro,planner,stuck,checkpoint,reviewer`。

  | 題 | 解出 A / B | 步數 A / B | input tokens (M) | cache % | peak ctx (k) | $ | 分鐘 |
  |---|---|---|---|---|---|---|---|
  | flipt-c188 | 0 / 0（都是 F2P 5/6，`TestECRCredential` MISSING） | 83 / 47 | 19.8 / 5.5 | 33 / 84 | 553 / 307 | 1.52 / 0.17 | 47 / 18 |
  | vuls-ca3f | 1 / 1 | 109 / 59 | 27.8 / 7.2 | 37 / 81 | 536 / 240 | 1.96 / 0.24 | 50 / 25 |

- 解題率相同；成本約 8.5 倍、步數約 1.8 倍，遠超過 20% 的標準。
- 主因是 cache：A 從第 5 個請求起 cache 命中率就停在約 35%，B 維持 80–95%。trace 中相鄰請求的訊息前綴相同（只有最後的 `<repair-state>` 被換掉）。
- Cache 原因（重播 trace 中的請求驗證，共約 $1）：
  - OpenAI（GPT-5.6 之後）把 cache 寫在最後一則 user 訊息的結尾（[Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)）。尾端的 user `<repair-state>` 每次換掉，下一次就對不上任何寫過的斷點，每次都只拿到冷請求的約 1/3。
  - 尾端 user 訊息還讓前面的工具呼叫歷史重新渲染：同一個請求多約 17k input tokens（short user 訊息也一樣，short system 訊息只多約 100）。這點文件沒有說明。
  - flipt 第 9–14 次請求重播：原樣 42–43%；拿掉 `<repair-state>` 82–88%；改成 system 角色 86–87%；固定上游或加 `prompt_cache_key` 都沒有改善。
  - 修正：`requestTail` 讓 `<repair-state>`、`<project-context>`（互動模式的 git status）、預算提醒都改成尾端 system 訊息。
- A 的觸發次數（2 題合計）：`repair` 工具 12、delta fixed 1 / unchanged 9、micro 編譯錯誤 21 / 還原 0、repro 1、widen 1、stuck 提醒 5 / 擋下 0、checkpoint 15 / rollback 0、reviewer 1 次（vuls 用滿 50 分鐘，reviewer 在期限到時逾時）。
- 發現的 RL6 問題（已修）：
  - 同一個回應平行跑兩個檢查時，兩者看到同樣的歷史，算出同一個 `cpN`，後者覆蓋前者的 ref（vuls 有 cp3、cp5、cp7、cp9、cp11、cp13 各兩個 commit）。
    - 修正：編號改從既有的 ref 取下一個，`git update-ref <ref> <commit> ""` 只在 ref 不存在時建立，搶輸的重取；工作區和最新 checkpoint 的 tree 相同時不存也不附註，所以平行的檢查只存一次。
  - 檢查前後都通過（`passed → passed`，delta `unchanged`）也算進步而存檔，所以 15 個 checkpoint 大多是 `attempt N: unchanged`。
    - 修正：checkpoint 改用 `improved`：delta 是 `fixed` / `progressed`，或沒有可比較的前一次而且通過。`madeProgress` 仍給 stuck 用（通過不算卡住）。
  - 測試：`TestSaveCheckpointNumbersAfterTheLatestRef`、`TestSaveCheckpointSkipsAnUnchangedWorkspace`、`TestCheckpointManagerSavesParallelChecksOnce`、`TestCheckpointManagerSkipsAPassingRerun`。

### Stage RL8b: 環境錯誤的回饋
**Goal**: 檢查因程式碼之外的原因失敗時（下載 module 時網路不通、缺 go.sum、服務沒啟動、缺工具），失敗分類、micro、卡住偵測、`<repair-state>` 和驗證關卡都認得出來，並告訴 agent 能做什麼，而不是叫它改程式碼。以日常開發為準（quill 的沙箱預設 `network = "none"`），bench 只用來量效果。
**Success Criteria**:
- 只依工具輸出裡的證據判斷，沒有 bench 專用開關；建議先列能修的做法，「修不了就說明哪些沒驗證」放最後。
- 驗證關卡仍然最多擋 2 次；環境錯誤仍然計入卡住偵測，只是訊息不同。
- hard-go 重跑 A：`micro_failed` 不再是環境錯誤，stuck 不再叫 agent 換程式碼假設，步數與成本比 RL8 的 A 低。
**Tests**: 見各 stage。
**Status**: In Progress（程式碼完成、bench 跑完一次，等使用者看數據）

- E1 分類器（`a7a1d20`、`851d884`）：
  - `envOfMessage` 判斷單一錯誤訊息；`classifyRunTests` 的 build_errors 和 `classifyDiagnostics` 的 error 每一筆先過它。全部是環境錯誤時判為 environment 並帶 `Env`，有程式碼錯誤時判為 compile / syntax，`Details` 只放程式碼錯誤。
  - 網路規則補上 `network is unreachable` 和 Go 下載的 `Get "<url>": <reason>`（DNS、連線被拒、proxy 回 EOF 都是這個格式；module 不存在是 `reading <url>: 404`，不算）。
  - `missing go.sum entry ...` 判為 `dependency:<pkg>`，建議 `go mod tidy`。`no required module provides package` 不加規則：flipt 裡同一句話是專案自己還沒建立的套件，沒讀 `go.mod` 分不出來。
  - `environmentAdvice` 依 `Env` 分開寫：network 說明改程式碼修不好，列出恢復存取的方法（沙箱設定、問使用者）或改跑不需下載的檢查，最後才是回報；dependency 要求 `go mod tidy`；其他沿用「啟動服務、安裝依賴」。
  - 測試：`TestClassifyFailure` 新增 8 列、`TestClassifyFailureKeepsOnlyCodeErrorsBesideEnvironmentOnes`、`TestFailureAdviceDependsOnTheEnvironmentProblem`。
- E2 micro（`36d67aa`）：`diagnosticsNote` 把 build 錯誤分成程式碼與環境；環境錯誤是 `status: environment` 加 `env` 與建議，同一個回合同一個 `env` 只提醒一次；混合時只列程式碼錯誤並點出環境問題。quillbench 新增 `micro_environment` 欄。測試：`TestMicroCheckReportsAnEnvironmentErrorOncePerTurn`（真的 go，缺 go.sum）、`TestDiagnosticsNoteSeparatesCodeAndEnvironmentErrors`。
- E3 卡住偵測與 `<repair-state>`（`d65e016`）：環境類 attempt 不計入 `same_failure` / `no_progress` / `same_patch`；連續 3 次環境錯誤回報新規則 `environment`，要求修環境或問使用者，不擋編輯（修環境可能要改 `go.mod`）；`<repair-state>` 的 next 改成點出環境問題。測試：`TestDetectStuckEnvironment`、`TestDetectStuckLeavesEnvironmentAttemptsOutOfTheCodeRules`、`TestDetectStuckSamePatchIgnoresEnvironmentChecks`、`TestStuckRefusalLetsEditsThroughAfterAnEnvironmentNote`、`TestDeriveRepairPhases` 新增一列。
- E4 驗證關卡（`ba4722e`）：最後一次檢查是環境錯誤時，訊息是「could not run because of the environment (Env)」加上同樣的建議，沒有「Fix the cause」。測試：`TestVerifyGateNamesTheEnvironmentProblem`、`TestVerifyGateAsksToFixACodeFailure`。
- E5 e2e：`TestAgentGetsEnvironmentAdviceInsteadOfFixingCode`（headless，缺 go.sum 拿到 `go mod tidy` 建議、micro 只提醒一次、離線的 `go test` 判為 network、關卡訊息點出環境問題）。
- Bench A2 `20261008-091748-quill-hard-go`（修復迴圈全開，含 cache 尾端修正 `c0d205a`、checkpoint 修正 `577715c` 與本 stage），各 1 次：

  | 題 | 解出 A / B / A2 | 步數 A / B / A2 | input tokens (M) | cache % | peak ctx (k) | $ | 分鐘 |
  |---|---|---|---|---|---|---|---|
  | flipt-c188 | 0 / 0 / 0 | 83 / 47 / 43 | 19.8 / 5.5 / 4.8 | 33 / 84 / 84 | 553 / 307 / 293 | 1.52 / 0.17 / 0.15 | 47 / 18 / 19 |
  | vuls-ca3f | 1 / 1 / 1 | 109 / 59 / 113 | 27.8 / 7.2 / 18.7 | 37 / 81 / 90 | 536 / 240 / 420 | 1.96 / 0.24 / 0.44 | 50 / 25 / 42 |

  - 成本下降大部分來自 cache 修正（命中率 33–37% → 84–90%），步數和 peak context 才反映行為的改變。
  - flipt：micro 3 次（2 次是真的要處理的程式碼問題：專案內還沒建立的套件、還沒加進 go.mod 的外部套件；1 次 environment/network），檢查判為 environment，關卡兩次都寫「could not run because of the environment (network)」，agent 第 40 步就說明網路不通並結束（RL8 的 A 是 83 步）。步數、成本都和 B 相當。
  - vuls：步數沒有下降。編輯 57 次、平均分布在整段（A 是 32 次），沒有環境錯誤引發的反覆修改；後段的檢查因為任務要求的 go.mod 升級（goval-dictionary v0.7.4 不在離線 cache）判為 dependency。單次樣本，差異可能是軌跡本身的變異。
  - 發現的問題（未修，等使用者決定）：
    - `same_failure` 會把連續 3 次「通過 → 通過」（delta `unchanged`）當成卡住：vuls 第 94 步收到 `the last 3 attempts all left the same failure: passed`。和 RL6 checkpoint 的 `passed → passed` 是同一類問題。
    - 缺 go.sum 的建議是 `go mod tidy`，但離線時它也會失敗；vuls 的 agent 自己判斷出來了，最後一步才試 `go mod download`。
  - Success Criteria：解題率不變；flipt 的步數與成本在 B 的 20% 內，vuls 沒有（步數 +92%、成本 +83%）。

## 遠端 agent 服務（S1–S6）

目標：quill 從「單機單人、本地 `quill serve`」改成「小團隊共用的遠端 agent 服務 + 本地桌面前端」。每個 thread 在 server 端 clone repo 到獨立容器執行，以 GitHub 登入，成果用 PR 交付。

預設決策：
- 程式碼來源：server 端 `git clone`，只支援 GitHub（OAuth 同時用於登入與 push）。
- 規模：單一實例、< 20 人，不做水平擴展與多租戶。
- 容器映像：repo 內 `.quill/Dockerfile` > `.devcontainer/devcontainer.json` 的 `image` > 預設映像。
- 網路：容器預設可上網（裝依賴需要）。
- 生命週期：閒置 30 分鐘停容器、保留 workspace；14 天未使用刪除 workspace；每人最多 3 個執行中容器。
- 核准：容器內工具自動核准；push、開 PR 要核准。

### Stage S1: Workspace 容器
**Goal**: thread 綁定 workspace（repo + branch + 容器），不再綁 server 的 `cwd`；指令只在容器內執行。
**Success Criteria**: `make agent-check` 通過；同 repo 兩個 workspace 互不可見；容器內看不到 server 的環境變數；server 重啟後 workspace 狀態從 Postgres 讀回、容器可重新啟動。
**Tests**: fake docker 的 Manager 單元測試（建立、啟動、停止、回收、配額、映像選擇）；真 Docker 整合測試（`QUILL_DOCKER_IT=1`）；service 端到端（FakeModel 在 workspace 中寫檔並經 docker exec 執行）。
**Status**: Complete

**Results**:
- `code/workspace`：`Manager`（Create / Acquire / Stop / Delete / Reap / Reconcile / Run）、`Store`（memory、Postgres `quill_workspaces`）、`Docker` 介面與 `CLIDocker`。
  - Create 在主機 `git clone`（token 經 `GIT_CONFIG_*` 環境變數送出，不在命令列也不寫進 clone 的設定），映像依 `.quill/Dockerfile`（build 成 `quill-ws-<id>`）> devcontainer `image` > `-workspace-image`。
  - Acquire 在需要時建立或啟動容器（`--init`、`sleep infinity`、clone 掛在 `/workspace`），設定 `safe.directory` 並刷新 git index；使用中的 workspace 不能 stop / delete，reaper 也跳過。
  - repo_url 只接受無帳密的 `https://`；`-workspace-local-repos` 才接受主機路徑。
- Service：`RunRequest.WorkspaceID`；thread 綁定 workspace（換 workspace 或改用目錄都回 409）；`Config.RequireWorkspace` 讓遠端模式拒絕在 server 目錄執行；檔案工具走主機上的 clone、指令走 `docker exec`。
- HTTP：`POST/GET /workspaces`、`GET/DELETE /workspaces/{id}`、`POST /workspaces/{id}/stop`；回應不含主機路徑。
- `quill serve -workspaces DIR`（`$QUILL_WORKSPACES_ROOT`）與 `-workspace-image / -idle / -retention / -max-running / -cpus / -memory-mb / -network / -local-repos`。
- `quill_threads` 加上 `workspace_id`、`owner` 欄位（owner 在 S2 填入）。
- 偏離計畫：workspace 裡沒有 `start_process`。`DockerExecBackend` 不是 `process.Preparer`：停止 host 上的 `docker exec` client 不會殺掉容器內的行程，需要另外的容器內行程管理，留到 S5（瀏覽器搬進容器時一起做）。

### Stage S2: 身分、權限、遠端連線
**Goal**: 非 loopback 也能安全提供服務；每個 thread 有擁有者。
**Success Criteria**: `make agent-check` 通過；`-public` 模式可綁任意位址；未登入 401、他人的 thread 403；稽核紀錄寫入；超過每日額度拒絕新 run。
**Tests**: GitHub device flow（httptest 假 GitHub）、session token 建立 / 過期、授權中介層、audit、額度。
**Status**: Complete

**Results**:
- `code/auth`：GitHub device flow（`POST /auth/device`、`POST /auth/token`（等待中 428、過期 410、拒絕或不在名單 403）、`POST /auth/logout`、`GET /auth/me`）；只有 `QUILL_ALLOWED_USERS` 名單內的 GitHub 帳號能登入。session token 只存 SHA-256，GitHub token 以 `QUILL_SECRET_KEY`（AES-256-GCM）加密存 Postgres（`quill_users`、`quill_sessions`），預設 30 天過期。
- `code/audit`：`quill_audit` 表；記錄 login / logout / login_refused、run（含該次 run 增加的模型花費）、decision（核准哪些工具）、workspace_create / workspace_delete。`GET /audit` 只回自己的紀錄。
- 擁有者：登入的使用者只能看、跑、改名、壓縮、追蹤、取消自己的 thread，`GET /threads` 只列自己的；本機無擁有者的 thread 對登入使用者也是 403。未登入（本機模式）行為不變。
- 額度：`-daily-limit-usd`（`$QUILL_DAILY_LIMIT_USD`）以 audit 的當日（UTC）run 花費加總判斷，超過回 429。單次 run 不會中途被切斷，所以最多超出一個 run 的花費。
- `quill serve -public`：可綁任意位址、必須搭配 `-workspaces`（並強制 RequireWorkspace，不能加 `-workspace-local-repos`）、所有 API 需要 `Authorization: Bearer`、`GET /healthz` 與 `/auth/*` 免登入、瀏覽器 Origin 只接受 `-allowed-origins`（並回 CORS）。TLS 交給 S3 的反向代理。

### Stage S3: 部署與維運
**Goal**: 一份 docker-compose 在一台機器上跑起整套服務。
**Success Criteria**: `make agent-check` 通過；migration 從空庫與舊 schema 都能升級；`/readyz` 反映依賴狀態；`/metrics` 輸出 Prometheus 格式；兩個 Service 實例搶同一 thread 只有一個成功。
**Tests**: migration、readyz、metrics、advisory lock（需 `POSTGRES_CONN_STRING`）。
**Status**: Complete

**Results**:
- `migrate` 套件：`migrate/sql/NNNN_name.sql` 內嵌進執行檔，`Up` 在 advisory lock 下逐一以交易套用並記在 `quill_schema_migrations`；資料庫版本比執行檔新時拒絕啟動。`0001_baseline` 收齊原本散在各 store 的 `CREATE TABLE IF NOT EXISTS`（含 langgraphgo 的 `quill_checkpoints`），全部可重入，所以舊資料庫也經由它升級。各 store 建構子不再建表，`storage.Open` 啟動時跑 migration。`bench_*` 表屬於 quillbench 開發工具，維持原樣。
- Thread 鎖：`storage.ThreadLocks`；Postgres 版是 session advisory lock，每把鎖佔一條連線（獨立的 64 連線 pool，長時間 run 不會吃掉主 pool），行程掛掉時 Postgres 斷線即釋放。`Service.acquire` 先查行程內 map（`Running` 旗標沿用），再拿 store 鎖；另一個行程持有時回 `ErrThreadBusy`（409）。
- `GET /readyz`：Postgres、Redis、（有 workspace 時）Docker daemon 逐項檢查，任何一項失敗回 503；回應只有項目名稱與 ok / unavailable，錯誤細節寫 log。
- `-public` 模式：slog JSON log（`-log-format`）、每個請求一行 access log（route 用 mux pattern，不含 thread ID）、`-metrics-addr` 在獨立位址提供 Prometheus `GET /metrics`（請求數、run 數依結果、run 時間、進行中 run、模型花費）。沒有引入 Prometheus client 套件。
- 部署：`deploy/Dockerfile`（quill + git + docker CLI）、`deploy/workspace.Dockerfile`（預設 `quill-workspace:latest`：Go、Node、Python、build-essential、ripgrep）、`deploy/compose.yaml`（quill、Postgres 17、Redis 7、Caddy 自動 TLS）、`deploy/Caddyfile`（NDJSON 串流 `flush_interval -1`）、`deploy/.env.example`。本機實測：兩個映像都能 build，compose 起來後 readyz 三項 ok、未登入 401、metrics 正常、migration 1 已記錄。
- 實測發現並修正：容器內沒有 bubblewrap 時 `-public` 無法啟動。workspace 模式指令都在容器內，`-public` 改把 host sandbox 設為 unavailable 時只警告（`.git/hooks`、`.git/config` 的檔案保護仍生效）。

### Stage S4: 用 PR 交付成果
**Goal**: agent 完成後開 PR，作者是使用者本人。
**Success Criteria**: `make agent-check` 通過；`create_pr` 需核准；`GET /threads/{id}/diff` 回傳含未追蹤檔的完整差異。
**Tests**: httptest 假 GitHub API（建 PR、權限不足、PR 已存在）；暫存 git repo 的 commit / push 到 bare remote；diff 含未追蹤檔。
**Status**: Complete

**Results**:
- `code/delivery` 套件：`WorkingDiff`（用暫存 index 算出含未追蹤檔、改名、刪除、二進位的差異，不動使用者的 index；patch 超過 2 MiB 截斷並標示）、`CommitAll`（切到 `quill/<thread>`、作者是 GitHub 使用者的 noreply 信箱、停用 hooks 與簽章）、`Push`、`CreatePullRequest`（PR 已存在時回傳既有 PR；401/403/404 視為權限不足）。
- 安全設計：workspace 的 `.git/config` 由 agent 控制，所以 host 從不對 workspace repo 跑 git。diff 與 commit 透過 `docker exec` 在容器內執行；push 時容器把 commit 寫成 `.quill/delivery.bundle`，host 以 `O_NOFOLLOW` 讀出（拒絕 symlink），在乾淨的暫存 bare repo（不讀系統 / 全域 git 設定）裡 fetch base 與 bundle、核對 head SHA 後 push；GitHub token 只經由 `http.extraHeader` 環境變數傳給 host 端的 git，從不進容器。
- API：`GET /threads/{id}/diff`（workspace 對 upstream base 的 merge-base；本地 thread 對 HEAD）是 GUI「Files changed」面板的資料來源；`POST /threads/{id}/pr` 以 thread 鎖保護，成功後寫 `pull_request` audit。
- `create_pr` 工具只在 workspace thread 提供，透過新的 `agent.Options.ExtraInterrupts` 設 `Always: true`，即使 auto-approve 也一定停下等使用者核准；核准提示顯示 PR 標題。

### Stage S5: 桌面薄 client、遠端瀏覽器、發佈
**Goal**: 使用者只裝桌面 app 就能連到遠端服務。
**Success Criteria**: `make check` 通過；desktop 可設定 server URL、以 device flow 登入、token 存系統金鑰圈；瀏覽器 endpoint 可指向 workspace 容器；electron-builder 設定與 GitHub Actions workflow 存在且 lint 通過。
**Tests**: 遠端連線設定與 token 注入、登入流程、browser endpoint 驗證。
**Status**: Complete

**Results**:
- 桌面連線（`src/main/connection.ts`）：server URL 只接受 https（loopback 可用 http）；GitHub device flow（`/auth/device` → 開瀏覽器 → 輪詢 `/auth/token`，428 等待、`slow` 加 5 秒、410 過期、403 拒絕）；session token 以 Electron `safeStorage`（macOS Keychain）加密存在 userData，依 URL 分開。遠端請求帶 `Authorization: Bearer`，401 時忘掉 session 並回到連線面板；本機模式維持 `X-Quill-Token`。
- Renderer：連線面板、workspace 選擇器（repo URL + branch，新 thread 必須先選）、「Files changed」面板（`/diff` + 開 PR 表單）、遠端瀏覽器面板。重連沿用既有的 thread 事件重播。
- 容器內瀏覽器：workspace 映像改用 Node 24（Playwright MCP 需要 Node 20+）並加 Chromium；server 用 `docker exec -i <container> npx @playwright/mcp --headless --isolated` 啟動 MCP，每個 workspace 一個，容器停止或刪除時關掉。CDP 不離開容器，所以沒有做 CDP proxy。
- 和計畫的差異：screencast 改成面板每秒輪詢 `GET /threads/{id}/browser/screenshot`（jpeg），點擊經 `POST /threads/{id}/browser/click` 轉成 `browser_mouse_click_xy`；比 CDP screencast 簡單，對少量使用者夠用。
- `start_process` 在 workspace thread 改在容器內執行（`workspace.ProcessShell`，PID 檔 + `/proc` 子行程樹），`stop_process` 會殺掉容器內整棵行程樹（真 Docker 驗證）。
- 發佈：electron-builder（mac dmg/zip，hardened runtime + notarize；linux AppImage；win nsis；publish 到 GitHub Releases）、`electron-updater` 自動更新（只在打包後）。`.github/workflows/ci.yml`（PR：Postgres + Redis service、`make agent-check`、`-race`、`make desktop-check`），`release.yml`（`v*` tag：推 quill / quill-workspace 映像到 GHCR，三平台 installer）。actionlint 通過；本機 unsigned 打包與啟動成功。
- 尚未驗證：GitHub runner 上的 bubblewrap 行為、簽章與 notarize（需要 `CSC_LINK`、`APPLE_ID` 等 secrets）。
- 測試：`connection.test.ts`、`protocol.test.ts`（Bearer、401、本機 token）、`remote.test.ts`、`TestStopAlsoStopsTheRemoteSideOfAStopper`、`TestOnStopHearsAboutStoppedAndDeletedWorkspaces`、`TestInteractiveWorkspaceRunsStartProcessesInTheContainer`、`wsbrowser_test.go`、`TestContainerBrowser`（`QUILL_DOCKER_IT=1`，真容器內 navigate / screenshot / click 通過）。

### Stage S6: 上線品質門檻
**Goal**: 修已知 bug、決定修復迴圈預設、補 Anthropic provider、定義發版評測門檻。
**Success Criteria**: `make agent-check` 通過；「通過 → 通過」不再觸發 `same_failure`；Anthropic provider 通過 httptest（文字、tool call、串流、錯誤）。
**Tests**: `detectStuck` 新增案例；Anthropic httptest。
**Status**: Not Started
