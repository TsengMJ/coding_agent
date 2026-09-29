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

- 最後更新：2026-09-29（Stage 2 完成：agent loop、OpenAI Chat Completions、`make run`）
- 核心 SDK：runtime reducer、agent loop、FakeModel、OpenAI Generate 已實作；middleware、backend、checkpointer 仍為 stub
- CLI：headless `dcode -n` 經 in-process server 可跑；TUI、設定檔、sessions 仍為 stub

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

## 1. Runtime 基礎（取代 langchain_core / LangChain / LangGraph）

### 1.1 訊息 `llm/message.go`（langchain_core.messages）

- [~] `Role`（system / user / assistant / tool）
- [~] `ContentBlock`（text / image / audio / video / file / reasoning、base64、url、mime_type、cache_control）
- [~] `ToolCall`（id / name / args）
- [~] `Usage`（input / output / total / cache read / cache write）
- [~] `Message`（SystemMessage / HumanMessage / AIMessage / ToolMessage 合一）
- [~] 建構函式 `NewSystemMessage` / `NewUserMessage` / `NewAssistantMessage` / `NewToolMessage` / `TextBlock`
- [x] `Message.Text()`：串接文字內容
- [ ] JSON 序列化 / 反序列化（checkpoint 用）
- [ ] 訊息 ID 自動產生

### 1.2 ChatModel `llm/model.go`, `llm/registry.go`, `llm/providers.go`

- [~] `ChatModel` 介面（`Info` / `Profile` / `Generate` / `Stream`）
- [~] `ChatRequest` / `ToolSpec` / `ResponseFormat` / `Chunk`
- [~] `ModelInfo`、`ModelProfile`（max_input_tokens、多模態支援旗標）
- [~] `TokenCounter` 型別
- [~] `ApproximateTokenCounter`（`count_tokens_approximately`）
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
- [~] OpenAI provider — go: `llm/openai.go` `OpenAIModel`（stdlib `net/http`，`OPENAI_API_KEY` / `OPENAI_BASE_URL`）
  - [ ] Responses API（預設）
  - [x] Chat Completions API（Generate）
  - [ ] Stream
  - [x] tool calling
  - [ ] `store=False` + `reasoning.encrypted_content`
- [ ] OpenRouter provider（OpenAI 相容 + app attribution headers）
- [ ] NVIDIA provider
- [ ] Google Gemini provider
- [ ] Ollama provider
- [ ] Bedrock provider
- [ ] Fireworks provider
- [x] FakeModel（測試用：預先編排回應 / tool calls）— go: `llm/fake.go`
- [ ] context overflow 錯誤型別（`ContextOverflowError`，供 summarization 恢復）
- [ ] 重試 / rate limit 處理

### 1.3 Tool `tool/tool.go`（langchain_core.tools）

- [~] `Tool` 介面（Name / Description / Schema / Invoke）
- [~] `Schema`（JSON Schema）
- [~] `Runtime`（`ToolRuntime`：tool_call_id / state / store / config / context / stream writer）
- [~] `Result`（content / Command / artifact / is_error）
- [~] `Func`（`StructuredTool.from_function`）
- [~] `Spec`：Tool → `llm.ToolSpec`
- [~] `ApplyDescriptionOverrides` — py: `_tools._apply_tool_description_overrides`
- [ ] 從 Go struct 反射產生 JSON Schema
- [ ] 參數驗證（依 schema 驗證 args、拒絕未知欄位）
- [x] 工具錯誤 → ToolMessage(status=error) 轉換 — go: `agent/loop.go`

### 1.4 State / Reducer `runtime/state.go`（LangGraph state）

- [~] `State`（messages / structured_response / 擴充 Values）
- [~] `Update`
- [~] `Reducer`、`KeySpec`（Private = `PrivateStateAttr`、OmitFromInput / OmitFromOutput）
- [~] `Schema` / `MergeSchemas` / `PrivateKeys` — py: `middleware/_state.private_state_field_names`
- [x] `State.Apply`
- [x] `State.Clone`（淺層複製：messages slice 與 Values map 另外複製）
- [x] `MessagesReducer`（`add_messages`：append、依 ID 取代、`RemoveAllMessages`）
- [ ] `_messages_delta_reducer` + `DeltaChannel`（checkpoint 只存增量，snapshot_frequency=50）— py: `_messages_reducer.py`, `graph.DeepAgentState`
- [ ] files reducer（`_file_data_reducer`：None 值代表刪除）— py: `middleware/filesystem.py`

### 1.5 Checkpointer `runtime/checkpoint.go`

- [~] `Checkpoint`、`Checkpointer` 介面
- [~] `MemoryCheckpointer`（`InMemorySaver`）
  - [ ] Latest / Put / List / DeleteThread
- [ ] SQLite checkpointer（`AsyncSqliteSaver`，CLI sessions 使用）
- [ ] checkpoint 序列化格式（JSON）

### 1.6 Store `runtime/store.go`

- [~] `Namespace`、`Item`、`SearchOptions`、`Store` 介面
- [~] `InMemoryStore`
  - [ ] Get / Put / Delete / Search（prefix + filter）
- [ ] 持久化 Store（SQLite / Postgres）

### 1.7 Interrupt / Command `runtime/interrupt.go`

- [~] `Interrupt`、`InterruptError`、`Command`（update / resume / goto）
- [ ] interrupt 暫停與 checkpoint 保存 pending interrupts
- [ ] 以 `Command.Resume` 續跑

### 1.8 Config / Event `runtime/config.go`, `runtime/event.go`

- [~] `RunConfig`（thread_id / recursion_limit / tags / metadata / configurable）
- [~] `DefaultRecursionLimit = 9999`
- [~] `StreamMode`（values / updates / messages / custom）、`Event`、`Emitter`
- [ ] subgraph（subagent）事件 namespace 傳遞

### 1.9 Agent loop `agent/`（langchain `create_agent` + `AgentMiddleware`）

- [~] `Middleware` 介面 + 可選 hooks：`BeforeAgentHook` / `BeforeModelHook` / `AfterModelHook` / `AfterAgentHook` / `WrapModelCallHook` / `WrapToolCallHook` / `ToolProvider` / `StateSchemaProvider`
- [~] `ModelRequest` / `ModelResponse`（含 `ExtendedModelResponse` 的 Update）
- [~] `ToolCallRequest` / `ModelHandler` / `ToolHandler`
- [~] `Runtime`（hook 用）
- [~] `Config` / `Input` / `Agent` 介面（Invoke / Stream / GetState / Name）
- [~] `CreateAgent` — go: `agent/loop.go`, `agent/chain.go`
  - [x] model → tools → model 迴圈（tool calls 循序執行）
  - [x] middleware hook 串接順序（wrap 類：前面的在外層；AfterModel / AfterAgent 逆序）
  - [ ] 平行執行多個 tool calls
  - [x] 未知工具錯誤回報
  - [x] recursion limit（`ErrRecursionLimit`）
  - [ ] checkpoint 讀寫（每步）
  - [ ] interrupt / resume
  - [ ] streaming（values / updates / messages / custom）
  - [x] middleware 名稱重複檢查（含工具名稱重複檢查）
  - [ ] AfterAgent 回傳 `Command{Goto:"model"}` 重新進入迴圈
- [~] Structured output：`ToolStrategy` / `ProviderStrategy` / `AutoStrategy` — go: `agent/response_format.go`
  - [ ] ToolStrategy 實作（合成工具 + 解析）
  - [ ] ProviderStrategy 實作
  - [ ] AutoStrategy 選擇邏輯
- [ ] `context_schema`（不可變 run context）
- [ ] `cache`（節點快取）
- [ ] debug 模式輸出

---

## 2. graph.py：`create_deep_agent` — go: `deepagents.go`

- [~] `CreateDeepAgent(opts ...Option)` 入口（Stage 2 直通版：只轉給 `agent.CreateAgent`）
  - 備註：CreateDeepAgent 暫未組裝預設 middleware stack；subagents / skills / memory / permissions / interrupt_on / response_format / checkpointer / backend 會回 `ErrNotImplemented`
- [~] 參數（functional options）
  - [x] `model`（`WithModel` 優先，否則 `WithModelSpec` 經 `InitChatModel` 解析）
  - [ ] `model=None` 預設 `claude-sonnet-4-6`（Python 已 deprecated）
  - [x] `tools`（`WithTools`）
  - [x] `system_prompt`（`WithSystemMessage` 優先，否則 `WithSystemPrompt`）
  - [~] `middleware`（`WithMiddleware`：目前直接作為完整 stack，尚無同名取代規則）
  - [~] `subagents`（`WithSubAgents`：SubAgent / CompiledSubAgent / AsyncSubAgent）
  - [~] `skills`（`WithSkills`）
  - [~] `memory`（`WithMemory`）
  - [~] `permissions`（`WithPermissions`）
  - [~] `backend`（`WithBackend`，預設 StateBackend）
  - [~] `interrupt_on`（`WithInterruptOn`）
  - [~] `response_format`（`WithResponseFormat`）
  - [~] `state_schema`（`WithStateSchema`）
  - [ ] `context_schema`
  - [~] `checkpointer` / `store`（`WithCheckpointer` / `WithStore`）
  - [~] `debug` / `name`（`WithDebug` / `WithName`）
  - [ ] `cache`
- [ ] Model 解析（`resolve_model`）+ harness profile 查找
- [ ] Prompt 組裝：USER → BASE → SUFFIX（`_apply_profile_prompt`）；SystemMessage 保留 cache_control blocks
- [ ] Main agent middleware 組裝順序：Skills → Filesystem → SubAgent → Summarization → PatchToolCalls → AsyncSubAgent → [caller] → profile extra → prompt caching → Memory → HITL → UnsupportedContent → ToolExclusion
- [ ] `_apply_custom_middleware`：同名取代原位置、新增者插在 core 之後 tail 之前
- [~] Required middleware 保護（Filesystem / SubAgent 不可被排除）— go: `RequiredMiddleware`
- [ ] `_excluded_middleware.py`：`_validate_excluded_middleware_config` / `_apply_excluded_middleware` / `_verify_excluded_middleware_coverage` / 名稱衝突檢查
- [ ] 預設 general-purpose subagent（可被 profile 關閉、可被同名 spec 取代、繼承同名 middleware）
- [ ] 宣告式 SubAgent 處理：model 解析、subagent profile、permissions 繼承、interrupt_on 繼承與合併、tools 繼承、skills、fork 模式（鏡射 parent prompt middleware、system prompt addendum）
- [ ] AsyncSubAgent 分流到 AsyncSubAgentMiddleware
- [ ] permissions → interrupt_on 合併（`_merge_fs_interrupt_on`）— go: `hitl.Merge`（stub）
- [ ] private state keys 注入 SubAgentMiddleware
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
  - [ ] `SupportsExecution` 對 CompositeBackend 依 default backend 判斷
- [-] sync / async 雙版本（`als` / `aread` ...）— Go 以 `context.Context` 單一版本取代
- [-] `execute_accepts_timeout` / `_method_accepts_max_count` 反射檢查 — Go 介面簽名固定

### 3.2 RunContext `backend/runcontext.go`

- [x] `RunContext` / `WithRunContext` / `RunContextFrom`（取代 LangGraph `get_config()` / `get_store()`）

### 3.3 實作

- [~] `StateBackend` — py: `backends/state.py`
  - [ ] ls / read / write / edit / delete / grep / glob
  - [ ] upload_files / download_files
  - [ ] 透過 RunContext 讀寫 `files` state、產生 Pending update
- [~] `StoreBackend` — py: `backends/store.py`
  - [~] `NamespaceFactory`、`ValidateNamespace`
  - [ ] ls / read / write / edit / delete / grep / glob
  - [ ] upload_files / download_files
- [~] `FilesystemBackend` — py: `backends/filesystem.py`
  - [ ] virtual_mode 路徑解析與逃逸防護
  - [ ] ls / read（含 binary → base64）/ write / edit / delete
  - [ ] grep（ripgrep 優先，fallback 內建搜尋）、context_lines
  - [ ] glob（逾時、截斷原因）
  - [ ] max_file_size_mb
  - [ ] symlink loop 偵測、例外 → 標準錯誤碼
  - [ ] upload_files / download_files
- [~] `LocalShellBackend` — py: `backends/local_shell.py`
  - [ ] execute（timeout、max_output_bytes、env / inherit_env、cwd=root）
- [~] `CompositeBackend` — py: `backends/composite.py`
  - [ ] 最長前綴路由
  - [ ] ls 合併（根目錄列出 route）
  - [ ] grep / glob 跨 route 合併、路徑重寫、max_count 預算、截斷原因合併
  - [ ] read / write / edit / delete 路由
  - [ ] execute 委派 default
  - [ ] upload / download 分組路由
  - [ ] artifacts_root
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

- [~] 常數：`EmptyContentWarning` / `EmptyOldStringError` / `TruncationMarkerTemplate` / `MaxLineLength` / `ToolResultTokenLimit` / `TruncationGuidance` / `MaxVideoInputBytes`
- [~] `FileType` + `FileTypeForPath`（副檔名 → text / image / audio / video / pdf）
- [~] `ValidatePath`（allowed_prefixes、`..`、`~`）
- [~] `ToPOSIXPath`
- [~] `FormatContentWithLineNumbers`（cat -n、長行切分）
- [~] `CheckEmptyContent`
- [~] `CreateFileData` / `UpdateFileData` / `FileDataToString`
- [~] `NormalizeReadBounds` / `SliceReadResponse`
- [~] `PerformStringReplacement`
- [~] `TruncateIfTooLong`
- [~] `SanitizeToolCallID`
- [~] `CompileGrepIncludeGlob`
- [~] `GrepMatchesFromFiles` / `GlobSearchFiles`
- [~] `GrepOutputMode` / `FormatGrepMatches`（files_with_matches / content / count、context 分組）
- [~] `RegexLiteralHint`
- [ ] `build_grep_results_dict`、`_glob_anchor`、`_paths_overlap`

---

## 4. Middleware — go: `middleware/`

### 4.1 Filesystem `middleware/filesystem`（py: `middleware/filesystem.py`）

- [~] `ToolName`（`FsToolName`）與 `AllTools`
- [~] `Options`（backend / system_prompt / custom_tool_descriptions / tool_token_limit_before_evict / human_message_token_limit_before_evict / max_execute_timeout / grep_max_count / tools / permissions）
- [~] `Middleware`（Name / Tools / StateSchema / WrapModelCall / WrapToolCall）
- [ ] 工具實作
  - [ ] `ls`
  - [ ] `read_file`（offset / limit 分頁、行號、截斷通知、空檔提醒）
  - [ ] `read_file` 多模態：image / audio / pdf 回傳 content blocks
  - [ ] `read_file` 影片：抽幀（py: `_video.py`，需 ffmpeg 或等效函式庫）
  - [ ] `write_file`
  - [ ] `edit_file`（replace_all）
  - [ ] `delete`（僅 Deleter backend；萬用字元 deny 重疊檢查）
  - [ ] `glob`（逾時訊息）
  - [ ] `grep`（output_mode、max_count、regex 提示）
  - [ ] `execute`（timeout 上限、ExecuteArtifact、offload 大輸出）
- [ ] 工具 schema 與預設描述文字（`LsSchema` ... `ExecuteSchema`）
- [ ] system prompt：composite route host-path 說明（`_route_host_path_prompt`）
- [ ] 非 sandbox backend 時移除 `execute`
- [ ] 權限檢查：read / write / delete 規則、ls / glob / grep 結果過濾
- [ ] 平行檔案異動保護（`_parallel_file_mutation_error`）
- [ ] 大型 tool 結果 eviction 到 backend（`_message_eviction.py`：preview head/tail、`/large_tool_results/`）
- [ ] 大型 human message eviction
- [ ] media 結果重排到 tool results 之後、rejected content 替換
- [ ] `files` state reducer

### 4.2 Permissions `middleware/permissions`（py: `FilesystemPermission`, `_fs_interrupt.py`）

- [~] `Operation`（read / write / delete）、`Mode`（allow / deny / interrupt）
- [~] `FilesystemPermission` + `Validate`
- [~] `Check`（first match wins，無匹配 = allow）
- [~] `FilterPaths`
- [~] `BuildInterruptOn`（exact / bulk when-predicate、anchor 重疊判斷）

### 4.3 SubAgents `middleware/subagents`（py: `middleware/subagents.py`）

- [~] `TaskToolName`、`GeneralPurposeName` / `GeneralPurposeDescription` / `DefaultSubagentPrompt`
- [~] `Mode`（isolated / fork；legacy `handoff` = isolated）
- [~] `Spec` 介面、`SubAgent`、`CompiledSubAgent`
- [~] `Options` / `Middleware` / `AddPrivateStateKeys`
- [~] `ValidateSpecs`（名稱唯一、mode 合法、fork 不可有 skills）
- [ ] `task` 工具
  - [ ] `TaskToolSchema`（description / subagent_type，拒絕未知欄位）
  - [ ] `TASK_TOOL_DESCRIPTION` + `{available_agents}` 清單
  - [ ] isolated：只傳 task 描述，排除 `_EXCLUDED_STATE_KEYS` 與 private keys
  - [ ] fork：傳 parent 有效訊息、`_FORK_TASK_PREAMBLE`、防止遞迴 fork
  - [ ] 結果回傳：structured_response JSON 或最後一則非空 AI 訊息
  - [ ] subagent 的 state update 回寫 parent（files 等）
  - [ ] response_format（per-subagent）
  - [ ] 未知 subagent_type 錯誤
- [ ] 事件 namespace / tracing metadata（`ls_agent_type`）

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
- [ ] `DEEPAGENTS_DEFAULT_SUMMARY_PROMPT`
- [ ] 觸發判斷（tokens / messages / fraction；AND / OR 組合）
- [ ] cutoff 計算（不切斷 tool call / tool result 配對）
- [ ] 舊 tool args 截斷（write_file / edit_file）
- [ ] 產生摘要（trim_tokens_to_summarize）
- [ ] 被移除訊息 offload 到 `/conversation_history/{session_id}.md`
- [ ] inline media（data URL）offload 到 backend
- [ ] summary 訊息帶檔案路徑、過濾舊 summary
- [ ] context overflow 錯誤 → 摘要後重試（`_call_with_budget`）
- [ ] `_overflow_clip.py`：overflow 門檻推導
- [ ] `compact_conversation`：資格判斷、nothing-to-compact、錯誤處理

### 4.6 Memory `middleware/memory`（py: `middleware/memory.py`）

- [~] `ContentsStateKey`（private）、`Options`、`Middleware`（BeforeAgent / WrapModelCall）
- [ ] `MEMORY_SYSTEM_PROMPT` 模板
- [ ] BeforeAgent：依序載入 sources（每 thread 一次）
- [ ] 去除 HTML 註解
- [ ] 注入 system prompt、Anthropic cache_control breakpoint

### 4.7 Skills `middleware/skills`（py: `middleware/skills.py`）

- [~] 限制常數（檔案大小、name / description / compatibility 長度、warning 數量）
- [~] `Metadata`（`SkillMetadata`）、`Source`、`SourcesFromPaths`
- [~] `Middleware`（BeforeAgent / WrapModelCall / StateSchema）
- [~] `ParseMetadata`、`ListSkills`
- [ ] SKILL.md frontmatter（YAML）解析
- [ ] name 驗證（長度、字元、與目錄名一致）
- [ ] allowed-tools 解析
- [ ] 多來源覆蓋（後者優先）、source label 推導
- [ ] 載入錯誤收集與截斷
- [ ] `SKILLS_SYSTEM_PROMPT` + skills 清單 / 位置 / 警告格式化

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

- [~] PatchToolCalls `middleware/patchtoolcalls`（py: `patch_tool_calls.py`）
  - [ ] BeforeAgent：補上 dangling tool call 的取消訊息
- [~] UnsupportedContent `middleware/unsupportedcontent`（py: `unsupported_content.py`）
  - [ ] 依 ModelProfile 判斷 block 是否支援（含 tool message 內）
  - [ ] OpenAI Responses 例外、read_file placeholder
- [~] HumanInTheLoop `middleware/hitl`（langchain `HumanInTheLoopMiddleware`）
  - [~] `InterruptOnConfig` / `DecisionType` / `ActionRequest` / `Decision`
  - [ ] AfterModel：對符合的 tool calls 發出 interrupt
  - [ ] 處理 approve / edit / reject / respond
  - [~] `Merge`（`_merge_fs_interrupt_on`）
- [~] TodoList `middleware/todolist`（langchain `TodoListMiddleware`）
  - [ ] `write_todos` 工具與 system prompt
- [~] PromptCaching `middleware/promptcaching`（py: `_prompt_caching.py`）
  - [~] `AppendDefaults`
  - [ ] Anthropic cache_control 標記
  - [ ] Bedrock 標記
  - [ ] Fireworks 標記
- [~] ToolExclusion `middleware/toolexclusion`（py: `_tool_exclusion.py`）
  - [ ] WrapModelCall 過濾工具
- [ ] `middleware/_utils.append_to_system_message`
- [-] `middleware/permissions.py`（Python 僅為 re-export 模組）

---

## 5. Profiles — go: `profiles/`

- [~] `GeneralPurposeSubagentProfile`（enabled / description / system_prompt）
- [~] `HarnessProfile`（base_system_prompt / system_prompt_suffix / tool_description_overrides / excluded_tools / excluded_middleware / extra_middleware / general_purpose_subagent）
- [~] `HarnessProfileConfig` + `ToProfile`（宣告式子集）
- [ ] `HarnessProfileConfig` 從 YAML / JSON 載入與欄位驗證（`_coerce_*`）
- [~] `RegisterHarnessProfile`（additive merge）
- [~] `HarnessProfileFor`（provider → provider:model 疊加）
- [~] `MergeHarnessProfiles`（`_merge_profiles`、middleware 合併）
- [~] `ApplyPrompt`（`_apply_profile_prompt`）
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

- [x] `cmd/dcode/main.go`（flag：`-n` 或位置參數 prompt、`-model`、`-thread`、`-version`）
- [ ] 完整 CLI 參數（`main.py`：agent 選擇、model、sandbox、auto-approve、resume、list / reset agents、threads 子命令 ...）
- [~] Server `code/server`（`Server` / `Manager` / `Config` / `RunRequest` / `New`）
  - [x] in-process server（`Addr` 為空；模型依序取 `Config.ModelSpec` → `DCODE_MODEL` → `openai:gpt-5.5`；`Run` yield 一個含最終 state 的 values 事件）
  - [ ] server runtime 組裝（model、tools、MCP、sandbox、graph）— code: `server_graph.py`
  - [ ] 子行程啟動 / 重啟 / 停止 — code: `client/launch/server*.py`
  - [ ] streaming 協定
- [~] Client `code/client`
  - [~] `RunNonInteractive`（headless）— code: `client/non_interactive.py`
    - [x] 印出最後一則 assistant 文字、錯誤寫 stderr 並回 exit code 1
    - [ ] `-thread` 續跑（目前回「thread 續跑尚未支援」）
    - [ ] JSON 輸出、串流輸出、auto-approve
  - [~] `RunInteractive`
  - [ ] remote client — code: `client/remote_client.py`
- [~] TUI placeholder `code/tui`（Python：Textual `app.py` + `tui/*`，UI 函式庫未定）
  - [ ] 聊天輸入、自動完成、貼上收合
  - [ ] 訊息渲染、markdown、diff、工具輸出
  - [ ] approval 介面、ask_user
  - [ ] model / thread / agent / theme 選擇器
  - [ ] 狀態列、context 用量、通知中心、subagent 面板

### 6.2 Agent `code/agent`（code: `agent.py`）

- [~] `CreateCLIAgent` / `Options`
- [~] `SystemPrompt`（model identity、cwd、git 資訊、工具說明）
- [~] `ListAgents` / `ResetAgent`
- [~] `ShellAllowListMiddleware`
- [ ] 任務錯誤格式化（`_format_task_error`）
- [ ] subagents 注入 fs tools
- [ ] rubric grader 工具與 prompt
- [ ] PTC（programmatic tool calling）選項

### 6.3 設定 `code/config`（code: `config.py`, `configuration/*`, `_paths.py`, `_env_vars.py`）

- [~] `Scope`、`Settings`、`Source`、`Paths`、`Resolver`、`NewResolver`
- [x] `DefaultPaths`（`~/.deepagents`、`cwd/.deepagents`；尚未處理 profile state dir）
- [ ] config.toml 解析（需決定 TOML 函式庫）
- [ ] 分層合併（managed / user / project / session / env）
- [x] .env 載入（shell > 最近的專案 `.env` > `~/.deepagents/.env`）— go: `code/config/dotenv.go`
  - [ ] 拒絕危險 key（`_DOTENV_DENIED_ENV_KEYS`、專案 `.env` 不可設定的 LangSmith key）
  - [ ] 變數內插（`${VAR}`）、`/reload` 時移除先前注入的值
- [ ] `/reload` 產生新 generation
- [ ] 環境變數對照表

### 6.4 Sessions `code/sessions`（code: `sessions.py`）

- [~] `ThreadInfo` / `ListOptions` / `Manager` / `Open` / `NewThreadID`
- [ ] SQLite schema 與 checkpointer
- [ ] list / most recent / exists / find similar / delete / metadata
- [ ] message count、initial prompt 快取
- [ ] `threads` 子命令輸出

### 6.5 工具 `code/tools`（code: `tools.py`）

- [~] `NewWebSearch`（Tavily）
- [~] `NewFetchURL`
- [ ] SSRF 防護（封鎖私有 IP、DNS pinning、redirect 檢查）
- [ ] HTML → markdown

### 6.6 Approval `code/approval`（code: `approval_mode.py`, `auto_mode.py`）

- [~] `Mode`（manual / auto / yolo）、`Next`、`Read`、`Write`、`Verdict`、`Classifier`
- [ ] 每 thread 模式存 store
- [ ] YOLO 確認檔、auto mode notice
- [ ] Auto mode classifier（安全判斷模型）

### 6.7 Slash commands `code/commands`（code: `command_registry.py`）

- [x] `Catalog`：46 個公開 + 2 個隱藏指令資料（有測試）
- [~] `Registry.Register` / `Registry.Dispatch`
- [ ] 各指令實作
  - [ ] `/agents`
  - [ ] `/auth`（`/connect`）
  - [ ] `/auto`
  - [ ] `/auto-update`
  - [ ] `/btw`
  - [ ] `/changelog`
  - [ ] `/clear`
  - [ ] `/context`
  - [ ] `/context-doctor`
  - [ ] `/copy`
  - [ ] `/cost`
  - [ ] `/docs`
  - [ ] `/editor`
  - [ ] `/effort`
  - [ ] `/extensions`
  - [ ] `/feedback`
  - [ ] `/force-clear`
  - [ ] `/goal`
  - [ ] `/help`
  - [ ] `/install`
  - [ ] `/line-numbers`
  - [ ] `/manual`
  - [ ] `/mcp`
  - [ ] `/model`
  - [ ] `/notifications`
  - [ ] `/offload`（`/compact`）
  - [ ] `/plugins`
  - [ ] `/prompts`
  - [ ] `/quit`（`/q`）
  - [ ] `/reload`
  - [ ] `/remember`
  - [ ] `/restart`
  - [ ] `/rubric`（`/criteria`）
  - [ ] `/scrollbar`
  - [ ] `/skill-creator`
  - [ ] `/summarization-model`
  - [ ] `/theme`
  - [ ] `/threads`
  - [ ] `/timestamps`
  - [ ] `/tokens`
  - [ ] `/tools`
  - [ ] `/trace`
  - [ ] `/uninstall`
  - [ ] `/update`
  - [ ] `/version`（`/about`）
  - [ ] `/yolo`
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
- [~] Cost `code/cost`（`Price`、`Table`、`LoadTable`、`Estimate`）— code: `cost_tracking.py`
- [~] Plugins `code/plugins`（`Manifest`、`Store`、`OpenStore`）— code: `plugins/*`
  - [ ] marketplace、discovery、substitution
- [~] Extensions `code/extensions`（`Extension`、`Register`、`Loaded`）— code: `extensions/*`
- [ ] model config / 切換 / reasoning effort — code: `model_config.py`, `configurable_model.py`, `reasoning_effort.py`
- [ ] model retry — code: `model_retry.py`
- [ ] auth store / 顯示 — code: `auth_store.py`, `auth_display.py`
- [ ] OpenAI Codex 整合 — code: `integrations/openai_codex.py`
- [ ] btw（旁支問題）— code: `btw*.py`
- [ ] ask_user 工具 — code: `ask_user.py`
- [ ] local context（git、專案資訊）— code: `local_context.py`, `_git.py`, `project_utils.py`
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

---

## 7. 未來項目（本階段不實作）

- [ ] ACP connector（Zed Agent Client Protocol）— py: `libs/acp/deepagents_acp`（`server.py`, `utils.py`）
- [ ] Talon（多頻道 agent host）— py: `libs/talon/deepagents_talon`
  - [ ] host / runtime
  - [ ] channels：Slack / Discord / Telegram / WhatsApp
  - [ ] cron 排程（expression / jobs / scheduler / tools）
  - [ ] history（backends / vectors / embeddings / postgres / profiles）
  - [ ] archive、pairing、tool approvals、MCP、speech、media、observability
- [ ] Evals — py: `libs/evals`（harbor adapters、benchmarks、radar、scorecard）
- [ ] Partners sandbox 整合
  - [ ] Daytona — py: `libs/partners/daytona`
  - [ ] Modal — py: `libs/partners/modal`
  - [ ] Runloop — py: `libs/partners/runloop`
  - [ ] Vercel — py: `libs/partners/vercel`
  - [ ] QuickJS（REPL / PTC middleware）— py: `libs/partners/quickjs`
