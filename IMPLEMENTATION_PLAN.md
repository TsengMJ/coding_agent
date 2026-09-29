# Implementation Plan

分階段把 Python deepagents 移植成 Go。每完成一個 Stage，就同步更新本檔的 Status 與 [`TODO.md`](TODO.md) 的勾選狀態。

## Stage 1: 框架與介面

**Goal**: 定義核心 SDK 與 CLI 的套件分層、介面、型別與 stub，不寫業務邏輯。
**Success Criteria**: `go build ./...`、`go vet ./...`、`gofmt -l .` 皆無輸出；`go test ./...` 通過；TODO.md 列出所有 Python 功能並標記狀態。
**Tests**: 介面符合性（compile-time assertions）、stub 回傳 `notimpl.ErrNotImplemented`、slash command catalog 數量與唯一性。
**Status**: Complete

## Stage 2: 最小 agent loop

**Goal**: `llm`、`tool`、`runtime`、`agent` 可以用 fake model 跑通「model → tool → model」迴圈。
**Success Criteria**: `agent.CreateAgent` 能執行多輪 tool call 直到 model 回傳最終答案；middleware hooks 依正確順序被呼叫；`MemoryCheckpointer` 可保存並續跑 thread。
**Tests**:
- `MessagesReducer`：append、依 ID 取代、`RemoveAllMessages`
- `State.Apply` 搭配自訂 reducer
- FakeModel 編排兩次 tool call 後結束，驗證最終 messages
- WrapModelCall / WrapToolCall 巢狀順序
- recursion limit 觸發錯誤
- checkpoint 後以同 thread 續跑
**Status**: Not Started

## Stage 3: Backend 與 FilesystemMiddleware

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

## Stage 4: SubAgents 與 CreateDeepAgent

**Goal**: SubAgentMiddleware 的 `task` 工具、CreateDeepAgent 完整組裝（profiles、預設 general-purpose subagent），並接上一個真實 provider（Anthropic 或 OpenAI）。
**Success Criteria**: `deepagents.CreateDeepAgent(WithModelSpec(...))` 產生可執行的 agent，能委派任務給 subagent 並拿回結果。
**Tests**:
- middleware 組裝順序與 `_apply_custom_middleware` 取代規則
- excluded middleware 驗證（不可排除 Filesystem / SubAgent）
- prompt 組裝 USER → BASE → SUFFIX
- `task` 工具 isolated 模式只傳描述、結果取最後 AI 訊息
- provider 以 httptest 模擬 API 回應
**Status**: Not Started

## Stage 5: 進階 middleware 與 CLI headless

**Goal**: Summarization、Memory、Skills、HITL、PatchToolCalls 完成；`dcode -n "prompt"` 能在本機以 LocalShellBackend 執行一次任務。
**Success Criteria**: 長對話會被自動摘要並 offload；AGENTS.md 與 SKILL.md 被注入 prompt；HITL 能中斷並以 Command 續跑；headless CLI 輸出結果並回傳 exit code。
**Tests**:
- summarization 觸發條件與 cutoff 不切斷 tool call 配對
- SKILL.md frontmatter 解析與驗證
- HITL approve / reject / edit 決策
- dangling tool call 修補
- headless CLI 以 fake model 端到端執行
**Status**: Not Started
