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
