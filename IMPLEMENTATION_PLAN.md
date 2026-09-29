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
**Status**: Complete

## Stage 3: Checkpointer 與 thread 續跑

**Goal**: `MemoryCheckpointer` 可保存 state；`Invoke` 以同一個 thread 續跑；`GetState` 可用；`dcode -thread` 能接續對話。
**Success Criteria**: 同一 thread 第二次 `Invoke` 看得到第一次的 messages；`CreateAgent` 接受 Checkpointer；`dcode -thread id` 不再回「thread 續跑尚未支援」。
**Tests**:
- `MemoryCheckpointer` Put / Latest / List / DeleteThread
- checkpoint 後以同 thread 續跑，驗證 messages 累積
- 不同 thread 互不影響
- server / client 帶 ThreadID 的端到端測試（fake model）
**Status**: Not Started

## Stage 4: Backend 與 FilesystemMiddleware

**Goal**: `StateBackend`、`FilesystemBackend` 與 `backend/utils` 完成；FilesystemMiddleware 的 ls / read_file / write_file / edit_file / glob / grep 可用。
**Success Criteria**: 使用 fake model 的 agent 能在 state 或本機檔案系統上讀寫檔案，且 virtual mode 擋下路徑逃逸。
**Tests**:
- `PerformStringReplacement`（唯一匹配、replace_all、找不到）
- `FormatContentWithLineNumbers` 與分頁 `SliceReadResponse`
- `ValidatePath` 拒絕 `..` 與 `~`
- StateBackend 讀寫經由 RunContext 產生 `files` update
- FilesystemBackend 在 temp dir 上的 ls / read / write / edit / grep / glob
- 權限規則 deny 時工具回傳 permission denied
**Status**: Not Started

## Stage 5: SubAgents 與 CreateDeepAgent 完整組裝

**Goal**: SubAgentMiddleware 的 `task` 工具、CreateDeepAgent 完整組裝（預設 middleware stack、profiles、預設 general-purpose subagent）。
**Success Criteria**: `deepagents.CreateDeepAgent(WithModelSpec(...))` 產生帶預設 middleware 的 agent，能委派任務給 subagent 並拿回結果。
**Tests**:
- middleware 組裝順序與 `_apply_custom_middleware` 取代規則
- excluded middleware 驗證（不可排除 Filesystem / SubAgent）
- prompt 組裝 USER → BASE → SUFFIX
- `task` 工具 isolated 模式只傳描述、結果取最後 AI 訊息
**Status**: Not Started

## Stage 6: 進階 middleware 與 CLI

**Goal**: Summarization、Memory、Skills、HITL、PatchToolCalls 完成；`dcode -n "prompt"` 能在本機以 LocalShellBackend 執行一次任務。
**Success Criteria**: 長對話會被自動摘要並 offload；AGENTS.md 與 SKILL.md 被注入 prompt；HITL 能中斷並以 Command 續跑；headless CLI 可使用 shell 工具。
**Tests**:
- summarization 觸發條件與 cutoff 不切斷 tool call 配對
- SKILL.md frontmatter 解析與驗證
- HITL approve / reject / edit 決策
- dangling tool call 修補
- headless CLI 以 fake model 端到端執行 shell 工具
**Status**: Not Started
