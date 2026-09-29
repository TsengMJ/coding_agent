# Implementation Plan

分階段把 Python deepagents 移植成 Go。每完成一個 Stage，就同步更新本檔的 Status 與 [`TODO.md`](TODO.md) 的勾選狀態。

## Stage 1: 框架與介面

**Goal**: 定義核心 SDK 與 CLI 的套件分層、介面、型別與 stub，不寫業務邏輯。
**Success Criteria**: `go build ./...`、`go vet ./...`、`gofmt -l .` 皆無輸出；`go test ./...` 通過；TODO.md 列出所有 Python 功能並標記狀態。
**Tests**: 介面符合性（compile-time assertions）、stub 回傳 `notimpl.ErrNotImplemented`、slash command catalog 數量與唯一性。
**Status**: Complete

## Stage 2: 先把 graph 走通（FakeModel 迴圈 + OpenAI + make run）

**Goal**: `agent.CreateAgent` 跑通「model → tool → model」迴圈；`CreateDeepAgent` 直通到 `CreateAgent`；接上 OpenAI Chat Completions；`make run PROMPT=...` 印出模型回答。預設 middleware stack、checkpointer、streaming、interrupt、structured output、並行 tool calls 都不在這一步。
**Success Criteria**: `make check` 通過；`go test ./agent -run Loop -v` 看到多輪 tool call 測試通過；有 `OPENAI_API_KEY` 時 `make run PROMPT='hello'` 印出回答並回 0；沒有 key 時印出缺少 `OPENAI_API_KEY` 的錯誤並回非 0。
**Tests**:
- `MessagesReducer`：append、依 ID 取代、`RemoveAllMessages`；`State.Apply` 搭配自訂 reducer；`State.Clone`
- FakeModel、`ParseModelSpec`、`InitChatModel` / `RegisterProvider`
- FakeModel 迴圈：無 tool call、兩輪 tool call、未知工具、工具錯誤、recursion limit、WrapModelCall 巢狀順序與 BeforeAgent / AfterModel 順序、middleware 名稱重複
- `CreateDeepAgent`：FakeModel + `tool.Func` 跑通一輪；`WithSubAgents` 回 `ErrNotImplemented`
- OpenAI（httptest）：請求 JSON 形狀、純文字回應、tool_calls 回應、401 錯誤、缺少 API key
- CLI：`DefaultPaths`、in-process server、`RunNonInteractive` 搭配 fake server
**Status**: Complete（執行層已由 Stage 3 改接 langgraphgo / langchaingo）

## Stage 3: 改接 langgraphgo / langchaingo

**Goal**: agent graph 跑在 langgraphgo 上，thread 續跑用它的 in-memory checkpointer，OpenAI Chat Completions 走 langchaingo。不再自寫 agent loop、MessagesReducer 合併、MemoryCheckpointer 或 OpenAI HTTP client。
**Success Criteria**: `make check` 通過；FakeModel 多輪 tool call、recursion limit、middleware 順序仍通過；同一 thread 第二次 `Invoke` 看得到第一次的 messages，且 checkpoint 存在 langgraphgo 的 `MemoryCheckpointStore`；`CreateAgent` 接受 Checkpointer；OpenAI httptest 仍打到 `WithBaseURL`。
**Tests**:
- `MessagesReducer` 經 `graph.AddMessages`：append、依 ID 取代、`RemoveAllMessages`
- `MemoryCheckpointer` Put / Latest / List / DeleteThread（不同 thread 互不影響）
- checkpoint 後以同 thread 續跑，驗證 messages 累積
- `InterruptBefore` tools 後以 `Command` resume
- `Stream` 至少送出一個 values 事件
- OpenAI（httptest）：請求 JSON 形狀、純文字回應、tool_calls 回應、401 錯誤
**Status**: Complete

## Stage 4: Postgres checkpoint（crash recovery）

**Goal**: thread 的 execution state 與 pending human interrupt 寫進 PostgreSQL，行程重啟後仍能 `Latest` 續跑。Redis 不在這一步。
**Success Criteria**: `make check` 通過；checkpoint JSON round-trip 還原 messages、`Steps`、`ResumeFrom`、`PendingInterrupts`；有 `POSTGRES_CONN_STRING` 時兩個 checkpointer 實例對同一 thread 做 Put / Latest / List / DeleteThread，新實例看得到 pending interrupt。沒設環境變數時整合測試 skip。
**Tests**:
- `Checkpoint` JSON（經 `map[string]any`）還原 messages、`Steps`、`ResumeFrom`、`PendingInterrupts`
- `MemoryCheckpointer` Put / Latest / List / DeleteThread 仍通過
- `PostgresCheckpointer` 跨實例讀寫與刪除（`POSTGRES_CONN_STRING`）
**Status**: Complete

## Stage 5: 讓 dcode 真的能寫 code

**Goal**: `backend/utils`、`FilesystemBackend`（virtual mode 鎖在 cwd）、`LocalShellBackend.Execute` 完成；FilesystemMiddleware 的 ls / read_file / write_file / edit_file / glob / grep / execute 可用；`CreateDeepAgent(WithBackend)` 與 CLI 接上，`dcode -n` 能在 cwd 內寫檔並執行指令。StateBackend、permissions、HITL、eviction 不在這一步。
**Success Criteria**: `make check` 通過；FakeModel agent 在 temp dir 完成 write → read → edit → execute；virtual mode 擋下 `..` 與 symlink 逃逸；真模型 smoke：空目錄下 `dcode -n "建立 main.go ... go run"` 產生檔案並回報輸出。
**Tests**:
- `backend/utils`：`ValidatePath`、`PerformStringReplacement`、`SliceReadResponse`、`FormatGrepMatches`、`CompileGrepIncludeGlob` 等（table-driven）
- FilesystemBackend 在 temp dir 上的 ls / read / write / edit / delete / grep / glob、路徑與 symlink 逃逸
- LocalShellBackend exit code / stderr / 逾時 / 截斷
- FilesystemMiddleware：預設工具清單、非 sandbox backend 沒有 execute、FakeModel 端到端、錯誤轉成 error tool message、system prompt 附加
- `CreateDeepAgent(WithBackend)` 暴露檔案工具；server 以 FakeModel 在 temp dir 寫檔並執行；client 在 stderr 列出 tool calls
**Status**: Complete

## Stage 5.5: StateBackend 與 permissions

**Goal**: `StateBackend`（檔案存在 agent state 的 `files`）與 filesystem permissions 規則。
**Success Criteria**: 沒給 backend 時 CreateDeepAgent 預設用 StateBackend；deny 規則讓工具回傳 permission denied。
**Tests**:
- StateBackend 讀寫經由 RunContext 產生 `files` update；files reducer 以 nil 代表刪除
- 權限規則 deny 時工具回傳 permission denied，ls / glob / grep 結果被過濾
**Status**: Not Started

## Stage 6: dcode serve + TUI（邊寫邊測）

**Goal**: 常駐 `dcode serve`（HTTP、NDJSON 串流）、thread 與 sessions 列表存 Postgres（無連線字串時 in-memory）、token 串流、HITL 核准（write_file / edit_file / execute 預設需核准，`-y` 自動核准）、Bubble Tea TUI 與 line REPL。
**Success Criteria**: `make check` 通過；Postgres 模式下重啟 serve 後 thread、sessions 與待核准的 tool call 都還在；真模型 smoke：TUI 看到串流字、寫檔前跳出核准框、拒絕理由送回模型；`dcode -r` 接續同目錄最近的 thread。
**子階段**:
- 6a `server.Service`：依 cwd 快取 agent、thread 鎖、thread 綁定 cwd、只送出本次新增的訊息；`sessions.Store`（`dcode_threads` 表 / memory）
- 6b HTTP（`/healthz`、`POST /runs`、`GET /threads`、`GET /threads/{id}`）、`client.Remote`、`dcode serve`、`-server` / `DCODE_SERVER`、`-thread`、`-r`、`dcode threads`
- 6c token 串流：OpenAI / OpenRouter / FakeModel `Stream`；SSE 經 tee 取文字 delta，tool call 依 index 組回
- 6d HITL：loop 拆出 `after_model` 節點、`agent.Interrupt`（resume 值只用一次）、approve / reject、headless 的 y/N 提示
- 6e Bubble Tea TUI（串流、核准框、`/new` `/threads` `/resume` `/yolo` `/quit`、Ctrl-C 取消）與 line REPL（非 TTY 或 `-plain`）
**Tests**:
- Service：續跑帶上一輪訊息、只 yield 新訊息、busy / cwd 不符 / 相對路徑、兩個 cwd 互不影響、sessions 排序、Postgres 重啟後續跑與 resume 核准
- HTTP + Remote 端到端（寫檔、續跑、409 / 400、串流中途錯誤、client 取消時 server 端也取消）
- 串流：FakeModel 片段合起來等於全文、loop delta 事件順序、OpenAI SSE（文字 delta、tool call 依 index 組回、中途停止）
- HITL：approve 後 model 只被呼叫預期次數、reject 產生 error tool message 並回到 model、auto-approve、resume 值不重用、不允許的決策報錯
- client：y / a / N / 理由解析、headless 自動 resume、沒有 reviewer 時拒絕並附提示、REPL 兩輪同 thread 與 `/new`
- TUI：delta / message / interrupt 事件、核准與拒絕按鍵、slash 指令、thread picker、Ctrl-C 取消與離開
**Status**: Complete（edit decision 延到 Stage 8）

## Stage 6.5: 穩定性與 CLI 體驗

**Goal**: 長時間使用不崩、不卡、不漏資源；CLI 補上設定檔、shell allow-list 與腳本化旗標。
**Success Criteria**: `make check` 通過；長 thread 的 checkpoint 表不再無限成長；tool / node panic 不會打掛 serve；取消中的 tool call 下一輪會被補上結果；serve 收到 Ctrl-C 會讓進行中的 run 收尾；TUI 長對話不卡、顯示 markdown 與 diff。
**子階段**:
- 6.5a 穩定性：checkpoint 每 thread 保留最新 10 筆（`version` 欄存 seq，Postgres 用 SQL 取最新 / 裁剪）、panic recover（tool 變 error 訊息、node 變 `ErrPanic`）、PatchToolCalls（補取消的 tool call）、shell 程序群組與輸出上限、檔案讀取上限、LLM 重試（429 / 5xx / 連線錯誤）與串流閒置逾時、serve 兩階段關閉與寫入逾時
- 6.5b TUI：已完成訊息依寬度快取、捲上去時不被拉回底部、assistant 訊息用 glamour 渲染、核准框顯示 unified diff
- 6.5c CLI：`~/.deepagents/config.toml`（`models.default`、`startup.mode`、`shell.allow_list`、`retries.max_retries`、`server.url`；flag > env > 檔案 > 預設）、hitl `When` + shell allow-list（移植 Python 規則）、`-q` / `-no-stream` / `-timeout`（124 / 130）、`dcode threads -json`
**Tests**:
- checkpoint：memory / Postgres 只留最新 N 筆、刪除後 seq 重來、讀得到舊版（version=0）資料
- panic：tool panic 變 error tool message、node panic 回傳 `ErrPanic`；取消的 tool call 下一輪補上結果
- shell：背景子程序與逾時會殺整個程序群組、輸出截斷在 rune 邊界；大檔讀取被拒
- LLM：503 / 429 重試、超過上限放棄、400 不重試、`max_retries = 0`、header 等待逾時、串流閒置逾時
- serve：關閉時讓進行中的 run 完成、超過寬限期取消、client 不讀時 run 會停
- TUI：訊息每個寬度只渲染一次、捲上去時串流不跳動、markdown 渲染、核准框 diff
- config：缺檔用預設、讀取各欄位、env 覆寫檔案、錯誤帶行號、非法值被拒；allow-list：注入字元、管線、`&`、`all`
- CLI：`-q` 只輸出回覆、`-no-stream` 一次寫出、逾時 124、中斷 130、JSON envelope、旗標需搭配 `-n`
**Status**: Complete

## Stage 7: 讓 dcode 日常可用、SubAgents、工具平行執行

**Goal**: tool calls 平行執行並預設信任模型；write_todos、AGENTS.md memory、專案資訊；核心 summarization 與 `/compact`；SubAgent `task` 工具與 CreateDeepAgent 完整組裝（預設 middleware stack、profiles、general-purpose subagent）。
**Success Criteria**: `make check` 通過；同一輪多個 tool calls 同時執行、結果依 call 順序寫回；長對話自動摘要並 offload；AGENTS.md 注入 prompt；`CreateDeepAgent` 產生帶預設 middleware 的 agent，能委派任務給 subagent 並拿回結果。
**子階段**:
- 7a 平行 tool calls（state 快照、最多 8 個 goroutine、依原順序套用）、同一路徑平行異動保護、`startup.mode` 預設 yolo
- 7b `write_todos`（TUI checklist）、MemoryMiddleware（AGENTS.md）、system prompt 的 Project Context（git 分支 / 狀態、專案標記檔）
- 7c `ApproximateTokenCounter`、`ErrContextOverflow`、Summarization（觸發、參數截斷、cutoff、摘要、offload、overflow 重試）、`/compact`
- 7d `task` 工具（isolated）、profiles 合併 / `ApplyPrompt`、ToolExclusion、CreateDeepAgent 完整組裝與 general-purpose subagent
**Tests**:
- 平行：barrier 工具證明同時執行、Command update 依 call 順序、同一路徑第二個寫入被拒、`-race`
- todos / memory / local context / TUI checklist
- summarization 觸發、cutoff 不切斷 tool call 配對、state messages 不變、offload、overflow 重試、`/compact` 端到端
- middleware 組裝順序與同名取代、excluded middleware 驗證、prompt 組裝、`task` isolated 與平行呼叫
**Status**: Complete

## Stage 8: 進階 middleware

**Goal**: Skills 完成；HITL 補上 edit / respond 決策。
**Success Criteria**: SKILL.md 被注入 prompt；核准時可修改 tool 參數後再執行。
**Tests**:
- SKILL.md frontmatter 解析與驗證
- HITL edit 決策（取代 AI 訊息中的 tool call 參數）
**Status**: Not Started
