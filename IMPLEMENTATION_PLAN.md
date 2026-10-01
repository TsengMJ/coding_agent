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

併入 Stage 8（8a / 8b）。

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

## Stage 7e: TUI 顯示 subagent 活動與進行中狀態

**Goal**: TUI 能分辨 main 與各 subagent 的 LLM / tool 呼叫，平行的 subagent 有各自編號；執行中的東西有 spinner 與經過時間，不會看起來像卡住。
**Success Criteria**: 串流時 subagent 事件帶 `task:<tool call id>` namespace 傳到 client；TUI 在 task 列下巢狀顯示 subagent 的呼叫與報告摘要；main / subagent 等待模型時顯示 thinking 列；取消後沒有殘留 spinner；line 輸出不受影響。
**Tests**:
- loop：Stream 下 tool 拿得到 emitter；巢狀 Invoke 的模型文字不混進 parent 串流（`-race`）
- `task`：串流時轉發的事件帶正確 namespace，報告與 Invoke 路徑一致
- server：主 agent 與多個 namespace 交錯時各自增量輸出；line printer 忽略 subagent 事件
- TUI：平行 task 得到 #1 / #2 且列在各自 task 下、task 完成顯示 ✓ 與報告、端到端 subagent 執行、thinking 列、tick 只重繪進行中的 entry、取消與 resume 後無 spinner
**Status**: Complete

## Stage 9: dcode GUI（可展開細節的本機 Web UI）

**Goal**: `dcode` 互動模式預設開啟本機 GUI（Go binary 內嵌 React SPA、開瀏覽器）；完整歷史可捲動；預設收合結構與 TUI 相同，每一步可展開看完整指令、輸出、LLM input / output；檔案改動以 GitHub 樣式 diff 呈現。
**Success Criteria**: `make check` 通過；`make web` 產生 `code/gui/dist`；`dcode` 開啟 GUI、`-tui` 回到 TUI、`-plain` 仍是 line REPL；沒有 token 或 Origin 不符的 API 請求被拒；重新載入頁面後歷史（含 subagent、LLM input、diff）仍可展開。
**子階段**:
- 9a trace：`code/trace`（每 thread 一個 JSONL、blob 以 hash 去重）、`trace.Model` 記錄每次 model call 的完整 request 並在回覆標上 `trace_seq`、`trace.WrapBackend` 在寫入 / 編輯 / 刪除時記錄完整 unified diff、`trace.Middleware` 讓 subagent 記錄帶 `task:<id>` namespace；loop 把 tool call ID 放進 ctx（`tool.CallIDFrom`）；`GET /threads/{id}/trace`、`GET /threads/{id}/trace/llm/{seq}`、`EventTrace`
- 9b `code/gui`：embed SPA、token + Origin 檢查、只綁 loopback、遠端模式 reverse proxy；CLI 預設 GUI、`-tui`、`-no-open`
- 9c 前端 timeline：歷史載入、NDJSON 串流、輸入框、核准卡片、虛擬捲動
- 9d 展開詳情：各 tool renderer、diff viewer、LLM 檢視器、Files changed
- 9e thread 側欄、過濾 / 搜尋、快捷鍵、`make web`、README
**Tests**:
- trace：完整 request 還原、摘要不含 body、hash 去重、`new_from`、重開後 seq 延續、thread ID 不能逃出目錄、model 回覆帶 seq、backend diff（新增 / 編輯 / 刪除）且保留 execute / delete 能力
- server：端到端 main + subagent 的 model call、diff、subagent 訊息都被記錄；`EventTrace`；HTTP trace 路由 200 / 404 / 400
- gui：無 token 403、錯 Origin 403、靜態檔、proxy；CLI flag
- 前端（vitest）：事件 → timeline reducer、diff 解析、tool 輸出解析、過濾 / 搜尋
**Status**: Complete

## Stage 8: StateBackend、permissions、網路工具、Skills、HITL edit

**Goal**: delete 工具預設開啟；`StateBackend` 成為沒給 backend 時的預設；filesystem permissions；dcode 的 `web_search`（Brave）與 `fetch_url`；Skills；HITL edit / respond 決策。
**Success Criteria**: 每個子階段 `make check` 通過；沒給 backend 的 `CreateDeepAgent` 能在 state 內寫檔並跨 `Invoke` 讀回；deny 規則讓工具回 permission denied；`dcode -n` 能用 fetch_url 抓網頁；SKILL.md 出現在 prompt；核准時可修改 tool 參數後再執行。
**子階段**:
- 8.0 delete：`DefaultTools` 同 Python 全開、symlink 只刪 link、dcode 核准（approve / reject）與核准畫面
- 8a `FilesReducer`、`StateBackend`、filesystem 工具以 `RunContext` 產生 `files` update、CreateDeepAgent 預設 StateBackend
- 8b permissions：`Validate` / `Check` / `FilterPaths` / `BuildInterruptOn`、filesystem 工具檢查、`WithPermissions`、subagent 繼承
- 8c `web_search`（Brave：所有 web/search 參數與 `x-loc-*` header 都是 tool 參數；summary / rich 自動打後續 API）、`fetch_url`（SSRF 防護、HTML 轉 markdown）
- 8d Skills：SKILL.md frontmatter、`ListSkills`、SkillsMiddleware、`WithSkills`、dcode skill 來源
- 8e HITL edit / respond
**Tests**:
- delete：預設工具清單、非 Deleter backend、刪檔 / 目錄 / 根目錄 / symlink / 不存在、dcode 核准與自動核准
- StateBackend 讀寫經由 RunContext 產生 `files` update；files reducer 以 nil 代表刪除；跨 Invoke 讀回；subagent files 回寫
- 權限規則 deny 時工具回傳 permission denied，ls / glob / grep 結果被過濾；interrupt 規則觸發 HITL
- Brave 參數對應、驗證、錯誤轉換、後續呼叫；fetch_url 封鎖私有 IP 與 redirect
- SKILL.md frontmatter 解析與驗證、多來源覆蓋
- HITL edit 決策（取代 AI 訊息中的 tool call 參數）、respond
**Status**: Complete（真模型 smoke：web_search 需使用者提供 `BRAVE_API_KEY` 後再跑）

## Stage 10: 長任務 context 管理與 dcode 日常體驗

**Goal**: 巨大的 tool 結果與使用者訊息不再塞爆 context（CompositeBackend + eviction）；dcode 有統一的 slash command registry、執行中切換模型、context 用量與成本。`BaseSandbox.ExecuteWithOffload`、ask_user、hooks、MCP、Anthropic 不在這一步。
**Success Criteria**: 每個子階段 `make check` 通過（前端改動加跑 `make web-test`）；真模型 smoke：大輸出指令只回路徑加預覽且可用 `read_file` 分頁讀回；TUI `/model` 切換後續聊；`/cost` 顯示金額。
**子階段**:
- 10a `CompositeBackend`（最長前綴路由、ls / grep / glob 合併、upload / download 分組、能力依 default）；dcode 以它取代 `withHostRoutes`，`artifactsRoot = /.deepagents`
- 10b eviction：tool 結果寫到 `{artifactsRoot}/large_tool_results/{id}`、使用者訊息寫到 `{artifactsRoot}/conversation_history/{uuid}.md`，回傳路徑加頭尾預覽；寫入失敗退回截斷
- 10c `commands.Registry`（alias、未知指令建議、help）、TUI / REPL 共用 handler、`GET /commands` 與 GUI 自動完成
- 10d `RunRequest.Model` + 動態 model wrapper（subagent 跟著換、回覆標 `model_name`）、thread 記住模型、`/model [spec] [--default]`
- 10e 模型 context window 表、價格表（內嵌 + `~/.deepagents/prices.json`）、`GET /threads/{id}/usage`、`/tokens` `/context` `/cost`、狀態列 context 百分比
**Tests**:
- composite：路由與路徑改寫、根目錄 ls 含 route、grep 全域預算、glob pattern 改寫與合併、upload / download 順序、非 sandbox default 無 execute、dcode skills 唯讀
- eviction：門檻以下不動、超過時寫檔與預覽、10 行內全顯示、長行裁切、排除工具、寫入失敗退回截斷、artifacts root、使用者訊息只 evict 一次且 state 不變
- registry：alias、建議、隱藏指令、TUI / REPL 既有指令、`/commands` 路由、GUI 自動完成
- model：不同 request 不同 model（含 subagent）、未知 spec 400、thread 記住模型、`--default` 寫回 config.toml、回覆帶 `model_name`
- usage：profile 前綴比對、價格計算（含 cache read）、未定價、覆寫檔、多模型加總、指令輸出、summarization 改用比例觸發
**Status**: Complete（真模型 smoke 已跑：`seq 1 300000` 只回路徑且檔案可讀回；`/model` 切換後續聊並經 `dcode serve` + `-r` 沿用；`/cost` 顯示金額。偏離計畫：eviction 門檻 0 代表預設值、負數才關閉）

## Stage B: dbench（SWE-bench Pro V2 評測）

之後的迭代以真實能力為準：用 SWE-bench Pro V2 固定 50 題（`code/bench/subsets/smoke50.txt`）衡量 dcode，結果、測試明細、過程指標與成本寫進 PostgreSQL（`bench_*` 表），表現穩定後再跑 `full`（642 題）與 `hard`（51 題）。agent 階段遵照官方 locked protocol：容器 `--network none`、無 web 工具、每題 50 分鐘；patch 在乾淨映像上用官方 `tests/test.sh` 重新評分。

### Stage B1: Task catalog 與 smoke50
**Goal**: `dbench prepare` 抓固定 commit 的 `v2/`（sparse checkout）、驗證 SHA256SUMS、從 HF datasets-server 取 metadata 並寫入 `bench_tasks`；`dbench subset` 依語言分層、HARD 比例、repo 輪流抽樣。
**Success Criteria**: 642 題、6436 個 checksum 通過；smoke50 = go 20 / python 18 / js+ts 12、HARD 4、11 個 repo 都有；同一 seed 兩次輸出相同。
**Tests**: `config.json` 三種清單格式、Python literal 跳脫、patch 統計、verifier 測試路徑規則、gold 檔案 precision / recall、分層配額與決定性、HF 分頁、checksum 竄改偵測、Postgres / memory store 來回（無 `POSTGRES_CONN_STRING` 時 skip）
**Status**: Complete

### Stage B2: 評分器與驗收
**Goal**: Docker 封裝；新容器套 patch（官方 PatchReplayAgent 指令鏈）→ `bash /tests/test.sh` → `reward.txt` 與 `output.json` 算出 F2P / P2P；`dbench gate` 跑 oracle 與 nop。
**Success Criteria**: oracle 全過、nop 全不過；fake docker 單元測試涵蓋套用失敗、verifier 逾時、缺 output.json。
**Tests**: F2P / P2P 計數、套用失敗仍跑 verifier、空 patch（nop 仍跑 verifier）、verifier 逾時、reward 與 output.json 矛盾時報錯、runner 續跑 / 跳過已完成 / infra 重試 / 取消、重新評分
**Notes**: `test.sh` 只在 exit trap 建立 `/logs/verifier`（Harbor 會預先掛載），所以評分前要先 `mkdir -p /logs/verifier`，否則 output.json 靜默遺失。
**Status**: Complete（實機 gate smoke50：oracle 50/50、nop 0/50、infra error 0；映像共約 198 GB）

### Stage B3: agent 階段
**Goal**: `/app` 複製到主機 workspace 並掛回斷網容器；檔案工具走 FilesystemBackend，`execute` 走 `docker exec`；不掛 web 工具、不載入使用者層設定；50 分鐘預算；照官方指令擷取 `git diff`；收集步數、工具、token、成本、時間、定位指標。
**Success Criteria**: 單題實跑產生 patch、trace 與過程指標並完成評分；prompt 只看到 `/app`、沒有 web 工具。
**Tests**: `DockerExecBackend`（工作目錄與環境變數、與檔案工具共用檔案、逾時、輸出上限）、`NoWebTools` / `WorkingDirSection`、solver 擷取 patch 與指標、`/testbed` 後備、成本上限、錯誤分類、trace 指標（排除 summarization 呼叫）
**Status**: Complete（實機單題：GLM-5.3 跑滿 30 分鐘上限、65 步、$1.43，patch、trace、指標與評分都寫進 Postgres）

### Stage B4: 報表與比較
**Goal**: SQL views、`dbench report / compare / history`、Wilson、McNemar、paired bootstrap、`dbench refs import` 做同題比較。
**Tests**: Wilson 區間、精確 McNemar、bootstrap 決定性、summary 排除 infra、pass@k、同題配對與翻轉清單、CSV / JSON 參考成績解析
**Status**: Complete

### Stage B5: 第一輪基準線
**Goal**: 用官方 harness 研究中的開源模型跑 smoke50，和 mini-swe-agent / Pi / OpenCode 同題同模型比較，產出下一步優化清單。
**Status**: Not Started

## Stage 11: 結構化 coding tools（超出 Python 版）

**Goal**: dcode 層（`code/codetools`）新增 `git_status` / `git_diff`、Codex 格式 `apply_patch`、Go 的 `run_tests` / `diagnostics`，把指令輸出整理成 JSON 回給 LLM。指令一律經 `SandboxBackend.Execute`（dbench 容器也能用），檔案改動經 backend 讀寫。核心 `middleware/filesystem` 不動。
**Success Criteria**: 每個子階段 `make check` 通過（有動前端加跑 `make web-test`）；backend 不能執行指令時只提供 `apply_patch`；`apply_patch` 任一 hunk 失敗時沒有檔案被改；核准畫面顯示 patch 的 unified diff。
**子階段**:
- 11a `git_status` / `git_diff`（唯讀、不需核准；porcelain v2、numstat、按檔切 diff、untracked、沒有 HEAD、輸出上限）
- 11b `apply_patch`（解析、逐層放寬的 seekSequence、全有或全無、平行寫入保護、HITL diff 預覽、GUI、bench metrics）
- 11c `run_tests` / `diagnostics`（Go：test2json、`go build` / `go vet` 解析；參數驗證；沒有 go.mod 回 unsupported；核准規則比照 execute）
**Tests**:
- 解析器 table-driven：改名、檔名含空白、binary、刪除；patch 每種 op 與格式錯誤；test2json 通過 / 失敗 / panic / build fail / subtest；vet 輸出
- 端到端：暫存目錄 `git init` 經 `LocalShellBackend`；apply_patch 在 `FilesystemBackend` 與 `StateBackend`；會失敗的 Go module
- 核准：apply_patch 需核准且預覽為 diff；run_tests / diagnostics 依 shell allow list
**Status**: Complete（真模型 smoke：有 bug 的 Go 專案裡 `dcode -n` 依序用了 apply_patch → run_tests → diagnostics → git_diff 並修好測試。dbench smoke50 的 Go 題比較尚未跑：需要 `POSTGRES_CONN_STRING`，排在 Stage B5 基準線一起做）

## Stage 12: 驗證閘門與多語言驗證工具

**Why**: dbench NodeBB 題（gpt-6-luna-pro）只過 2/4 F2P。agent 因為 Redis 沒啟動而一次測試都沒跑成功，只跑 lint 就宣告完成；兩個錯（`relative_path` 前綴、slugify 小寫檔名）跑測試就會現形。system prompt 早就要求「Verify before declaring done」，所以只靠 prompt 不夠。
**Goal**: headless 模式加入確定性的完成前驗證（改過檔案卻沒有通過的測試 / build 就擋下結束，最多 2 次）；`run_tests` / `diagnostics` 支援 JS/TS 與 Python；prompt 補上環境建置與最小改動；工具錯誤訊息改成可操作。
**Success Criteria**: 每個子階段 `make check` 通過；NodeBB 題 agent 會自己啟動服務並跑測試；`dbench run -limit 10` 前後比較有數據。
**子階段**:
- 12a `VerifyGate`（`code/agent/verify.go`）：AfterAgent 判斷修改後有沒有驗證證據，`[verify]` 標記計數、上限 2；WrapToolCall 對 `ECONNREFUSED` 附服務啟動提示；只在 headless 掛上，`Options.NoVerifyGate` 可關
- 12b `run_tests` / `diagnostics` 多語言：依 `go.mod` / `package.json` / Python 標記檔偵測；mocha / jest / vitest JSON、pytest junit XML（改用 XML 而非 `-rA`：輸出大小可控），報告由內嵌的 node / python 腳本壓成摘要再交給 Go 解析；tsc / eslint、ruff / 記憶體內語法檢查（不寫 `__pycache__`）
- 12c system prompt：run_tests 語言、測試環境自己建、不加題目沒要求的驗證、不確定就印出實際值
- 12d 工具錯誤訊息：edit_file 多處相符時列行號、apply_patch hunk 規則與最接近行提示、git 逾時 120 秒
**Tests**:
- gate：沒修改放行、修改未驗證擋下、驗證通過放行、驗證失敗擋下並附失敗測試、擋滿放行、服務提示、FakeModel 整合（擋下後繼續跑）
- 解析器 table-driven：mocha / jest / vitest JSON（含混入 log 的 stdout）、pytest 摘要、tsc / eslint / ruff 輸出
- 錯誤訊息：多處相符列出行號、空 hunk、最接近行
**Evidence**: 無外部回饋的自我修正無效（Huang et al. ICLR 2024）、有可靠外部回饋才有效（Kamoi et al. 2024）；Claude Code `Stop` hook；SWE-agent ACI ablation；Anthropic 工具設計與 eval 文章
**Results**（gpt-6-luna-pro）:
- NodeBB 題重跑 `20260930-155159`：resolved（f2p 4/4、p2p 307/307，$0.06）。eslint diagnostics 抓到重複宣告；mocha run_tests 回報 `ECONNREFUSED 6379` 後 agent 自己 `redis-server --daemonize yes`，再跑 207 / 334 個測試全過。
- smoke50 前 10 題 A/B（before = 同一份程式碼關掉 gate、run_tests / diagnostics 只支援 Go、舊 prompt）：`20260930-160724` 8/10 vs `20260930-160743` 8/10，解出的題目相同（McNemar p = 1.0；10 題樣本太小，看不出解題率差異）。過程指標有變：after 每題都用 run_tests（2–8 次）與 diagnostics，before 幾乎沒用（0–1 次，Node 題會回 unsupported）；gate 在 10 題中擋下 1 次。成本 $0.52 vs $0.57。
- 驗證時發現：工作目錄裡另一份未提交的 summarization 修改把「模型沒有 context window 資訊」時的觸發門檻從 170k 降到 16k tokens，gpt-6-luna-pro 不在 context window 表內，於是每幾步就摘要一次、反覆重讀檔案而不動手（`20260930-151154`，343 次 tool call 沒有任何修改，已中止）。上面的數據是在 /tmp 複本把門檻改回 170k 後跑的。
**Status**: Complete

## Stage 13: Plan 模式與 write_todos 強化（參考 Cursor / Claude Code）

**Why**: trace 顯示 agent 在讀任何檔案前就呼叫 `write_todos`，清單是三條通用步驟（「檢視專案」「實作」「跑測試」）；沒有先研究、再產出確定性計畫並等使用者同意的流程。
**Goal**: todo 是執行中會修正的追蹤清單（可含待查證項目）；plan 是唯讀研究後產出的確定性計畫，附 checklist，使用者按 Build 才轉成 todos 開始實作。TUI 與 GUI 都支援。
**Success Criteria**: 每個子階段 `make check` 通過（有動前端加跑 `make web-test`）；Plan 模式下模型拿不到寫入工具、呼叫也會被擋；計畫寫到 `~/.deepagents/plans/`；Build 之後 todos 與計畫一致。
**子階段**:
- 13a `write_todos`：`{id, content, status, notes?}`、`cancelled`、`merge` 依 id 部分更新、舊資料自動補 id；prompt 改成先探索再寫、項目具體可驗證、不確定的寫成 investigation
- 13b `ask_user`：選擇題提問，走 HITL 中斷（只允許 respond / reject）；`InterruptOnConfig.Always` 讓它在 YOLO 下仍中斷；只在 interactive 註冊
- 13c `middleware/planmode`：`RunConfig.Configurable` 帶 mode；唯讀工具白名單（WrapModelCall 過濾 + WrapToolCall 擋）、plan prompt、`create_plan` 寫檔與 state `plan`、`build_plan` 時把計畫 todos 寫進 state
- 13d server / 指令：`RunRequest.Mode`、`BuildPlan`（plan 只限 interactive）、`ThreadState.Todos` / `Plan`、`/plan`、`/build`
- 13e TUI / GUI：模式顯示與切換、todo notes、計畫檢視與 Build、ask_user 選擇介面
**Tests**:
- todolist：merge、補 id、cancelled、錯誤狀態、merge 未知 id
- ask_user：參數驗證、auto-approve 下仍中斷、respond 答案成為 tool 結果
- planmode：兩種模式的工具清單、擋下寫入、create_plan 寫檔與 state、覆寫同一檔、build 產生 todos
- server：headless 不能 plan、Configurable 帶 mode
- TUI：`/plan`、`/build`、ask 流程；GUI vitest：todosOf、planOf、答案序列化
**Status**: Complete
**結果**: `make check`、`make web-test` 通過，`code/gui/dist` 已重建。真實模型 smoke（`openrouter:openai/gpt-6-luna-pro`，`dcode serve` + `POST /runs`）：plan 模式只提供 10 個唯讀工具；模型先呼叫 `ask_user` 中斷，回答後讀檔並 `create_plan`，寫出含 Findings / Approach / Verification / Risks 與 2 項 todo 的計畫檔；`build_plan` 後 todos 與計畫 id 一致，模型依序以 `write_todos`（含 merge 與 notes）推進並全部完成，產出的程式與測試可執行。

## Stage 14: 持久儲存改用 Postgres / Redis，web 工具對齊主流 coding agent

**Why**: `fetch_url` 把整頁 markdown（最多 20k tokens）直接放進主 context，`web_search` 預設回 20 筆加 extra snippets / rich JSON；網頁內容沒有 prompt injection 框隔。主流做法：Claude Code `WebFetch(url, prompt)` 由 Haiku 擷取後只回答案、`WebSearch` 只回標題 / URL / 摘要；Gemini CLI `web_fetch(prompt)`、Amp `read_web_page(prompt)` 同樣模式；多步研究交給 subagent，Claude Code subagent 可背景執行、完成時以通知送回主 agent。另外 plan、trace、eviction 結果散落在本機檔案，重啟或換機器就不一致。
**設計取捨**:
- 單頁擷取是「一次 LLM 呼叫、不帶工具」，放在 tool 內部，不做成 subagent；每頁一次（同 Claude Code），可平行、可用 `url + prompt` 快取、單頁失敗不影響其他頁。
- 平行不需要 subagent：loop 已平行執行同一輪的 tool calls（`maxParallelTools = 8`）。
- 「搜尋時主 agent 繼續做別的事」靠背景 `task`，這是通用能力，不綁 web。
- 儲存：重啟後要讀回的完整內容放 Postgres（thread、checkpoint、plan、trace、`conversation_history`）；可重新產生的快取放 Redis 並設 TTL（網頁原文、搜尋 / 擷取 / Context7 結果、`large_tool_results`）。沒有設定 Postgres 或 Redis 時啟動失敗。
**Goal**: 主 context 只收到擷取後的答案與引用；多查詢研究可委派給 `web-researcher`；`task` 可背景執行；本機不再存 plan / trace / eviction 檔。
**Success Criteria**: 每個子階段 `make check` 通過（有動前端加跑 `make web-test`）。
**子階段**:
- 14.0 儲存基礎：`DCODE_REDIS_URL`（相容 `REDIS_URL`）、`runtime.PostgresStore` / `RedisStore`、`backend.StoreBackend`、`code/storage.Open` 啟動檢查
- 14.1 搬移本機資料：plan 存 `dcode_plans`、trace 存 `dcode_trace_records` / `dcode_trace_blobs`、`large_tool_results` 與網頁原文走 Redis（`storage.results_ttl`，預設 7 天；網頁 24 小時）、`conversation_history` 走 Postgres store
- 14a `fetch_url(url, prompt)`：以 `models.web`（未設定用主模型）擷取，網頁以 `<web_content>` 框住；完整頁面存 `/.deepagents/web/<hash>.md`；15 分鐘快取；擷取失敗退回原文
- 14b `web_search`：`count` 預設 8、`allowed_domains` / `blocked_domains`、15 分鐘快取；prompt 改為「Looking Things Up」查詢順序
- 14c Context7：`resolve_library_id` / `query_docs`（`CONTEXT7_API_KEY`），1 小時快取、錯誤碼對應、需核准
- 14d `web-researcher` subagent：web 工具 + 唯讀檔案工具，最多 4 次搜尋、8 頁；啟動研究的 `task` 需核准
- 14e 背景 `task`：`run_in_background`、`<task-notification>`（BeforeModel 注入、進 checkpoint）、`task_output(task_id, timeout)`、AfterAgent 等待、取消 run 連帶取消；registry 只在記憶體
- 14f TUI / GUI：背景 task 顯示「background」直到通知到達；Context7 / fetch 的專屬檢視；`fetch_url` 掛上 `web_extract` 擷取呼叫
**Tests**: 各子階段的單元測試（httptest 驗請求形狀與快取、FakeModel 端到端、barrier 證明 fetch 平行與背景 task 與主 agent 重疊、`-race` 壓測背景 task）、真實 Postgres / Redis 的 store 與 storage 測試、GUI vitest。
**Status**: Complete
**結果**: `make check`、`make web-test` 通過，`code/gui/dist` 已重建。尚未做真實模型 smoke（整頁 vs. 擷取的 token 比較、Context7 釘選版本查詢、serve 重啟後 plan / trace 仍在）。

## Stage 15: 縮短單題時間（慢工具與 write_todos 回合）

**Why**: smoke50 前 10 題 trace（`20260930-160724` / `160743`）中 agent 時間約 85% 在等模型。工具端：`git_status` ansible 中位數 21 秒、`git_diff` 最慢 61 秒、從根目錄 `grep` NodeBB 最慢 15 秒。模型端：520 個主 agent 回合裡有 68 個只呼叫 `write_todos`（約 995 秒）。
**實測**（NodeBB-8168 映像、照 `DcodeSolver` 複製 `/app` 並掛回容器）:
- git：`docker cp` 讓每個檔案的 stat 資料都變了，而 git 工具帶 `GIT_OPTIONAL_LOCKS=0` 不會寫回 index，所以每次都重新 hash 整個工作樹。`git status` 6–8 秒、`git diff` 9.1 秒；先跑一次 `git update-index -q --refresh`（0.3 秒）後分別是 0.9 秒、0.4 秒。
- grep：從 `/` 搜尋 11–15 秒，第一次撞到 15 秒預算回傳 0 筆且 truncated；只搜 `/src` 0.03 秒；glob 全樹 0.7 秒。時間都花在讀 `node_modules` 內容。
- eslint 單檔 4–8 秒（`--cache` 無明顯差異），屬專案本身成本，不在這一步處理。
**子階段**:
- 15a `DcodeSolver` 啟動容器後刷新一次 git index
- 15b `FilesystemBackend.Grep` 略過依賴目錄（`node_modules` 等），搜尋路徑本身在其中時照常搜尋；工具說明寫明
- 15c `write_todos` prompt：明說可以和其他工具放在同一個回應，只是不能同一回應呼叫兩次
**Success Criteria**: `make check` 通過；實測 git_status / git_diff / 根目錄 grep 回到 1 秒左右。
**Tests**:
- solver：refresh 指令在 agent 執行前送出、失敗時回報錯誤
- grep：根目錄搜尋不回傳 `node_modules` 內的結果、明確指定 `node_modules` 路徑時照常回傳
- todolist：prompt 不再出現會被理解成「單獨呼叫」的措辭、含同回應搭配其他工具的指示
**Status**: Complete
**結果**: `make check` 通過。同一份 NodeBB 工作目錄重測：刷新 index 後 `git status` 0.9 秒、`git diff` 0.4 秒；根目錄 grep 0.6–2 秒且結果正確（原本 11–15 秒、會逾時回傳 0 筆）。`write_todos` 回合數的變化要等下一次 dbench 跑完才有數據。
**另外確認 summarization**: 觸發門檻已不是 16k。gpt-6-luna 已在 context window 表內（1,050,000），觸發點是 80%（約 840k），保留 10%；沒有 profile 的模型退回 170k 觸發。原本兩個 16k 相關常數之後都已移除：`createSummary` 每塊上限改為 `window/4`（`TrimTokensToSummarize` 預設不設），`contextBudget` 對沒有 profile 的模型假設 200k context window（`assumedContextWindow`）再取 80%。
- 15d 已修：沒有 profile 的模型一律假設 200k window（與 170k 退回觸發點一致），`contextBudget` 與摘要每塊上限都由同一個 window 推算；每塊上限改為 window / 4，不再封頂 16k（luna 約 262k、未知模型 50k，原本是 16k / 4k）。測試：未知 window 的模型接受 20k tokens 的 system prompt（原本被拒）、約 36k tokens 的歷史在 1M 與未知 window 下都只要 1 次摘要呼叫（原本 3 次與 10 次左右）。
