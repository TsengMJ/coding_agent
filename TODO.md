# deepagents → Go 功能對照 ToDo

本文件逐項對照 Python 版 [deepagents](../deepagents) 的功能與物件，追蹤其 Go 實作狀態。**每完成一項就更新對應勾選狀態。**

## 狀態符號

- `[ ]` 未開始（Go 端尚無任何型別或函式）
- `[~]` 介面 / stub 已定義（型別與簽名存在，呼叫回傳 `notimpl.ErrNotImplemented` 或為佔位行為）
- `[x]` 已實作且有測試
- `[-]` 不打算實作（需在行尾註明原因）

## 路徑慣例

- `py:` 相對於 `deepagents/libs/deepagents/deepagents/`（核心 SDK）或 `deepagents/libs/code/deepagents_code/`（CLI，標註 `code:`）
- `go:` 相對於 `deepagents_golang/`

## 進度總覽

- 最後更新：2026-09-30（Stage 10 完成：context 管理與 dcode 日常體驗）
- 核心 SDK：圖、in-memory / Postgres checkpoint（每 thread 保留最新 10 筆）、interrupt、OpenAI Generate / Stream 走套件（含重試與串流閒置逾時）；FilesystemBackend / LocalShellBackend / FilesystemMiddleware / HITL（approve / reject、`When`）/ PatchToolCalls 可用；StateBackend（CreateDeepAgent 預設）可用；其餘 middleware、Store 仍為 stub
- CLI：`dcode serve`（HTTP + NDJSON、兩階段關閉）、預設瀏覽器 GUI（可展開每一步、GitHub 樣式 diff、LLM 檢視器；trace 存在 Postgres）、`-tui` Bubble Tea TUI（markdown、diff 預覽）與 line REPL、`-n` / `-thread` / `-r` / `-y` / `-q` / `-no-stream` / `-timeout`、`dcode threads [-json]`；使用者層 `config.toml` 與 shell allow-list

---

## 0. 專案基礎設施

- [x] Go module、README、目錄分層 — go: `go.mod`, `README.md`
- [x] 未實作 sentinel error `notimpl.Err` / `ErrNotImplemented` — go: `internal/notimpl`
- [x] 佔位工具 `stubtool.New`（讓 middleware 提前暴露最終工具清單） — go: `internal/stubtool`
- [ ] CI（GitHub Actions：build / vet / gofmt / test）
- [ ] golangci-lint 設定
- [ ] 範例程式 `examples/`（對應 `deepagents/examples`）
- [-] `_api/deprecation.py`（deprecated 裝飾器）— Go 版無歷史包袱，以 `// Deprecated:` 註解取代
- [-] `_version.py` 的 langchain 版本偵測 — 僅保留 `deepagents.Version`

---

## 1. Runtime 基礎（採用 langchaingo / langgraphgo）

### 1.1 訊息 `llm/message.go`（langchain_core.messages）

- [~] `Role`（system / user / assistant / tool）
- [~] `ContentBlock`（text / image / audio / video / file / reasoning、base64、url、mime_type、cache_control）
- [~] `ToolCall`（id / name / args）
- [~] `Usage`（input / output / total / cache read / cache write）
- [~] `Message`（SystemMessage / HumanMessage / AIMessage / ToolMessage 合一）
- [~] 建構函式 `NewSystemMessage` / `NewUserMessage` / `NewAssistantMessage` / `NewToolMessage` / `TextBlock`
- [x] `Message.Text()`：串接文字內容
- [ ] JSON 序列化 / 反序列化（checkpoint 用）
- [x] 訊息 ID 自動產生（agent loop：model 回覆與輸入訊息沒有 ID 時補上）

### 1.2 ChatModel `llm/model.go`, `llm/registry.go`, `llm/providers.go`

- [~] `ChatModel` 介面（`Info` / `Profile` / `Generate` / `Stream`）
- [~] `ChatRequest` / `ToolSpec` / `ResponseFormat` / `Chunk`
- [~] `ModelInfo`、`ModelProfile`（max_input_tokens、多模態支援旗標）
- [x] `KnownProfile`：常見 OpenAI / OpenRouter / Claude / Gemini 等模型的 context window 表（模型名最長前綴；OpenRouter 取 `/` 後）；OpenAI / OpenRouter `Profile()` 用它，summarization 因此改以 85% context 觸發
- [x] `TokenCounter` 型別
- [x] `ApproximateTokenCounter`（`count_tokens_approximately`：字元數 / 4 + 每則訊息固定成本）— go: `llm/tokens.go`
- [x] `ModelSpec` / `ParseModelSpec`（`provider:model`，只以第一個 `:` 切分）
- [x] `RegisterProvider` / `InitChatModel`（`init_chat_model`；未知 provider 列出已註冊清單）
- [~] `ModelMatchesSpec`（py: `_models.model_matches_spec`）
- [ ] `get_model_identifier` / `get_model_provider` / `is_bedrock_model` — py: `_models.py`
- [~] Anthropic provider — go: `llm/providers.go` `AnthropicModel`
  - [ ] Generate（Messages API）
  - [ ] Stream（SSE）
  - [ ] tool calling
  - [ ] prompt caching `cache_control`
  - [ ] 多模態輸入（image / pdf）
  - [ ] extended thinking / reasoning blocks
- [~] OpenAI provider — go: `llm/openai.go` `OpenAIModel`（langchaingo `llms/openai`，`OPENAI_API_KEY` / `OPENAI_BASE_URL`）
  - [ ] Responses API（預設；langchaingo 未涵蓋，仍待實作）
  - [x] Chat Completions API（Generate）— 由 langchaingo 提供
  - [x] Stream — langchaingo 串流請求；SSE 經 `sseTee` 取文字 delta，tool call 依 index 自行組回（langchaingo 遇到重複 `type` 會拆錯）— go: `llm/openai_stream.go`
  - [x] tool calling — 由 langchaingo 提供
  - [ ] `store=False` + `reasoning.encrypted_content`
- [~] OpenRouter provider — go: `llm/openrouter.go`（共用 OpenAI Chat Completions client；`OPENROUTER_API_KEY` / `OPENROUTER_API_BASE`）
  - [x] Generate + tool calling、app attribution headers（`HTTP-Referer` / `X-Title`）
  - [ ] `provider: {ignore: [azure]}` 預設路由（`DEEPAGENTS_OPENROUTER_ALLOW_AZURE`）
  - [x] Stream（共用 OpenAI 實作）
- [ ] NVIDIA provider
- [ ] Google Gemini provider
- [ ] Ollama provider
- [ ] Bedrock provider
- [ ] Fireworks provider
- [x] FakeModel（測試用：預先編排回應 / tool calls）— go: `llm/fake.go`
- [x] context overflow 錯誤型別 — go: `llm.ErrContextOverflow`（OpenAI 錯誤碼 / 訊息比對後包裝）
- [ ] 重試 / rate limit 處理

### 1.3 Tool `tool/tool.go`（langchain_core.tools）

- [~] `Tool` 介面（Name / Description / Schema / Invoke）
- [~] `Schema`（JSON Schema）
- [~] `Runtime`（`ToolRuntime`：tool_call_id / state / store / config / context / stream writer）
- [~] `Result`（content / Command / artifact / is_error）
- [~] `Func`（`StructuredTool.from_function`）
- [~] `Spec`：Tool → `llm.ToolSpec`
- [x] `ApplyDescriptionOverrides` — py: `_tools._apply_tool_description_overrides`
- [ ] 從 Go struct 反射產生 JSON Schema
- [ ] 參數驗證（依 schema 驗證 args、拒絕未知欄位）
- [x] 工具錯誤 → ToolMessage(status=error) 轉換 — go: `agent/loop.go`

### 1.4 State / Reducer `runtime/state.go`（LangGraph state）

- [~] `State`（messages / structured_response / 擴充 Values）
- [~] `Update`
- [~] `Reducer`、`KeySpec`（Private = `PrivateStateAttr`、OmitFromInput / OmitFromOutput）
- [~] `Schema` / `MergeSchemas` / `PrivateKeys`（`PrivateKeys` 完成）— py: `middleware/_state.private_state_field_names`
- [x] `State.Apply`
- [x] `State.Clone`（淺層複製：messages slice 與 Values map 另外複製）
- [-] `MessagesReducer`（`add_messages`：append、依 ID 取代）— 合併交給 langgraphgo `AddMessages`，不再自寫。`RemoveAllMessages` 套件不認得，仍在 `runtime` 先切開。`files` reducer 仍自建
- [ ] `_messages_delta_reducer` + `DeltaChannel`（checkpoint 只存增量，snapshot_frequency=50）— py: `_messages_reducer.py`, `graph.DeepAgentState`
- [x] files reducer（`_file_data_reducer`：None 值代表刪除）— go: `backend.FilesReducer`（也吃 checkpoint JSON 讀回的 `map[string]any`，每次回傳新 map）

### 1.5 Checkpointer `runtime/checkpoint.go`

- [x] `Checkpoint`、`Checkpointer` 介面（對外 API；儲存交給套件）。續跑欄位 `Seq` / `Steps` / `ResumeFrom` 具型別，不放在 `Metadata`
- [-] `MemoryCheckpointer`（`InMemorySaver`）— 轉接 langgraphgo `MemoryCheckpointStore`，不自寫儲存。Latest / Put / List / DeleteThread 已接上
- [x] `PostgresCheckpointer` — go: `runtime/postgres.go`，轉接 langgraphgo `checkpoint/postgres`（`POSTGRES_CONN_STRING`，表 `deepagents_checkpoints`）。JSON 讀回還原 `Checkpoint`；pending interrupt 與 `ResumeFrom` 可跨行程讀取
- [-] SQLite checkpointer（`AsyncSqliteSaver`）— CLI 使用 Postgres（`DCODE_POSTGRES_CONN_STRING`，相容 `POSTGRES_CONN_STRING`），未設定時用 in-memory
- [x] `PostgresCheckpointer.Pool()` 讓 sessions 表共用連線池
- [x] checkpoint 序列化格式（JSON）— Postgres 路徑把 `map[string]any` 解回 `Checkpoint`
- [x] 每 thread 只保留最新 `DefaultCheckpointsPerThread`（10）筆；`version` 欄存 seq，Postgres 以索引取最新與裁剪，舊資料（version=0）仍可讀

### 1.6 Store `runtime/store.go`

- [~] `Namespace`、`Item`、`SearchOptions`、`Store` 介面
- [~] `InMemoryStore`
  - [ ] Get / Put / Delete / Search（prefix + filter）
- [ ] 持久化 Store（SQLite / Postgres）

### 1.7 Interrupt / Command `runtime/interrupt.go`

- [~] `Interrupt`、`InterruptError`、`Command`（update / resume / goto）
- [-] interrupt 暫停與 checkpoint 保存 pending interrupts — langgraphgo `GraphInterrupt` / `InterruptBefore`（`RunConfig.Configurable["interrupt_before"]`）
- [-] 以 `Command.Resume` 續跑 — 從同一個 thread checkpoint `ResumeFrom`
- [x] `agent.Interrupt(ctx, value)`（LangGraph `interrupt()`）：在 AfterModel hook 中暫停；resume 值每個 run 只交出一次，同一 run 之後的 Interrupt 會再次暫停 — go: `agent/loop.go`

### 1.8 Config / Event `runtime/config.go`, `runtime/event.go`

- [~] `RunConfig`（thread_id / recursion_limit / tags / metadata / configurable）
- [~] `DefaultRecursionLimit = 9999`
- [~] `StreamMode`（values / updates / messages / custom）、`Event`、`Emitter`
- [-] values 事件串流 — 由 langgraphgo `OnGraphStep` 轉成 `runtime.Event`，不自寫 event loop。updates / custom 尚未接
- [x] messages 串流：`Stream(..., StreamMessages)` 時 model 節點改用 `ChatModel.Stream`，每個片段送出 `llm.Chunk` 事件；沒實作 Stream 的 model 退回 Generate
- [ ] subgraph（subagent）事件 namespace 傳遞

### 1.9 Agent loop `agent/`（langchain `create_agent` + `AgentMiddleware`）

- [~] `Middleware` 介面 + 可選 hooks：`BeforeAgentHook` / `BeforeModelHook` / `AfterModelHook` / `AfterAgentHook` / `WrapModelCallHook` / `WrapToolCallHook` / `ToolProvider` / `StateSchemaProvider`
- [~] `ModelRequest` / `ModelResponse`（含 `ExtendedModelResponse` 的 Update）
- [~] `ToolCallRequest` / `ModelHandler` / `ToolHandler`
- [~] `Runtime`（hook 用）
- [~] `Config` / `Input` / `Agent` 介面（Invoke / Stream / GetState / Name）
- [~] `CreateAgent` — go: `agent/loop.go`, `agent/chain.go`
  - [-] model → after_model → tools → model 迴圈 — langgraphgo `MessageGraph`（這個版本的 `StateGraph.Compile` 不處理 `InterruptBefore`）。AfterModel hooks 在獨立的 `after_model` 節點，中斷後 resume 不重打 model；所有 tool call 都已有回覆（例如被拒絕）時回到 model
  - [x] middleware hook 串接順序（wrap 類：前面的在外層；AfterModel / AfterAgent 逆序）
  - [x] 平行執行多個 tool calls — `tools` 節點以 state 快照同時執行（上限 8），結果依 call 順序套用，tool message 順序不變
  - [x] 未知工具錯誤回報
  - [x] recursion limit（`ErrRecursionLimit`；套件 Invoke 沒有此上限，在 model 節點檢查）
  - [-] checkpoint 讀寫（每步）— `OnGraphStep` 寫入 langgraphgo checkpointer
  - [-] interrupt / resume — 見 1.7
  - [-] streaming（values）— 見 1.8；updates / messages / custom 尚未接
  - [x] middleware 名稱重複檢查（含工具名稱重複檢查）
  - [x] AfterAgent 回傳 `Command{Goto:"model"}` 重新進入迴圈 — langgraphgo `Command`
- [~] Structured output：`ToolStrategy` / `ProviderStrategy` / `AutoStrategy` — go: `agent/response_format.go`
  - [ ] ToolStrategy 實作（合成工具 + 解析）
  - [ ] ProviderStrategy 實作
  - [ ] AutoStrategy 選擇邏輯
- [ ] `context_schema`（不可變 run context）
- [ ] `cache`（節點快取）
- [ ] debug 模式輸出

---

## 2. graph.py：`create_deep_agent` — go: `deepagents.go`

- [x] `CreateDeepAgent(opts ...Option)` 入口（預設 middleware stack、profiles、general-purpose subagent）
  - 備註：response_format / async subagents 仍回 `ErrNotImplemented`；未給 backend 時用 StateBackend
- [~] 參數（functional options）
  - [x] `model`（`WithModel` 優先，否則 `WithModelSpec` 經 `InitChatModel` 解析）
  - [ ] `model=None` 預設 `claude-sonnet-4-6`（Python 已 deprecated）
  - [x] `tools`（`WithTools`）
  - [x] `system_prompt`（`WithSystemMessage` 優先，否則 `WithSystemPrompt`）
  - [x] `middleware`（`WithMiddleware`：同名取代 base stack 原位置，其餘插在 base 之後；重複名稱報錯）
  - [x] `subagents`（`WithSubAgents`：SubAgent / CompiledSubAgent；AsyncSubAgent 回 `ErrNotImplemented`）
  - [x] `skills`（`WithSkills`：主 agent stack 最前面；general-purpose subagent 繼承；宣告式 subagent 用自己的 `Skills`）
  - [x] `memory`（`WithMemory`：以主 backend 讀取，放在 caller middleware 之後、HITL 之前）
  - [x] `permissions`（`WithPermissions`：傳給主 agent 與 subagent 的 Filesystem；interrupt 規則經 `BuildInterruptOn` 併入 HITL；不能搭配有 execute 的 backend）
  - [x] `backend`（`WithBackend`；未給時預設 `StateBackend`，Filesystem 與 Summarization 一律加入）
  - [x] `interrupt_on`（`WithInterruptOn`：HITL middleware 放在 stack 最後）
  - [~] `response_format`（`WithResponseFormat`）
  - [~] `state_schema`（`WithStateSchema`）
  - [ ] `context_schema`
  - [~] `checkpointer` / `store`（`WithCheckpointer` 已轉給 `agent.CreateAgent`；`WithStore` 仍只保存參照）
  - [~] `debug` / `name`（`WithDebug` / `WithName`）
  - [ ] `cache`
- [x] Model 解析（`resolve_model`）+ harness profile 查找（`WithModelSpec` 用 spec，`WithModel` 用 `Info()`）
- [x] Prompt 組裝：USER → BASE → SUFFIX（`_apply_profile_prompt`）；SystemMessage 保留 cache_control blocks
- [~] Main agent middleware 組裝順序（AsyncSubAgent / prompt caching / UnsupportedContent 尚未加入）：Skills → Filesystem → SubAgent → Summarization → PatchToolCalls → AsyncSubAgent → [caller] → profile extra → prompt caching → Memory → HITL → UnsupportedContent → ToolExclusion
- [x] `_apply_custom_middleware`：同名取代原位置、新增者插在 core 之後 tail 之前 — go: `mergeMiddleware`
- [x] Required middleware 保護（Filesystem / SubAgent 不可被排除）— go: `RequiredMiddleware`
- [x] `_excluded_middleware.py`：`_validate_excluded_middleware_config` / `_apply_excluded_middleware` / `_verify_excluded_middleware_coverage`（Go 只有名稱形式，無類別衝突檢查）
- [x] 預設 general-purpose subagent（可被 profile 關閉、可被同名 spec 取代、繼承同名 middleware；不繼承 interrupt_on，因 subagent 尚不能暫停 parent run）
- [~] 宣告式 SubAgent 處理（已完成 model 解析、subagent profile、tools 繼承、permissions 繼承（自己的 Permissions 優先；interrupt 規則轉成 deny，因 subagent 不能暫停 parent run）、skills（subagent 自己的 `Skills`，放在 stack 最後）；fork / interrupt_on 繼承未做）：model 解析、subagent profile、permissions 繼承、interrupt_on 繼承與合併、tools 繼承、skills、fork 模式（鏡射 parent prompt middleware、system prompt addendum）
- [ ] AsyncSubAgent 分流到 AsyncSubAgentMiddleware
- [x] permissions → interrupt_on 合併（`_merge_fs_interrupt_on`）— go: `hitl.Merge(permissions.BuildInterruptOn(...), WithInterruptOn)`，使用者設定優先
- [x] private state keys 注入 SubAgentMiddleware
- [ ] `with_config`：recursion_limit=9999、metadata（ls_integration / lc_versions / lc_agent_name）
- [~] `DeepAgentState`（對應 `runtime.State` + messages delta reducer，見 1.4）
- [-] `BASE_AGENT_PROMPT` / `_LEGACY_BASE_AGENT_PROMPT` — Python 已 deprecated
- [-] `get_default_model` — Python 已 deprecated

---

## 3. Backends — go: `backend/`

### 3.1 Protocol `backend/protocol.go`（py: `backends/protocol.py`）

- [~] 常數：`DefaultGrepTimeout` / `AsyncGrepTimeout` / `AsyncGlobTimeout` / `DefaultReadLimit`
- [~] 錯誤碼：`file_not_found` / `permission_denied` / `is_directory` / `invalid_path`
- [~] `FileInfo` / `ContextLine` / `GrepMatch` / `FileData`
- [~] `ReadResult`
  - [ ] 分頁欄位一致性驗證（`__post_init__` 規則）
- [~] `WriteResult` / `EditResult` / `DeleteResult` / `LsResult`
- [~] `GrepResult`、`GlobResult`、`GlobTruncationReason`
- [ ] `_apply_grep_max_count`
- [~] `FileUpload` / `FileUploadResponse` / `FileDownloadResponse`
- [~] `GrepOptions`（path / glob / max_count / context_lines）
- [~] `Backend` 介面（ls / read / write / edit / grep / glob / upload_files / download_files）
- [~] `Deleter` 可選能力 + `SupportsDelete`（`_supports_delete`）
- [~] `ExecuteResponse` / `ExecuteArtifact` / `ExecuteOffloadResult`
- [~] `SandboxBackend` 介面（id / execute）+ `SupportsExecution`
  - [x] `SupportsExecution` 對 CompositeBackend 依 default backend 判斷（`NewCompositeBackend` 依 default 的能力組出 wrapper）
- [x] `ArtifactsRooter` / `ArtifactsRoot`（沒有指定時為 `/`；trace wrapper 會轉發）
- [-] sync / async 雙版本（`als` / `aread` ...）— Go 以 `context.Context` 單一版本取代
- [-] `execute_accepts_timeout` / `_method_accepts_max_count` 反射檢查 — Go 介面簽名固定

### 3.2 RunContext `backend/runcontext.go`

- [x] `RunContext` / `WithRunContext` / `RunContextFrom`（取代 LangGraph `get_config()` / `get_store()`）

### 3.3 實作

- [x] `StateBackend` — py: `backends/state.py`；go: `backend/state.go`
  - [x] ls / read / write / edit / delete（目錄前綴遞迴）/ grep / glob
  - [x] upload_files / download_files（非 UTF-8 存成 base64）
  - [x] 透過 RunContext 讀寫 `files` state、產生 Pending update（同一次呼叫內 read-your-writes）；filesystem 工具、summarization offload、memory 載入都會設定 RunContext
- [~] `StoreBackend` — py: `backends/store.py`
  - [~] `NamespaceFactory`、`ValidateNamespace`
  - [ ] ls / read / write / edit / delete / grep / glob
  - [ ] upload_files / download_files
- [~] `FilesystemBackend` — go: `backend/filesystem.go`
  - [x] virtual_mode 路徑解析與逃逸防護（含 symlink 逃逸）
  - [x] ls / read（含 binary → base64）/ write / edit / delete
  - [~] grep：內建 Go 字面搜尋（跳過 `.git`、逾時截斷）
    - [ ] ripgrep 優先
    - [ ] context_lines
  - [x] glob（`**` / `{a,b}` 用 doublestar、逾時截斷）
  - [x] max_file_size_mb（grep 跳過過大檔案）
  - [~] 例外 → 標準錯誤碼（upload / download 已對應；symlink loop 由 `EvalSymlinks` 回報錯誤）
  - [x] upload_files / download_files
- [x] `LocalShellBackend` — py: `backends/local_shell.py`
  - [x] execute（timeout、max_output_bytes、env / inherit_env、cwd=root；stderr 行加 `[stderr]`、逾時 exit code 124；unix 上逾時或結束後殺整個程序群組、輸出邊讀邊截斷在 rune 邊界）
- [x] `CompositeBackend` — py: `backends/composite.py`；go: `backend/composite.go`
  - [x] 最長前綴路由（去掉前綴、結果路徑改寫回來）
  - [x] ls 合併（根目錄列出 route）
  - [x] grep / glob 跨 route 合併、路徑重寫、max_count 預算、截斷原因合併（route 依前綴長度排序，不是 Python 的插入順序）
  - [x] read / write / edit / delete 路由（route backend 不能刪時回錯誤；default 不能刪時整個 composite 不提供 delete）
  - [x] execute 委派 default
  - [x] upload / download 分組路由
  - [x] artifacts_root
  - [x] dcode：使用者 skills 目錄是唯讀 route（取代 `hostRoutes`），`artifactsRoot = /.deepagents`
- [~] `BaseSandbox` + `SandboxTransport` — py: `backends/sandbox.py`
  - [ ] ls / read / write / edit（inline 與 tmpfile 兩種）/ delete / grep / glob（以 python3 指令實作）
  - [ ] glob 指令模板（brace expansion、`**`、時間預算、MAX_MATCHES）
  - [ ] 輸出上限 `MAX_OUTPUT_BYTES` / `MAX_BINARY_BYTES`
  - [~] `ExecuteWithOffload`（capture-at-source：完整輸出存檔、只回傳 head/tail 預覽）
- [~] `LangSmithSandbox` — py: `backends/langsmith.py`
  - [ ] LangSmith sandbox transport 實作
- [~] `ContextHubBackend` — py: `backends/context_hub.py`
  - [ ] Hub repo 讀取、mutation queue、commit

### 3.4 Utils `backend/utils.go`（py: `backends/utils.py`）

- [x] 常數：`EmptyContentWarning` / `EmptyOldStringError` / `TruncationMarkerTemplate` / `MaxLineLength` / `ToolResultTokenLimit` / `TruncationGuidance` / `MaxVideoInputBytes`
- [x] `FileType` + `FileTypeForPath`（副檔名 → text / image / audio / video / pdf）
- [x] `ValidatePath`（allowed_prefixes、`..`、`~`、Windows 絕對路徑）
- [x] `ToPOSIXPath`
- [x] `FormatContentWithLineNumbers`（兩空白分隔、長行切成 N.1、N.2）
- [x] `CheckEmptyContent`
- [x] `CreateFileData` / `UpdateFileData` / `FileDataToString`
- [x] `NormalizeReadBounds` / `SliceReadResponse`
- [x] `PerformStringReplacement`（含 EOF 換行提示）
- [x] `TruncateIfTooLong`（字串版）
- [x] `SanitizeToolCallID`
- [x] `CompileGrepIncludeGlob`（doublestar；dotfile 規則與 wcmatch 略有差異）
- [x] `GrepMatchesFromFiles` / `GlobSearchFiles`
- [x] `GrepOutputMode` / `FormatGrepMatches`（files_with_matches / content / count、context 分組）
- [x] `RegexLiteralHint`
- [ ] `build_grep_results_dict`
- [x] `_glob_anchor`、`_paths_overlap` — go: `permissions.GlobAnchor` / `PathsOverlap`

---

## 4. Middleware — go: `middleware/`

### 4.1 Filesystem `middleware/filesystem`（py: `middleware/filesystem.py`）

- [x] `ToolName`（`FsToolName`）與 `AllTools`、`DefaultTools`（同 Python `tools=None`：全部工具；backend 不支援時略過 `delete` / `execute`）
- [x] `Options`（backend / system_prompt / custom_tool_descriptions / token eviction 門檻 / max_execute_timeout / grep_max_count / tools / permissions；Backend 必填）
- [x] `Middleware`（Name / Tools / StateSchema / WrapModelCall 附加 system prompt / WrapToolCall 截斷過長文字結果）
- [~] 工具實作 — go: `middleware/filesystem/tools.go`
  - [x] `ls`
  - [x] `read_file`（offset / limit 分頁、`@@ lines A-B of T | next offset N @@` 標頭、負 offset 通知、空檔提醒；超過 `MaxFileSizeMB` 的檔案拒讀並建議用 grep）
  - [x] `read_file` 多模態：image / audio / pdf 回傳 base64 content blocks
  - [ ] `read_file` 影片：抽幀（py: `_video.py`，需 ffmpeg 或等效函式庫）
  - [x] `write_file`
  - [x] `edit_file`（replace_all）
  - [~] `delete`（Deleter backend 預設提供；目錄遞迴刪除、symlink 只刪 link、拒絕刪根目錄；dcode 需核准（approve / reject）；permissions 以 write 檢查，目錄刪除會檢查所有可能重疊的 deny 規則）
  - [x] `glob`（截斷 note）
  - [x] `grep`（output_mode、max_count、regex 提示、截斷 note）
  - [~] `execute`（timeout 上限、ExecuteArtifact；offload 大輸出未做）
- [x] 工具 schema 與預設描述文字（`LsSchema` ... `ExecuteSchema`）
- [ ] system prompt：composite route host-path 說明（`_route_host_path_prompt`）
- [x] 非 sandbox backend 時移除 `execute`（在 `New` 決定工具清單）
- [x] 權限檢查：read / write / delete 規則、ls / glob / grep 結果過濾、無 path 的 glob 被拒時附提示；搭配有 execute 的 backend 時 `New` 回錯誤
- [x] 平行檔案異動保護（`_parallel_file_mutation_error`：同一輪對同一路徑的第二個以後的 write / edit / delete 回錯誤）
- [x] 大型 tool 結果 eviction 到 backend（`_message_eviction.py`：preview head/tail、`{artifactsRoot}/large_tool_results/`；Command 內的 tool message 也處理；寫入失敗或排除的工具退回截斷；門檻負值關閉）— go: `middleware/filesystem/eviction.go`
- [x] 大型 human message eviction（`{artifactsRoot}/conversation_history/{uuid}.md`、`lc_evicted_to` 標記、model request 用預覽、state 保留全文；agent loop 會替沒有 ID 的輸入訊息補 ID，讓標記版原地取代）
- [ ] media 結果重排到 tool results 之後、rejected content 替換
- [x] `files` state reducer（`StateSchema` 掛 `backend.FilesReducer`）；工具每次呼叫建立 `RunContext`，StateBackend 寫入經 `tool.Result.Update` 與 tool message 一起套用

### 4.2 Permissions `middleware/permissions`（py: `FilesystemPermission`, `_fs_interrupt.py`）

- [x] `Operation`（read / write；delete 歸 write，同 Python）、`Mode`（allow / deny / interrupt）
- [x] `FilesystemPermission` + `Validate`（開頭 `/`、不可含 `..` / `~`）
- [x] `Check`（first match wins，無匹配 = allow；doublestar 比對，`*` / `**` 會比對到 dotfile，比 wcmatch 嚴格）
- [x] `FilterPaths`（只濾 deny，interrupt 放行）
- [x] `DeleteDenyPatterns`（`_find_delete_deny_patterns`：遞迴刪除收集所有重疊的 deny 規則）
- [x] `BuildInterruptOn`（exact / bulk when-predicate、anchor 重疊判斷、glob 絕對 pattern 與 `..`）
- [x] `InterruptAsDeny`（Go 專屬：subagent 不能暫停時把 interrupt 規則轉 deny）

### 4.3 SubAgents `middleware/subagents`（py: `middleware/subagents.py`）

- [x] `TaskToolName`、`GeneralPurposeName` / `GeneralPurposeDescription` / `DefaultSubagentPrompt`
- [x] `Mode`（isolated / fork；legacy `handoff` = isolated）
- [x] `Spec` 介面、`SubAgent`、`CompiledSubAgent`（宣告式 spec 由 `New` 編譯；Skills / Permissions 由 CreateDeepAgent 套到 subagent 的 middleware stack）
- [x] `Options` / `Middleware` / `AddPrivateStateKeys`
- [x] `ValidateSpecs`（名稱唯一、mode 合法、fork 不可有 skills；fork 回 `ErrNotImplemented`）
- [x] `task` 工具（同一輪多個 task 平行執行）
  - [x] `TaskToolSchema`（description / subagent_type，拒絕未知欄位）
  - [x] `TASK_TOOL_DESCRIPTION` + `{available_agents}` 清單
  - [x] isolated：只傳 task 描述，排除 `_EXCLUDED_STATE_KEYS` 與 private keys
  - [ ] fork：傳 parent 有效訊息、`_FORK_TASK_PREAMBLE`、防止遞迴 fork
  - [x] 結果回傳：structured_response JSON 或最後一則非空 AI 訊息
  - [x] subagent 的 state update 回寫 parent（files 等）
  - [ ] response_format（per-subagent）
  - [x] 未知 subagent_type 錯誤
  - [x] 背景執行（Go 版延伸，參考 Claude Code）：`Options.Background` 加 `run_in_background` 與 `task_output`；完成時 BeforeModel 注入一次 `<task-notification>`；AfterAgent 等待未完成的 task；取消 run 連帶取消；registry 只在記憶體、不轉發事件
- [~] 事件 namespace / tracing metadata（`ls_agent_type`）— 串流時 subagent 的 values 事件以 `task:<tool call id>` namespace 轉發給 parent；tracing metadata 未做

### 4.4 AsyncSubAgents `middleware/asyncsubagents`（py: `middleware/async_subagents.py`）

- [~] `AsyncSubAgent`（name / description / graph_id / url / headers）
- [~] `Task`、`TasksStateKey`
- [~] `Client` 介面（LangGraph SDK threads / runs 子集）
- [~] `Middleware`（Tools / StateSchema / WrapModelCall）
- [ ] `start_async_task`
- [ ] `check_async_task`
- [ ] `update_async_task`
- [ ] `cancel_async_task`
- [ ] `list_async_tasks`
- [ ] client 快取（依 url + headers）
- [ ] system prompt 注入

### 4.5 Summarization `middleware/summarization`（py: `middleware/summarization.py`）

- [~] `ContextSize` / `TriggerClause` / `TruncateArgsSettings` / `Event` / `Defaults`
- [~] 私有 state keys `_summarization_event` / `_summarization_session_id`
- [~] `Options` / `New` / `NewForModel` / `ComputeDefaults`
- [~] `Middleware`（Name = `SummarizationMiddleware`）
- [~] `ToolMiddleware`（`compact_conversation` 工具）
- [x] `DEEPAGENTS_DEFAULT_SUMMARY_PROMPT` — go: `DefaultSummaryPrompt`
- [x] 觸發判斷（tokens / messages / fraction；AND / OR 組合）
- [x] cutoff 計算（不切斷 tool call / tool result 配對）
- [x] 舊 tool args 截斷（write_file / edit_file）
- [x] 產生摘要（trim_tokens_to_summarize）
- [x] 被移除訊息 offload 到 `/conversation_history/{session_id}.md`（dcode 用 `/.deepagents/conversation_history`）
- [ ] inline media（data URL）offload 到 backend
- [x] summary 訊息帶檔案路徑、過濾舊 summary
- [x] context overflow 錯誤 → 摘要後重試一次（`_call_with_budget`）
- [x] 摘要模型失敗（非 overflow / 非取消）時改用指向 offload 檔的訊息繼續，不中止 run（Go 版延伸，Stage 16）
- [x] `Compact`（手動摘要，供 dcode `/compact`；nothing-to-compact 回 `ErrNothingToCompact`）
- [ ] `_overflow_clip.py`：overflow 門檻推導
- [ ] `compact_conversation`：資格判斷、nothing-to-compact、錯誤處理

### 4.6 Memory `middleware/memory`（py: `middleware/memory.py`）

- [x] `ContentsStateKey`（private）、`Options`、`Middleware`（BeforeAgent / WrapModelCall）
- [x] `MEMORY_SYSTEM_PROMPT` 模板（需含 `{agent_memory}`）
- [x] BeforeAgent：依序載入 sources（每 thread 一次，檔案不存在略過）
- [x] 去除 HTML 註解
- [~] 注入 system prompt（Anthropic cache_control breakpoint 待 Anthropic provider）

### 4.7 Skills `middleware/skills`（py: `middleware/skills.py`）

- [x] 限制常數（檔案大小、name / description / compatibility 長度、warning 數量）
- [x] `Metadata`（`SkillMetadata`）、`Source`、`SourcesFromPaths`
- [x] `Middleware`（BeforeAgent / WrapModelCall / StateSchema；`skills_metadata` 為 nil 時才重新載入）
- [x] `ParseMetadata`、`ListSkills`
- [x] SKILL.md frontmatter（YAML）解析
- [x] name 驗證（長度、字元、與目錄名一致；不合規只記 log，仍載入）
- [x] allowed-tools 解析
- [x] 多來源覆蓋（後者優先）、source label 推導
- [x] 載入錯誤收集與截斷
- [x] `SKILLS_SYSTEM_PROMPT` + skills 清單 / 位置 / 警告格式化
- [ ] `KeySpec.OmitFromOutput` 尚未在 runtime 生效：`skills_metadata` 仍出現在 `Invoke` 回傳的 state（subagent 已靠排除清單不回傳）

### 4.8 Rubric `middleware/rubric`（py: `middleware/rubric.py`）

- [~] `Result`（satisfied / needs_revision / failed / max_iterations_reached / grader_error）
- [~] `CriterionEval` / `GraderResponse` / `Evaluation`
- [~] State keys（rubric 公開、其餘 private）
- [~] `Options` / `Middleware`（BeforeAgent / AfterAgent）
- [ ] `GRADER_SYSTEM_PROMPT`
- [ ] grader subagent 建立與 structured output 策略
- [ ] transcript 建構（最多 30 則、每則 4000 字、payload 淨化）
- [ ] revision prompt 與重入迴圈
- [ ] max_iterations、coverage retry、grader 例外處理
- [ ] on_evaluation callback、trace metadata

### 4.9 其他 middleware

- [x] PatchToolCalls `middleware/patchtoolcalls`（py: `patch_tool_calls.py`）
  - [x] BeforeAgent：補上 dangling tool call 的取消訊息（接在 filesystem 之後、caller middleware 之前）
- [~] UnsupportedContent `middleware/unsupportedcontent`（py: `unsupported_content.py`）
  - [ ] 依 ModelProfile 判斷 block 是否支援（含 tool message 內）
  - [ ] OpenAI Responses 例外、read_file placeholder
- [x] HumanInTheLoop `middleware/hitl`（langchain `HumanInTheLoopMiddleware`）
  - [x] `InterruptOnConfig` / `DecisionType` / `ActionRequest` / `Decision`（`AllowedDecisions` 預設 approve / reject）
  - [x] AfterModel：對符合的 tool calls 發出 interrupt；`hitl_auto_approve` 設定時略過
  - [x] approve / reject（reject 產生 error tool message，可附理由）
  - [x] edit（以 `EditedArgs` 取代最後一則 AI 訊息中的 tool call args，同一 message ID 經 reducer 原地取代後照常執行；model 回覆沒有 ID 時由 agent loop 補上）/ respond（不執行工具，回傳 reviewer 訊息作為成功的 tool message）；dcode TUI / headless 尚無 edit UI
  - [x] `Merge`（`_merge_fs_interrupt_on`）
  - [x] `Describe`：每個 call 各自產生 description（Python callable `description`）
- [x] TodoList `middleware/todolist`（langchain `TodoListMiddleware`）
  - [x] `write_todos` 工具與 system prompt；同一輪多次呼叫全部回錯誤；TUI 以 checklist 顯示
  - [x] 擴充（Go 版延伸，Python 沒有）：`id`、`notes`、`cancelled`，`merge: true` 依 id 更新；prompt 要求先探索再列具體 todo，不確定的列成 `Investigate:`
  - `interrupt_on` 的 `Always`：自動核准時仍中斷（`ask_user` 使用）
- [x] PlanMode `middleware/planmode`（Go 版延伸，參考 Cursor Plan 模式）：plan 模式只開唯讀工具；`create_plan` 存進 Postgres（`dcode_plans`，`planmode.Store`）並存進 state；`build_plan` 把計畫 todo 種進 todo list；dcode 的 `ask_user`（`code/askuser`）以 HITL respond 收答案；GUI / TUI / REPL 支援 `/plan`、`/build`
- [~] PromptCaching `middleware/promptcaching`（py: `_prompt_caching.py`）
  - [~] `AppendDefaults`
  - [ ] Anthropic cache_control 標記
  - [ ] Bedrock 標記
  - [ ] Fireworks 標記
- [x] ToolExclusion `middleware/toolexclusion`（py: `_tool_exclusion.py`）
  - [x] WrapModelCall 過濾工具
- [x] `middleware/_utils.append_to_system_message` — go: `agent.AppendToSystemMessage`
- [-] `middleware/permissions.py`（Python 僅為 re-export 模組）

---

## 5. Profiles — go: `profiles/`

- [~] `GeneralPurposeSubagentProfile`（enabled / description / system_prompt）
- [~] `HarnessProfile`（base_system_prompt / system_prompt_suffix / tool_description_overrides / excluded_tools / excluded_middleware / extra_middleware / general_purpose_subagent）
- [~] `HarnessProfileConfig` + `ToProfile`（宣告式子集）
- [ ] `HarnessProfileConfig` 從 YAML / JSON 載入與欄位驗證（`_coerce_*`）
- [x] `RegisterHarnessProfile`（additive merge）
- [x] `HarnessProfileFor`（provider → provider:model 疊加）
- [x] `MergeHarnessProfiles`（`_merge_profiles`、middleware 依 Name 合併）
- [x] `ApplyPrompt`（`_apply_profile_prompt`）
- [~] `ProviderProfile`（init_kwargs / pre_init / init_kwargs_factory）
- [~] `RegisterProviderProfile` / `ProviderProfileFor`
- [~] `ResolveModel`（`resolve_model`）
- [~] `RegisterBuiltins`
- [ ] 內建 provider profiles
  - [ ] nvidia — py: `profiles/provider/_nvidia.py`
  - [ ] openai — py: `profiles/provider/_openai.py`
  - [ ] openrouter — py: `profiles/provider/_openrouter.py`
- [ ] 內建 harness profiles
  - [ ] anthropic claude-haiku-4-5 — py: `profiles/harness/_anthropic_haiku_4_5.py`
  - [ ] anthropic claude-sonnet-4-6 — py: `profiles/harness/_anthropic_sonnet_4_6.py`
  - [ ] anthropic claude-opus-4-7 — py: `profiles/harness/_anthropic_opus_4_7.py`
  - [ ] nvidia nemotron-3-ultra — py: `profiles/harness/_nvidia_nemotron_3_ultra.py`
  - [ ] openai codex（含 TodoListMiddleware）— py: `profiles/harness/_openai_codex.py`
- [-] entry-point plugin 載入（`_invoke_profile_plugins`）— Go 以編譯期註冊取代

---

## 6. CLI（libs/code）— go: `cmd/dcode`, `code/`

### 6.1 進入點與架構

- [x] `cmd/dcode/main.go`（flag：`-n` 或位置參數 prompt、`-model`、`-thread`、`-r`、`-y`、`-plain`、`-server`、`-version`、`-q`、`-no-stream`、`-timeout`；子命令 `serve`、`threads [-json]`）
- [ ] 完整 CLI 參數（`main.py`：agent 選擇、sandbox、list / reset agents ...）
- [~] Server `code/server`（`Server` / `Service` / `Config` / `RunRequest` / `Event` / `New`）
  - [x] 每個 run 可換 model：`RunRequest.Model` → thread 記住的 model（`sessions.Info.Model`、Postgres `dcode_threads.model`）→ 預設；agent 不重建（`switchModel` 從 ctx 取 run 的 model，subagent 跟著切換），回覆 `Metadata["model_name"]` 標記實際 model；`ThreadState.Model`、`/healthz` 回報預設 model；遠端 `-model` 不再被忽略
  - [x] `Service`：依 cwd（與 interactive）快取 agent、共用 model 與 checkpointer；thread 鎖（`ErrThreadBusy`）、thread 綁定 cwd（`ErrThreadCwdMismatch`）、待核准時拒收新訊息（`ErrPendingApproval`）
  - [x] 事件協定：`delta` / `message`（只送本次新增）/ `interrupt` / `done` / `error`
  - [x] thread 儲存：`PostgresConn` → Postgres checkpointer + `dcode_threads`；否則 in-memory
  - [x] HTTP（`dcode serve`，只綁 loopback）：`GET /healthz`、`POST /runs`（NDJSON；409 / 400）、`GET /threads`、`GET /threads/{id}`；client 斷線會取消 run — go: `code/server/http.go`
  - [x] 關閉：進行中的 run 有 30 秒收尾，之後取消；每次事件寫入 30 秒逾時；再按一次 Ctrl-C 強制結束
  - [x] tool / node panic 轉成錯誤，不會打掛 serve
  - [~] server runtime 組裝（model、tools、MCP、sandbox、graph）— code: `server_graph.py`；目前 model + `CreateCLIAgent`
  - [ ] 子行程啟動 / 重啟 / 停止 — code: `client/launch/server*.py`
- [~] Client `code/client`
  - [~] `RunNonInteractive`（headless）— code: `client/non_interactive.py`
    - [x] 即時印出 assistant 文字（串流）、tool calls 與失敗的 tool 寫到 stderr、錯誤回 exit code 1
    - [x] `-thread` 續跑；沒給就新建並印出 `thread: <id>`
    - [x] 核准：`-y` 自動核准；stdin 是 TTY 時逐一 y / a / N / 理由；否則拒絕並告知模型
    - [x] `-q`（只輸出回覆）、`-no-stream`（完成後一次寫出）、`-timeout`（exit 124）、Ctrl-C（exit 130）
    - [-] JSON 事件串流 — Python `-n` 也沒有；`dcode threads -json` 用 Python 的 `{"schema_version":1,...}` envelope
  - [x] `RunInteractive`：line REPL（非 TTY 或 `-plain`），slash 指令、Ctrl-C 只取消當前 run
  - [x] remote client `client.Remote`（`DCODE_SERVER`）— code: `client/remote_client.py`
  - [x] `PrintThreads`（`dcode threads [-all]`）
- [~] TUI `code/tui`（Bubble Tea；Python：Textual `app.py` + `tui/*`）
  - [x] 輸入框、transcript viewport（串流字、tool 行、錯誤）、狀態列（cwd / thread / model / auto-approve / spinner）
  - [x] approval 框（write / edit / execute 預覽；a / r + 理由 / A / Ctrl-C）
  - [x] `/new` `/thread` `/threads`（picker）`/resume` `/yolo` `/help` `/quit`；Ctrl-C 取消 run，閒置時離開
  - [x] markdown（glamour，完成的 assistant 訊息）、核准框 unified diff（write / edit 對照磁碟檔案）
  - [x] 效能：已完成訊息依寬度快取、內容沒變就不重排、捲上去時串流不拉回底部
  - [x] 呼叫樹：main / subagent 標示；subagent（#1、#2… 各自顏色）的 LLM 與 tool 呼叫巢狀顯示在 task 列下並附報告摘要；進行中的列顯示 spinner 與經過時間、thinking 列；狀態列顯示執行中的 subagent 數
  - [ ] 自動完成、貼上收合、ask_user
  - [ ] model / agent / theme 選擇器、context 用量、通知中心
- [x] GUI `code/gui` + `code/gui/web`（Stage 9；Python 沒有對應功能）
  - [x] trace `code/trace`：完整 LLM request / 回覆、檔案 unified diff、subagent 訊息，存在 Postgres（`trace.Log`：`dcode_trace_records` / `dcode_trace_blobs`）、內容去重；`fetch_url` 擷取呼叫記在 `web_extract:<call id>`
  - [x] 內嵌 React SPA、loopback + token + Origin 檢查、遠端 server 走 reverse proxy；`dcode` 預設開啟，`-tui` / `-plain` / `-no-open`
  - [x] timeline（TUI 同款收合樹、完整歷史虛擬捲動、串流、核准卡片）、各 tool 詳情、unified / split diff、LLM 檢視器、Files changed
  - [x] thread 側欄、過濾 / 搜尋、快捷鍵、`make web` / `make web-test`
  - [x] 重啟後自動開啟目前目錄最近的 thread、側欄重新命名（`PATCH /threads/{id}`）

### 6.2 Agent `code/agent`（code: `agent.py`）

- [~] `CreateCLIAgent` / `Options`（LocalShellBackend virtual mode 以 cwd 為根 + system prompt + TodoList + Memory（`~/.deepagents/<agent>/AGENTS.md`、`/.deepagents/AGENTS.md`、`/AGENTS.md`；headless 用 `_MEMORY_HEADLESS_SYSTEM_PROMPT`）+ HITL `DefaultInterruptOn`（含 web_search / fetch_url / Context7、啟動 web-researcher 的 task，description 照 Python 格式）+ `WebTools`（有 `BRAVE_API_KEY` 才掛 web_search，有 `CONTEXT7_API_KEY` 才掛 Context7，fetch_url 一律掛）+ `web-researcher` subagent + 背景 task（`deepagents.WithBackgroundTasks`）+ 儲存（`Plans`、`Artifacts`、`Cache`、`WebModel`）+ SkillsMiddleware（`SkillSources`，見 6.10）；MCP 未接）
- [~] `SystemPrompt`（headless / interactive、model identity、cwd 與虛擬路徑說明、Project Context、Looking Things Up 段落（本地 → Context7 → web_search → fetch_url(prompt) → web-researcher）與 web search 段落（有 key 才加，另說明 freshness / result_filter / offset / summary）；skills 段落由 SkillsMiddleware 注入）— go: `code/agent/system_prompt.md`
- [~] `ListAgents` / `ResetAgent`
- [x] shell allow-list（`IsShellCommandAllowed`，移植 `contains_dangerous_patterns` / `is_shell_command_allowed`）— 以 hitl `When` 讓符合的 execute 免核准，`all` 取消 execute 核准；取代 `ShellAllowListMiddleware` stub
- [ ] 任務錯誤格式化（`_format_task_error`）
- [ ] subagents 注入 fs tools
- [ ] rubric grader 工具與 prompt
- [ ] PTC（programmatic tool calling）選項

### 6.3 設定 `code/config`（code: `config.py`, `configuration/*`, `_paths.py`, `_env_vars.py`）

- [x] `Scope`、`Settings`、`Source`、`Paths`、`Resolver`、`NewResolver`（使用者層 + env）
- [x] `DefaultPaths`（`~/.deepagents`、`cwd/.deepagents`；尚未處理 profile state dir）
- [x] `config.SetDefaultModel`：只改 `models.default` 一行（保留註解與其他 key），前後都要能解析才寫入
- [x] config.toml 解析（`github.com/BurntSushi/toml`）：`models.default`、`startup.mode`（manual / yolo，預設 yolo）、`shell.allow_list`（陣列、逗號字串、`recommended`、`all`）、`retries.max_retries`、`server.url`；其餘 key 放 `Raw`；錯誤帶行號
- [~] 分層合併：flag > env（`DCODE_MODEL`、`DCODE_SERVER`、`DEEPAGENTS_CODE_SHELL_ALLOW_LIST`、`BRAVE_API_KEY` → `WebSearchAPIKey`、`CONTEXT7_API_KEY` → `Context7APIKey`；config.toml 另有 `models.web`、`storage.results_ttl`）> 使用者 config.toml > 預設；managed / project / session 層未做
- [x] .env 載入（shell > 最近的專案 `.env` > `~/.deepagents/.env`）— go: `code/config/dotenv.go`
  - [ ] 拒絕危險 key（`_DOTENV_DENIED_ENV_KEYS`、專案 `.env` 不可設定的 LangSmith key）
  - [ ] 變數內插（`${VAR}`）、`/reload` 時移除先前注入的值
- [ ] `/reload` 產生新 generation
- [ ] 環境變數對照表

### 6.4 Sessions `code/sessions`（code: `sessions.py`）

- [x] `Info` / `Filter` / `Store` / `Latest` / `NewThreadID` / `Title`
- [-] SQLite schema — 改用 Postgres `PostgresStore`，嵌入／測試服務可用 `MemoryStore`
- [x] `SetTitle`：更新已存在 thread 的名稱，不被後續 `Touch` 覆蓋
- [x] list / most recent / get / delete；thread 綁定 cwd
- [x] message count、title（第一則 prompt 前 60 字）
- [ ] find similar（打錯 thread id 時提示）
- [x] `threads` 子命令輸出

### 6.5 工具 `code/tools`（code: `tools.py`）

- [x] `NewWebSearch`（Brave Search，與 Python 的 Tavily 不同）— go: `code/tools/brave.go`
  - [x] 所有 web/search 參數都是 tool 參數（country / search_lang / ui_lang enum、count、offset、safesearch、spellcheck、freshness、text_decorations、result_filter、units、goggles、extra_snippets、summary、enable_rich_callback、include_fetch_metadata、operators）；`location` → `x-loc-*` header；`no_cache` → `Cache-Control`；沒給的參數不送
  - [x] 送出前驗證（型別、範圍、enum、未知欄位、query 600 字元 / 75 字）
  - [x] summary 自動打 summarizer、enable_rich_callback 自動打 /web/rich；後續呼叫失敗只附 note
  - [x] 401 / 403 / 422 / 429（Retry-After）與缺 key 轉成 error tool message
  - [x] 輸出：query、summary、rich、web、news、videos、discussions、faq、infobox、locations
  - [x] `count` 預設 8、`allowed_domains` / `blocked_domains`（轉 `site:` / `-site:`）、Redis 快取 15 分鐘
- [x] `NewFetchURL` — go: `code/tools/fetch.go`（輸出為文字而非 Python 的 dict）
  - [x] `prompt`：以 `models.web` 擷取（`<web_content>` 框隔、每頁一次、15 分鐘快取），完整頁面存 `/.deepagents/web/`，擷取失敗退回原文
- [x] Context7 `resolve_library_id` / `query_docs` — go: `code/tools/context7.go`（REST v2、錯誤碼對應、1 小時快取）
- [x] SSRF 防護（只允許 http / https、封鎖非公開 IP 含 IPv4-mapped / 6to4、連線釘在驗證過的 IP、不走 proxy、每個 redirect 重新驗證、上限 5 次）
- [x] HTML → markdown（`html-to-markdown/v2`，相對連結轉絕對）；純文字 / JSON 原樣回傳；回應上限 5 MiB
- [~] approval 的 URL 警告：只檢查隱藏字元與 punycode 解碼（Python `unicode_security` 其餘規則未移植）

### 6.6 Approval `code/approval`（code: `approval_mode.py`, `auto_mode.py`）

- [~] `Mode`（manual / auto / yolo）、`Next`、`Read`、`Write`、`Verdict`、`Classifier`
- [ ] 每 thread 模式存 store
- [ ] YOLO 確認檔、auto mode notice
- [ ] Auto mode classifier（安全判斷模型）

### 6.7 Slash commands `code/commands`（code: `command_registry.py`）

- [x] `Catalog`：46 個公開 + 2 個隱藏指令資料（有測試）
- [x] `Registry.Register` / `Lookup` / `Dispatch` / `Help` / `Suggest`（alias、未知指令附編輯距離建議、隱藏指令不列出）
- [x] `commands.DCode`：dcode 實作的指令；TUI / REPL 經 `client.NewCommandRegistry(Session)` 共用 handler，GUI 由 `GET /commands` 取清單做自動完成與 `/help`
- [ ] 各指令實作
  - [ ] `/agents`
  - [ ] `/auth`（`/connect`）
  - [ ] `/auto`
  - [ ] `/auto-update`
  - [ ] `/btw`
  - [ ] `/changelog`
  - [x] `/clear`（dcode 的 `/new` alias）
  - [x] `/context`（最近一次呼叫的 input：system prompt + tools 與對話的估算、剩餘空間）
  - [ ] `/context-doctor`
  - [ ] `/copy`
  - [x] `/cost`（各模型 calls / input（含 cached）/ output / 預估金額；未定價模型另列，不算成 0）
  - [ ] `/docs`
  - [ ] `/editor`
  - [ ] `/effort`
  - [ ] `/extensions`
  - [ ] `/feedback`
  - [ ] `/force-clear`
  - [ ] `/goal`
  - [x] `/help`
  - [ ] `/install`
  - [ ] `/line-numbers`
  - [ ] `/manual`
  - [ ] `/mcp`
  - [x] `/model [provider:model] [--default]`（TUI / REPL / GUI；之後的 run 帶 `RunRequest.Model`；`--default` 只改 config.toml 的 `models.default` 一行；切回既有 thread 時沿用它記住的 model）
  - [ ] `/notifications`
  - [x] `/offload`（`/compact`）— `Server.Compact` / `POST /threads/{id}/compact`
  - [ ] `/plugins`
  - [ ] `/prompts`
  - [x] `/quit`（`/q`、`/exit`）
  - [ ] `/reload`
  - [ ] `/remember`
  - [ ] `/restart`
  - [ ] `/rubric`（`/criteria`）
  - [ ] `/scrollbar`
  - [ ] `/skill-creator`
  - [ ] `/summarization-model`
  - [ ] `/theme`
  - [x] `/threads`
  - [ ] `/timestamps`
  - [x] `/tokens`（`used / limit (pct%) · model`；TUI / GUI 狀態列顯示 context 百分比）
  - [ ] `/tools`
  - [ ] `/trace`
  - [ ] `/uninstall`
  - [ ] `/update`
  - [ ] `/version`（`/about`）
  - [x] `/yolo`（切換 auto-approve）
  - [ ] `/debug`（隱藏）
  - [ ] `/debug-error`（隱藏）

### 6.8 MCP `code/mcp`（code: `mcp_*.py`, `mcp_providers/*`）

- [~] `Transport`、`ServerConfig`、`LoadConfigs`、`Session`、`Connect`、`TokenStore`
- [ ] MCP client（stdio / http / sse）
- [ ] MCP tools → `tool.Tool` 轉換
- [ ] OAuth 登入流程與 token 儲存
- [ ] 停用清單、read-only 工具判斷
- [ ] 內建 providers（GitHub、Slack）
- [ ] MCP middleware / proxy

### 6.9 Hooks `code/hooks`（code: `hooks/*`, HOOKS.md）

- [~] `Event`（12 個事件）、`Hook`、`Outcome`、`Engine`、`Load`
- [ ] hooks.json 載入（project → user → plugin 優先序）、legacy 格式遷移
- [ ] handler 執行（stdin JSON、exit code 2 = block、JSON stdout）
- [ ] workspace trust
- [ ] server-side hook middleware

### 6.10 Skills / Subagents 載入

- [x] dcode 的 SkillsMiddleware 來源（`code/agent/skills.go`）：User Deepagents（`~/.deepagents/<agent>/skills`）→ User Agents（`~/.agents/skills`）→ Project Deepagents（`/.deepagents/skills`）→ Project Agents（`/.agents/skills`）；不存在的目錄略過；使用者層目錄經唯讀 host route 給檔案工具讀取
- [~] `code/skills`：`Location`、`Skill`、`Discover`、`InvocationPrompt`
  - [ ] built-in skills（skill-creator、thread-inspector）
  - [ ] skill trust、slash command 化
- [~] `code/subagents`：`Metadata`、`List`、`LoadAsync`
  - [ ] markdown + frontmatter 解析

### 6.11 Sandbox `code/sandbox`（code: `integrations/sandbox_*.py`）

- [~] `Config`、`Provider`、`Register`、`Open`
- [ ] LangSmith provider
- [ ] Daytona / Modal / Runloop / Vercel / AgentCore providers（見 7. partners）
- [ ] setup script、workspace 綁定

### 6.12 其他 CLI 子系統

- [~] Goal `code/goal`（`Goal`、`ToolsMiddleware`、`update_goal`）— code: `goal_*.py`
  - [ ] goal rubric、state limits、notice
- [~] Offload `code/offload`（`Result`、`Run`）— code: `offload*.py`
- [x] Cost `code/cost`（`Price`、`Table`、`LoadTable`（內嵌 `prices.json` + `~/.deepagents/prices.json` 覆寫）、`Lookup`（spec → 模型名 → `-` 邊界前綴）、`Estimate`（cache read / write 分開計價，查不到回 `ErrUnpriced`））— code: `cost_tracking.py`
- [x] `GET /threads/{id}/usage` / `Server.GetUsage`：由 checkpoint 訊息依 `model_name` 分模型加總（只含主 agent；subagent 呼叫不在 thread state）
- [~] Plugins `code/plugins`（`Manifest`、`Store`、`OpenStore`）— code: `plugins/*`
  - [ ] marketplace、discovery、substitution
- [~] Extensions `code/extensions`（`Extension`、`Register`、`Loaded`）— code: `extensions/*`
- [ ] model config / 切換 / reasoning effort — code: `model_config.py`, `configurable_model.py`, `reasoning_effort.py`
- [x] model retry — code: `model_retry.py`；go: `llm/transport.go`（429 / 5xx / 連線錯誤指數退避、Retry-After、`retries.max_retries`；串流閒置 5 分鐘逾時）
- [x] 空回應重試 — go: `llm/openai.go`（200 但 0 choices、或串流什麼都沒送出 → 依 `max_retries` 退避重試；串流已輸出就不重試）
- [ ] auth store / 顯示 — code: `auth_store.py`, `auth_display.py`
- [ ] OpenAI Codex 整合 — code: `integrations/openai_codex.py`
- [ ] btw（旁支問題）— code: `btw*.py`
- [ ] ask_user 工具 — code: `ask_user.py`
- [~] local context（git、專案資訊）— code: `local_context.py`, `_git.py`, `project_utils.py`；go: `code/agent/localcontext.go`（git 分支、`git status` 前 20 筆、專案標記檔；project root 用 cwd，不找 git root）
- [ ] workspace / repository bounds — code: `workspace.py`, `_repository_bounds.py`
- [ ] memory guard — code: `memory_guard.py`
- [ ] session stats / tokens — code: `_session_stats.py`
- [ ] context doctor — code: `context_doctor.py`
- [ ] doctor 命令 — code: `doctor.py`
- [ ] update check / 自動更新 — code: `update_check.py`
- [ ] tool catalog / managed tools — code: `tool_catalog.py`, `managed_tools.py`
- [ ] tracing（LangSmith）— code: `_tracing.py`
- [ ] notifications — code: `notifications.py`
- [ ] clipboard / editor / 主題 — code: `clipboard.py`, `editor.py`, `theme.py`
- [ ] unicode 安全檢查、terminal escape — code: `unicode_security.py`, `terminal_escape.py`
- [ ] ACP 模式（`dcode --acp`）— code: `acp.py`
- [ ] cold cache — code: `cold_cache.py`
- [ ] state migration — code: `state_migration.py`
- [-] `_textual_patches.py`、`iterm_cursor_guide.py`、`_dep_floor_check.py` — Python / Textual 特有

### 6.13 超出 Python 版的功能：結構化 coding tools `code/codetools`（Stage 11）

Python 版沒有這些工具；它們把凌亂的指令輸出整理成 JSON 再回給 LLM。

- [x] `git_status`（porcelain v2 → branch / modified / added / deleted / renamed / untracked / conflicted）
- [x] `git_diff`（每檔 path / status / additions / deletions / diff；含 untracked、沒有 HEAD、輸出上限）
- [x] `apply_patch`（Codex 格式、逐層放寬比對、全有或全無（寫入失敗會 rollback）、HITL diff 預覽、GUI 預覽）
- [x] `run_tests`（Go：`go test -json` → passed / failed / failures 帶 file:line）
- [x] `diagnostics`（Go：`go build` + `go vet` → file / line / column / message）
- [x] 其他語言的 run_tests / diagnostics（Stage 12：mocha / jest / vitest / npm test、pytest；tsc / eslint、ruff / 語法檢查）
- [x] 減少 unsupported：`dir` 參數與子目錄專案自動偵測（`code/codetools/target.go`）、無 tsconfig/ESLint 時 `node --check` 語法檢查、無 pytest 時改用 unittest
- [x] headless 驗證閘門 `VerifyGate`（Stage 12：改檔後沒有通過的測試 / build 就擋下結束，最多 2 次；ECONNREFUSED 服務啟動提示）
- [x] 驗證閘門不接受帶測試名稱過濾的 run（Stage 16：`run_tests run=...`、`--grep` / `-g` / `-t` / `-run` / `-k` / `file.py::test`，要求不帶過濾跑同一批測試檔）
- [ ] agent 宣稱失敗與改動無關時，要求用 `git stash` 在原始程式碼上重跑證明（Stage 16 bd80 trace）
- [ ] 服務啟動提示不在工作目錄留檔（`redis-server` 的 `dump.rdb` 被收進 patch）
- [x] `edit_file` 多處相符時列出行號與前一行（與 Python 版不同：Python 只回報次數）；`apply_patch` 對不上時指出最接近的行
- [ ] 驗證閘門的 `execute` 判斷目前靠指令 regex，不認得的測試指令（例如自訂 script 名稱）不算驗證
- [ ] symbol 導航（find_symbol / find_definition / find_references，LSP）

---

## 7. 未來項目（本階段不實作）

- [ ] ACP connector（Zed Agent Client Protocol）— py: `libs/acp/deepagents_acp`（`server.py`, `utils.py`）
- [ ] Talon（多頻道 agent host）— py: `libs/talon/deepagents_talon`
  - [ ] host / runtime
  - [ ] channels：Slack / Discord / Telegram / WhatsApp
  - [ ] cron 排程（expression / jobs / scheduler / tools）
  - [ ] history（backends / vectors / embeddings / postgres / profiles）
  - [ ] archive、pairing、tool approvals、MCP、speech、media、observability
- [-] Evals — py: `libs/evals`（harbor adapters、benchmarks、radar、scorecard）— 改以 `dbench`（`cmd/dbench`、`code/bench`）跑 SWE-bench Pro V2：固定 smoke50、官方 verifier 重新評分、結果與過程指標寫進 Postgres、報表與同題比較
- [ ] Partners sandbox 整合
  - [ ] Daytona — py: `libs/partners/daytona`
  - [ ] Modal — py: `libs/partners/modal`
  - [ ] Runloop — py: `libs/partners/runloop`
  - [ ] Vercel — py: `libs/partners/vercel`
  - [ ] QuickJS（REPL / PTC middleware）— py: `libs/partners/quickjs`
