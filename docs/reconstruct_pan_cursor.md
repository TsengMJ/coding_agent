# dcode 系統級重構計畫：研究、Audit、Baseline、Gap、目標架構與遷移

> 撰寫日期：2026-10-02。範圍：`deepagents_golang` 全 repo（核心 SDK + `code/*` dcode + `dbench`）。
> 本文件是計畫，尚未修改任何程式碼。實作開始時，每個 Stage 同步寫入 `IMPLEMENTATION_PLAN.md`。

---

## 0. TL;DR

1. **dcode 現在的瓶頸是「最後一哩」與成本長尾，找不到程式碼不是主因。** 過去 4 次 dbench 共 28 個 attempt，7 個沒解出來。這 7 個的 F2P（新需求測試）**全部通過**，失敗原因全是 P2P（原有測試）壞掉 1–12 個，或是逾時。Agent 有找到對的位置、做出對的功能，但把旁邊的東西改壞了，而且沒能自己發現。
2. **Token 用量隨步數平方成長，因為舊的 tool output 永遠不離開 context。** 每題平均 input 1.8M tokens（早期 20 題）到 10.7M（最近 8 題），最貴的一題 48M tokens、$1.62、逾時。summarization 門檻在 1M window 的 80%，所以從來沒觸發；context 最高到 577k。`read_file` + `grep` 佔 tool output 的 65–80%，同一個 tool call（名稱與參數都一樣）平均重複 4–19 次。
3. **最有槓桿的改動都是 deterministic code，不需要新增 LLM 呼叫或 graph node**：observation masking、artifact 引用、baseline-aware 的 regression 判定、相同失敗偵測、穩定的 prompt cache 前綴。Planner node、reviewer subagent、model router、向量索引、預設啟用的 LSP 都先不做，等 A/B 證明有效再說。
4. **保留目前的單一 model→tool loop。** 業界公開資料（Codex、Claude Code、mini-SWE-agent）都是簡單 loop 加上強的 tool 與 context 管理。dcode 的 5 個 graph node 已經夠用。
5. **Baseline 直接用過去的測試結果，不重跑 50 題。** 之後每個 Phase 只跑 7 題的 regression 子集（由真實失敗與高成本案例組成），每題 2 次，必要時再加 smoke50 前 20 題。

---

## 1. 方法與證據標記

### 1.1 本輪做了什麼

| 項目 | 方法 |
|---|---|
| 程式碼 audit | 讀過核心 loop（`agent/loop.go`）、middleware 介面與組裝（`agent/middleware.go`、`assembly.go`）、dcode 組裝（`code/agent/agent.go`）、system prompt、VerifyGate、live context、summarization、filesystem backend、shell backend、codetools、bench metrics；以 `rg` 掃 `notimpl` stub |
| 建置狀態 | `go build ./...`、`go vet ./...`、`go test ./...` 在目前 dirty tree（85 個檔案未提交）全部通過 |
| Baseline | 讀 `~/.deepagents/bench/reports/*.md`、dbench Postgres 的 `bench_attempts`（32 筆 dcode attempt）、`trace.jsonl` 與 `dcode_trace_records`（28 個 attempt 的完整 trajectory）。用一次性分析腳本算出 tool 分布、重複呼叫、每步 token、cache 命中、patch 大小 |
| 研究 | 官方文件、engineering blog、論文、開源程式碼；重點來源都在本輪重新查證（見 §12） |

### 1.2 證據標記

- **【已證實】**：官方文件、開源程式碼、論文的明確敘述，或本 repo 程式碼與資料直接可見。
- **【公開模式】**：多個系統的公開行為顯示同一種設計，但內部實作未公開。
- **【推論】**：我的判斷。需要用 benchmark 驗證，可能是錯的。

閉源系統（Claude Code、Codex 雲端、Cursor、Copilot）只根據公開文件與行為描述，不推測內部 prompt 或演算法。

---

## 2. Phase 1：Industry Research Report

### 2.1 架構與 Agent Loop

| 系統 | 內容 | 標記 |
|---|---|---|
| Codex CLI | loop 為「使用者輸入 → 模型推論 → 執行工具 → 把工具輸出附加到輸入 → 重複，直到模型給出最終回覆」。每次請求都是上一個請求的**完全前綴延伸**，以取得 prompt cache；換工具、換模型、改 sandbox / approval / cwd 都會讓 cache 失效。[Unrolling the Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/) | 【已證實】 |
| mini-SWE-agent | 約 100 行、只有 bash，不需自訂工具，在 SWE-bench Verified 超過 74%。SWE-bench 官方用它做 bash-only leaderboard 來公平比較模型。[repo](https://github.com/SWE-agent/mini-swe-agent)、[leaderboard 說明](https://www.swebench.com/verified.html) | 【已證實】 |
| Agentless | 不讓 LLM 自主決定流程：固定的「定位 → 修復 → patch 驗證」三段，用 regression test 與重現測試篩選候選 patch。以低成本達到當時開源最佳。[論文](https://arxiv.org/abs/2407.01489) | 【已證實】 |
| Anthropic | 建議先用最簡單可行的方案，只有在簡單方案不夠時才加 workflow / agent 複雜度。[Building effective agents](https://www.anthropic.com/engineering/building-effective-agents) | 【已證實】 |
| 共通點 | 主流 coding agent 的核心都是單一 tool-calling loop。差異主要在工具介面、context 管理與驗證，不在 orchestration graph。 | 【公開模式】 |

**對 dcode 的結論【推論】**：不需要把 loop 拆成更多 graph node。dcode 現在的 `before_agent → model → after_model → tools → after_agent` 已經能表達需要的控制點。需要的是讓「何時繼續、何時停止」的判斷變成明確、可測試的 deterministic 規則，而不是更多節點。

### 2.2 Context Engineering

| 來源 | 內容 | 標記 |
|---|---|---|
| Anthropic | context 是有限的注意力預算；context 越長，回想與推理品質越差（context rot）。Claude Code 的 compaction 保留架構決策、未解決的 bug、實作細節，並附上最近存取的 5 個檔案。最安全、最輕量的 compaction 是清除舊的 tool result。[Effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) | 【已證實】 |
| Anthropic API | `clear_tool_uses` 依時間順序清掉最舊的 tool result，換成 placeholder，保留 tool_use 紀錄，可設定保留最近 N 次、排除特定工具。適合可重新取得的內容；需要保留對話推理時才用 compaction。[Context editing](https://docs.anthropic.com/en/docs/build-with-claude/context-editing)、[Compaction](https://docs.anthropic.com/en/build-with-claude/compaction) | 【已證實】 |
| JetBrains 研究 | 在 SWE-agent / SWE-bench Verified 上，五種模型設定中，observation masking 讓成本比原始 agent 減半，解題率與 LLM 摘要相當甚至略高；混合策略再省 7–11%。結果可推廣到 OpenHands。[The Complexity Trap](https://arxiv.org/abs/2508.21433) | 【已證實】 |
| OpenHands | `LLMSummarizingCondenser`：事件數超過 `max_size`（預設 120）時，保留前 `keep_first` 個與最近的事件，中間交給（通常較便宜的）LLM 摘要；文件宣稱每輪 API 成本最多減半。[Condenser 架構](https://docs.openhands.dev/sdk/arch/condenser) | 【已證實】 |
| Cursor | 長 tool output 寫成檔案，agent 用 `tail` / 讀取按需取回，不截斷；摘要後提供完整歷史檔案的引用；MCP 工具說明改成按需載入，在呼叫 MCP 的 run 中省下 46.9% token。[Dynamic context discovery](https://cursor.com/blog/dynamic-context-discovery) | 【已證實】 |
| Codex | 超過 `auto_compact_limit` 時呼叫 `/responses/compact`，回傳可取代原輸入的精簡 item 清單。[同上 Codex 文章](https://openai.com/index/unrolling-the-codex-agent-loop/) | 【已證實】 |

**對 dcode 的結論【推論】**：第一步應該是 observation masking（舊 tool result 換成「已省略，內容在 artifact X，可以重新讀取」），不是調整 summarization 門檻。dcode 已經有 artifact 儲存（Redis `large_tool_results`、Postgres `conversation_history`），只缺「舊結果離開 context」這一步。

### 2.3 Code Localization

| 來源 | 內容 | 標記 |
|---|---|---|
| SWE-agent ACI | 搜尋最多回 50 筆，超過就不回結果，要求更精確的查詢。消融實驗：summarized search 18.0%、逐筆翻頁的 iterative search 12.0%（比沒有搜尋工具的 15.7% 還差，因為 agent 會逐筆看完所有結果）。[SWE-agent](https://arxiv.org/abs/2405.15793) | 【已證實】 |
| Agentless | 分層定位：檔案 → class / function（用只含宣告的 skeleton 格式壓縮檔案）→ 精確編輯位置；結合 LLM 排序與 embedding 檢索。 | 【已證實】 |
| LocAgent | 把程式碼解析成異質圖（目錄 / 檔案 / class / function 節點，contain / import / invoke / inherit 邊），讓 agent 做多跳搜尋；SWE-bench Lite 檔案層級定位最高 92.7%。[ACL 2025](https://aclanthology.org/2025.acl-long.426/) | 【已證實】 |
| Aider repo map | tree-sitter 抽出定義與引用 → 檔案依賴圖 → PageRank（以對話中的檔案與提到的識別字做 personalization）→ 在 token 預算內（預設 1k）挑最重要的符號。[文件](https://aider.chat/docs/repomap.html)、[實作](https://github.com/Aider-AI/aider/blob/main/aider/repomap.py) | 【已證實】 |
| Cursor | grep 加上自訓 embedding 的語意搜尋，問答準確率平均高 12.5%（依模型 6.5%–23.5%），大型 codebase 效果最明顯。兩者合用最好。[Semantic search](https://cursor.com/blog/semsearch) | 【已證實】 |
| Claude Code | 2.0.74 起內建 LSP 工具（goToDefinition、findReferences、hover、documentSymbol、workspaceSymbol、call hierarchy），需另外安裝 language server plugin。[社群 plugin 說明](https://github.com/Piebald-AI/claude-code-lsps) | 【已證實】（來源為社群 repo 對官方 changelog 的整理） |

**對 dcode 的結論【推論】**：在現有 trajectory 裡，定位不是主要失敗原因（§4.3）。所以 localization 的優先順序是「讓 grep / glob 更省 token、更精確」（P1），不是「加 repo map / LSP / 向量索引」（P3 實驗）。大型陌生 repo（SWE-bench Pro 公開題平均改 4.1 個檔案、107 行）上的定位品質，需要先有正確的定位指標才能判斷。

### 2.4 Tool / Agent-Computer Interface

| 來源 | 內容 | 標記 |
|---|---|---|
| Anthropic | tool 回傳只放有助決策的欄位；提供 `concise` / `detailed` 回應格式；分頁、範圍、過濾、截斷要有合理預設；Claude Code 預設 tool 回應上限 25,000 tokens；截斷與錯誤訊息要告訴 agent 下一步怎麼做。[Writing tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) | 【已證實】 |
| SWE-agent | 編輯時跑 linter，造成語法錯誤的編輯直接丟棄並要求重試：18.0% vs 沒有 lint 的 15.0%。 | 【已證實】 |
| Aider | 不同模型適合不同編輯格式；whole-file 簡單但慢又貴；search/replace 與 unified diff 只送改動部分。GPT-4 Turbo 用 udiff 從 20% 升到 61%，「偷懶省略程式碼」從 12 題降到 4 題。[Edit formats](https://aider.chat/docs/more/edit-formats.html)、[Unified diffs](https://aider.chat/docs/unified-diffs.html) | 【已證實】 |

**對 dcode 的結論【推論】**：dcode 的 `run_tests` / `diagnostics` 已經是業界水準的結構化工具（平均每次只回 0.1–0.5k 字元）。問題集中在三個「原始輸出」工具：`read_file`、`grep`、`git_diff`（每次平均 9–10k 字元）。

### 2.5 驗證與修復

| 來源 | 內容 | 標記 |
|---|---|---|
| Huang et al. | 沒有外部回饋時，LLM 自我修正推理通常無效，甚至變差。[ICLR 2024](https://arxiv.org/abs/2310.01798) | 【已證實】 |
| Kamoi et al. | 有可靠的外部回饋（例如測試、編譯器）時，自我修正才有效。[TACL 2024](https://arxiv.org/abs/2406.01297) | 【已證實】 |
| Agentless | 用 repo 原有的 regression test 篩掉會破壞既有功能的 patch。 | 【已證實】 |
| Claude Code | `Stop` hook 可以阻止 agent 結束，用來強制驗證或要求繼續工作；`PostToolBatch` 在一批平行工具結束後、下一次模型呼叫前觸發。[Hooks](https://code.claude.com/docs/en/hooks) | 【已證實】 |
| SWE-bench Pro | 失敗分類（GPT-5 判讀最後 20 輪）：wrong solution、tool-use、syntax error、incorrect file、edge case、無限迴圈、無止盡讀檔、context overflow。不同模型的主要失敗原因差很多：Opus 4.1 以 wrong solution（35.9%）為主，Sonnet 4 以 context overflow（35.6%）與無止盡讀檔（17.0%）為主。[論文](https://arxiv.org/abs/2509.16941) | 【已證實】 |

**對 dcode 的結論【推論】**：dcode 的 VerifyGate 方向正確（外部回饋 + Stop-hook 式的強制繼續），但它只回答「最後一次檢查是否通過」，沒有回答「失敗的測試是我造成的嗎」。§4.3 的 7 個失敗都卡在這裡。

### 2.6 Subagent

| 來源 | 內容 | 標記 |
|---|---|---|
| Anthropic | multi-agent research 系統的 token 用量約為一般對話的 15 倍（單一 agent 約 4 倍）；適合高價值、可大量平行、資訊量超過單一 context 的任務。**大多數 coding 任務能平行的部分比研究少**，LLM 也還不擅長即時協調。[Multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system) | 【已證實】 |
| Cognition | 平行 agent 寫同一個產出物會做出互相矛盾的隱含決策；目前有效的是唯讀的研究 / 審查型 subagent，寫入維持單執行緒。[Multi-agents: what's actually working](https://cognition.com/blog/multi-agents-working) | 【已證實】 |

**對 dcode 的結論【推論】**：explorer（唯讀）是合理的唯一預設委派。032626 那題 b398 用了 4 次 task、43 次 subagent 模型呼叫，最後仍逾時（§4.2），說明「多委派」本身不會提升解題率。

### 2.7 Sandbox 與權限

| 來源 | 內容 | 標記 |
|---|---|---|
| Claude Code | macOS 用 Seatbelt、Linux / WSL2 用 bubblewrap + socat；檔案系統與網路是兩個獨立的隔離層；預設只能寫工作目錄與 session 暫存目錄；網路透過 proxy 依網域白名單放行；能 sandbox 的指令自動核准，不能的走一般權限流程。[Sandboxing](https://code.claude.com/docs/en/sandboxing) | 【已證實】 |
| Codex | `read-only` / `workspace-write` / `danger-full-access` 三種 sandbox，搭配獨立的 approval policy；`workspace-write` 預設關網路；macOS 用 `sandbox-exec`，Linux 用 bwrap + seccomp。[sandbox.md](https://github.com/openai/codex/blob/13c42a077c88a0d04ae7680a9891d2daf4558577/docs/sandbox.md) | 【已證實】 |

**對 dcode 的結論【推論】**：dcode 目前本機 shell 沒有任何 OS 層隔離，`code/sandbox` 是 `notimpl` stub。dbench 跑在斷網容器裡，所以不影響 benchmark，但影響日常使用的安全。這是 P2（production maturity），不影響正確性排序。

### 2.8 Memory、Checkpoint、Observability

| 來源 | 內容 | 標記 |
|---|---|---|
| Claude Code | 專案記憶用 `CLAUDE.md`（分層：企業 / 使用者 / 專案）；編輯前保存檔案快照以便 rewind，與對話恢復分開。[Memory](https://code.claude.com/docs/en/memory)、[Checkpointing](https://code.claude.com/docs/en/checkpointing) | 【已證實】 |
| OpenHands | 事件可序列化並持久化，condensation 本身也是事件（記錄被遺忘的 event id），恢復時重建 view。[Condenser 架構](https://docs.openhands.dev/sdk/arch/condenser) | 【已證實】 |
| 共通點 | 專案記憶以人可編輯的 markdown 為主；跨 session 的自動記憶都很保守；trajectory 可回放。 | 【公開模式】 |

### 2.9 其他系統（本輪未重新查證細節）

Continue（context provider、LSP 定義與 imports）、Cline / Roo Code（task checkpoint、workspace 快照與對話恢復分離）、Goose（工具層級 allow / ask / deny）、Sourcegraph Cody（搜尋 + code graph）、GitHub Copilot coding agent（隔離執行環境 + firewall，文件列出 firewall 不涵蓋的路徑）。相關連結見同目錄的 `recontruct_plan_gpt6_astra.md` §2。這些設計與上面的結論一致，沒有改變優先順序。

---

## 3. Phase 2：Current Architecture Audit

### 3.1 Current Architecture

```text
cmd/dcode（CLI / TUI / GUI / headless）     cmd/dbench（評測）
        │                                        │
        ▼                                        ▼
code/server.Service（thread 鎖、agent 快取、模型選擇）   code/bench.DcodeSolver（斷網容器、docker exec）
        │                                        │
        └──────────────► code/agent.CreateCLIAgent ◄┘
                              │  system prompt、codetools、web tools、explorer / web-researcher / 自訂 subagent
                              ▼
                 deepagents.CreateDeepAgent（assembly.go 組 middleware）
                              ▼
                 agent.loopAgent（langgraphgo MessageGraph，5 個 node）
                              │
     ┌────────────────────────┼─────────────────────────┐
     ▼                        ▼                         ▼
 llm.ChatModel           tool.Tool + middleware      runtime（State、Checkpointer、Store）
 OpenAI / OpenRouter     filesystem、codetools、      Postgres checkpoint（每 thread 10 筆）
 重試、串流、計時         subagents、web、todo …        Redis artifacts / cache
                              │
                              ▼
                 backend（Filesystem / LocalShell / DockerExec / State / Store / Composite）
```

套件規模（不含測試與前端）：`code/bench` 4.9k 行、`code/codetools` 4.8k、`backend` 4.2k、`code/agent` 3.2k、`code/server` 3.0k、`agent` 2.2k；Go 程式碼總計約 51k 行。

### 3.2 Current Agent Loop

```text
before_agent ─► model ─► after_model ─┬─(有未回覆的 tool call)─► tools ─► model
                                      ├─(全部在 after_model 被回覆，如 HITL 拒絕)─► model
                                      └─(沒有 tool call)─► after_agent ─┬─(VerifyGate 擋下)─► model
                                                                         └─► END
```

- 每個 node 結束時透過 `stepCallback` 存一次 checkpoint（`agent/loop.go` 的 `OnGraphStep`）。
- tools node 用同一份 state 快照平行執行最多 8 個 tool call，結果依呼叫順序套回。
- 停止條件：模型不再呼叫工具；`RecursionLimit`（dbench 為 250 步）；dbench 另有成本上限（$3）與 50 分鐘逾時。

### 3.3 Current Context Flow

每次模型呼叫的請求：

```text
[system] 靜態 prompt（system_prompt.md 模板）
       + 各 middleware 附加：filesystem 說明、todo 說明、memory（AGENTS.md）、skills 清單、subagent 清單
       + liveContext：模型身分 + 「Project Context」（每次呼叫都重跑 git status，附在 system message 尾端）
[messages] 完整對話歷史（所有 tool result 原樣保留）
[tools] 全部工具定義
```

Context 只有兩個縮減機制：

1. **Eviction**：單一 tool result 超過 20k tokens 時寫到 artifact，context 只放路徑加頭尾各 5 行。
2. **Summarization**：總量達模型 window 的 80%（1M window 的模型約 840k；沒有 profile 的模型 170k）時，用 LLM 摘要舊歷史，完整歷史存 Postgres。

這兩個機制之間沒有任何東西：20k tokens 以下的 tool result 永遠留在 context，直到 80% 門檻。

### 3.4 Current Tool Flow

```text
模型 tool call
 → HITL（需核准的工具中斷等待；headless 自動核准）
 → middleware WrapToolCall 鏈（VerifyGate 服務提示、planmode 白名單、explorer researchLimits、trace …）
 → tool.Invoke（backend 讀寫 / 執行）
 → filesystem eviction（> 20k tokens 才寫 artifact）
 → tool message 附加到 state
```

工具清單（headless dbench）：`ls`、`read_file`（預設 100 行）、`write_file`、`edit_file`、`delete`、`glob`、`grep`（**字面子字串**比對，不支援 regex，不讀 `.gitignore`）、`execute`、`git_status`、`git_diff`、`apply_patch`、`run_tests`、`diagnostics`、`write_todos`、`task`、`task_output`。

### 3.5 Current Failure Recovery Flow

| 失敗 | 現在的處理 |
|---|---|
| 模型 API 429 / 5xx / 空回應 | 自動重試（預設 2 次），串流閒置 5 分鐘視為卡住 |
| 工具錯誤 / panic / 未知工具 | 轉成 error tool message 讓模型修正 |
| 被取消的 tool call | PatchToolCalls 下一輪補上「未完成」結果 |
| Context 超出 | `ErrContextOverflow` → 摘要後重試；摘要失敗時改成「摘要不可用，完整歷史在 <path>」並繼續（Stage 16b） |
| 改了檔案卻沒驗證 / 驗證失敗 / 只跑過濾後的測試 | VerifyGate 擋回模型，每輪最多 2 次，第 2 次要求在回覆中說明未驗證 |
| 同一個錯誤反覆出現 | **沒有偵測**，只靠 prompt「不要用同樣方法修同一個錯超過 3 次」 |
| 行程 crash | 從最後一個 node 的 checkpoint 續跑；工具副作用與 checkpoint 之間有空窗 |

### 3.6 Good Design（保留）

- **介面清楚**：`ChatModel`、`Tool`、`Backend`（含 capability 介面）、`Checkpointer`、`Store`；middleware 以可選介面表達 hook，不需要繼承。
- **核心 loop 小**（`agent/loop.go` 839 行），平行工具執行、panic 防護、interrupt / resume 都有測試。
- **結構化 coding 工具品質高**：`run_tests` / `diagnostics` 把 Go / JS / Python 測試與 lint 壓成精簡 JSON，平均每次 0.1–0.5k 字元；`apply_patch` 全有或全無。
- **VerifyGate** 是 deterministic 的完成閘門，已證明能擋下「只跑 lint 就宣告完成」與「只跑過濾後的測試」。
- **dbench 已經是很好的評測基礎**：斷網容器、官方 verifier、F2P / P2P 明細、Wilson 區間、McNemar、paired bootstrap、完整 trace 存 Postgres。
- **Artifact 基礎建設已存在**：Redis 的 large tool results、Postgres 的 conversation history、`/.deepagents/...` 虛擬路徑可用 `read_file` 讀回。

### 3.7 Technical Debt

| 項目 | 位置 | 說明 |
|---|---|---|
| Middleware 排序隱含 | `assembly.go` `buildMiddleware`、`placeSummarizationLast`、同名取代 | 核心 stack、caller middleware、profile middleware 三段合併，再把 summarization 移到最後。行為正確性依賴排序，但排序規則分散在多個函式 |
| 驗證判定靠文字 | `code/agent/verify.go` | 從 transcript 用 regex 判斷「這是測試指令」「exit code 多少」「有沒有 filter」。`execute` 改檔案不算 edit；`npm` fallback 沒有測試數 |
| 指標錯誤 | `code/bench/metrics.go` | `TestRuns` 只算 `execute` 的測試指令，`run_tests` 不算，所以 Stage 12 之後的 attempt 幾乎都是 0 |
| 定位指標誤導 | `gold_file_recall` | 解出來的題目 recall 只有 0.13–0.25（gold patch 含測試與其他檔案），不能代表定位準確度 |
| Stub 套件 | `code/sandbox`、`code/mcp`、`code/hooks`、`code/plugins`、`code/extensions`、`code/offload`、`code/approval`、`middleware/promptcaching`、`middleware/rubric` | 只有介面或 `notimpl`。增加閱讀成本，也容易讓人誤以為功能存在 |
| 兩份重構計畫 | `docs/recontruct_plan_gpt6_astra.md` 與本文件 | §10 說明兩者差異與取捨 |

### 3.8 Architectural Bottleneck

1. **Context 只會長大**（§3.3）。Token 成本約與步數平方成正比，長任務會越跑越慢、越貴，也越容易發生 context rot。
2. **Prompt cache 前綴不穩定**：`liveContext` 每次模型呼叫都把最新 `git status` 附加在 system message 尾端。工作樹一改，system message 就變了，**後面整段對話歷史都失去 cache**。dbench 用固定的 `ProjectContext`，所以 benchmark 看不到這個成本，但日常互動會付。
3. **完成判定沒有 baseline 概念**：VerifyGate 不知道失敗的測試在原始程式碼上會不會過，所以 agent 可以把自己造成的 regression 說成「與改動無關的環境問題」然後結束（bd80，Stage 16 已記錄）。
4. **沒有進度 / 停滯偵測**：loop 只用步數、成本、時間限制。同一個 patch 失敗 5 次、同一組 diagnostics 跑 7 次都不會觸發任何策略改變。

### 3.9 Missing Capability

- Observation masking / 舊 tool result 清除。
- Shell 大輸出完整保存：`LocalShellBackend` 的 `cappedBuffer` **只保留前 100KB、丟掉尾端**，而測試摘要與錯誤通常在尾端。之後的 eviction 也找不回被丟掉的部分。
- Baseline 測試比對（「這個測試在改動前就會失敗嗎？」）。
- 相同失敗 / 相同 tool call 偵測。
- 最終 diff 檢查：dump.rdb 被收進 patch（bd80），b398 的 patch 改了 64 個檔案（含所有語系檔）都沒有被攔下。
- regex 搜尋、`.gitignore` 感知、glob 排除依賴目錄（glob 目前不排除 `node_modules`，平均每次 7.5k 字元）。
- 符號層級的導覽（outline / definition / references）。
- OS 層 sandbox、secret 遮蔽、子程序環境變數最小化。
- 任務層級的 token / 成本 / 剩餘預算回饋給 agent。

### 3.10 Unnecessary Complexity

- **預設推動委派**（Stage 17 prompt）：沒有證據提升解題率，已觀察到一次成本暴增（§4.2）。建議保留 explorer，但移除「proactively」措辭，並以 A/B 決定。
- **Python 版功能對照的完整度**：AsyncSubAgent、fork mode、structured output、LangSmith 等功能對 dcode 的 coding 成功率沒有直接貢獻。保留，但不再往這方向擴充。
- **Stub 套件**：沒有使用者的 stub 先移出預設建置或標示為 experimental（不刪，只是不要讓它們出現在功能清單）。

### 3.11 Potential Failure Mode

| 失敗模式 | 觸發條件 | 證據 |
|---|---|---|
| Context 爆炸、成本長尾 | 大型 JS repo、多次完整跑測試、反覆讀檔 | b398：577k context、48M input tokens、$1.62、逾時 |
| 把 regression 當成環境問題 | 改壞的是 P2P 測試，agent 沒在原始碼上比對 | bd80 × 3、b398 × 4 |
| 本地通過、評分失敗 | 測試順序、Redis 殘留狀態、flaky | b398 本地 `test/messaging.js` 75/75，評分有 1 個失敗 |
| 工作目錄殘留檔案 | 提示的 `redis-server --daemonize yes` 在 `/app` 留下 `dump.rdb` | bd80 patch |
| 盲目重試 | 同一個 `apply_patch` 失敗後原樣重送 | b398 同一 patch 5 次 |
| 摘要後遺失細節 | 未知 window 門檻設太低 | Stage 12：16k 門檻造成每幾步摘要一次、343 次 tool call 沒有任何修改 |
| 工具副作用重放 | checkpoint 存檔前 crash | 程式結構推論，尚無實例 |

---

## 4. Phase 6：Baseline（以過去測試為準）

所有數字來自 dbench Postgres 與 trace。模型都是 `openrouter:openai/gpt-6-luna-pro`，SWE-bench Pro V2 smoke50 的前 10 題（NodeBB 4 題 JS、ansible 6 題 Python），每題 1 次。

### 4.1 Run 層級

| Run | 程式版本 | 題數 | 解出 | 總成本 | 中位成本 | 備註 |
|---|---|---|---|---|---|---|
| `20260930-160724` | Stage 12 before（無 gate） | 10 | 8 | $0.52 | $0.047 | |
| `20260930-160743` | Stage 12 after（有 gate） | 10 | 8 | $0.58 | $0.042 | 解出的題目與上一行相同 |
| `20261001-151704` | Stage 16 | 3（先前失敗的題目） | 1 | $0.83 | $0.282 | 8168 解出，b398、bd80 仍是 regression |
| `20261002-032626` | Stage 17（dirty tree） | 5 | 4 | $2.03 | $0.063 | b398 逾時，一題花掉總成本的 80% |

### 4.2 每題結果矩陣

| 題目 | 160724 | 160743 | 151704 | 032626 | 判讀 |
|---|---|---|---|---|---|
| NodeBB-8168 | ✓ | ✓ | ✓ | ✓ | 穩定解出，但成本 $0.06–$0.36 浮動很大 |
| NodeBB-a5af | ✓ | ✓ | – | ✓ | 穩定 |
| NodeBB-b398 | ✗ P2P 3053/3057 | ✗ P2P 3045/3057 | ✗ P2P 3056/3057 | ✗ 逾時（P2P 3056/3057） | **F2P 每次 8/8**；regression + 成本長尾 |
| NodeBB-bd80 | ✗ P2P 178/179 | ✗ P2P 178/179 | ✗ P2P 178/179 | ✓ | **F2P 每次 1/1**；regression |
| ansible-34db | ✓ | ✓ | – | ✓ | 穩定 |
| ansible-5640 / 5d25 / 9842 / c616 / e9e6 | ✓ | ✓ | – | – | 穩定；c616 在 160743 花了 59 次模型呼叫 |

### 4.3 失敗分析

- 28 個 attempt 中 7 個沒解出：**7 個的 F2P 全部通過**，其中 6 個是 P2P regression（1–12 個原有測試失敗），1 個是逾時（逾時時的 patch P2P 也是差 1 個）。
- 所以在這個樣本裡：**task understanding 與 localization 都不是瓶頸**；瓶頸是「改壞旁邊的東西 + 沒能自己發現或證明」。
- bd80 的 trace（Stage 16 已記錄）：閘門擋下後 agent 跑完整的 `test/controllers.js`，看到 `should export users posts` 失敗，判定為「與改動無關」就結束。這個測試在原始程式碼上會過。
- b398：本地完整跑 `test/messaging.js` 75/75，評分卻有 1 個 messaging 測試失敗。根因未查明，可能是測試順序或 Redis 殘留狀態。

### 4.4 過程指標

| 指標 | 早期 20 個 attempt（160724 + 160743） | 最近 8 個 attempt（151704 + 032626） |
|---|---|---|
| 平均模型呼叫數 | 27.1 | 58.8 |
| 平均 input tokens / 題 | 1.77M | 10.74M |
| Prompt cache 命中率（cache_read / input） | 88% | 79% |
| 最高單次 context | 218k | 577k |
| 完全相同的重複 tool call / 題 | 4.0 | 18.8 |
| 只有 `write_todos` 的模型回合 / 題 | 3.4（佔模型呼叫 12.5%） | 3.4 |
| Patch 平均檔案數 | 3.4 | 12.0（b398 一題 64 個） |
| 觸發 summarization | 0 次 | 0 次 |

Tool output 佔比（以字元計、每個 tool message 只算一次）：

| 工具 | 早期 20 個 | 最近 8 個 | 平均每次 |
|---|---|---|---|
| `read_file` | 53% | 39% | 2.6–4.4k |
| `grep` | 20% | 27% | 1.9–2.6k |
| `git_diff` | 17% | 14% | **9.2–10.3k** |
| `glob` | 2% | 12% | 1.7–7.5k |
| `execute` | 3% | 4% | 1.3–1.5k |
| `run_tests` + `diagnostics` | 1% | 1% | 0.1–0.5k |

時間：agent 時間中模型等待佔大宗（典型題目 model 116–861 秒、tool 16–953 秒）。最近幾次 tool 時間變長，主因是完整跑 NodeBB 測試檔（每次數分鐘）與 3 個容器同時執行。

### 4.5 Baseline 中發現的異常（需要 Phase A 量測）

1. **使用者訊息邊界的 token 跳增**：bd80（032626）在 VerifyGate 注入訊息時，新增內容只有約 1k 字元，input 卻從 67.9k 跳到 93.1k（+25k），之後 cache 命中率從 92% 掉到 66–70% 並維持。每次 gate 介入都重複一次。假設：provider 在新的 user turn 重放 reasoning 內容，或 OpenRouter 換了 provider 路由。需要比對「我們送出的 token 數」與「provider 回報的 token 數」。
2. **Cache 命中率下降**：從 88% 降到 79%，與上一點可能同源。
3. **單題成本變異大**：8168 同一題 $0.063–$0.355，差 5.6 倍。單次 attempt 的成本比較需要至少 2 次重複。

### 4.6 Baseline 的限制

- 只有 10 題、每題 1 次；兩種語言、兩個 repo；沒有 Go 題。
- 早期 run 沒有記錄 dcode commit；032626 是 dirty tree。
- 所以這份 baseline 只能用來**找出失敗類型與成本結構**，不能用來宣稱解題率的絕對水準。之後的比較以 §9 的 regression 子集與配對比較為主。

---

## 5. 你可能沒想到的 Unknown Unknowns

1. **「失敗的測試是不是我造成的」是一個需要證據的問題，而且可以用程式回答。** 在乾淨的 HEAD（`git stash` 或 `git worktree`）上重跑同一個測試，就能 deterministic 地區分 regression 與既有失敗，不需要 LLM 判斷。
2. **Prompt cache 是成本的一級因素，而且很容易被不經意地破壞。** 任何放在對話歷史前面、會變動的內容（git status、時間、todo 狀態）都會讓整段歷史失去 cache。動態內容要放在尾端。
3. **Provider 回報的 token 數不一定等於你送出的內容。** reasoning model 的隱藏推理、provider 路由與 cache 分片都會影響帳單。沒有量測就無法優化。
4. **Benchmark 的指標本身會誤導**：`gold_file_recall` 會懲罰其他正確的解法，`test_runs` 漏算 `run_tests`；只看解題率會忽略成本長尾（一題吃掉 80% 預算）。
5. **測試在本地通過不代表評分通過**：測試順序、共享服務（Redis）的殘留狀態、flaky test。驗證時需要乾淨的服務狀態，或至少知道哪些測試共用狀態。
6. **Agent 會在工作目錄留下副產品**（dump.rdb、暫存腳本、`__pycache__`），它們會進入 patch。最終 diff 需要 deterministic 檢查。
7. **重複讀同一個檔案不一定是壞事**：舊的讀取結果被 mask 之後，agent 需要能便宜地重讀。所以 observation masking 與「讀取工具要省 token」必須一起設計。
8. **完成閘門本身會增加成本**：bd80 在閘門介入後又跑了約 40 步。閘門要附帶具體、可行的下一步（例如「在原始碼上跑這個測試」），否則只是延長迴圈。
9. **Subagent 會繞過主 agent 的預算感知**：subagent 的模型呼叫不算在 `RecursionLimit` 內，只算在總成本裡。
10. **評測環境與日常環境的差異**：dbench 斷網、固定 `ProjectContext`、自動核准；日常使用有 git status 變動、需要核准、可能和 Cursor 共用工作目錄。只在 dbench 上優化，可能看不到日常使用的成本（例如 §3.8 的 cache 問題）。

---

## 6. Phase 3：Gap Analysis

Priority：P0 = 直接影響正確性；P1 = 主要可靠性 / 效能；P2 = production 成熟度；P3 = 未證明必要。

| 能力 | 現況 | 業界模式 | 問題 | 優先級 |
|---|---|---|---|---|
| Regression 判定 | VerifyGate 只看最後一次檢查是否通過 | Agentless 用 regression test 篩 patch；有外部回饋的自我修正才有效 | 7/7 個失敗都是 P2P regression 或逾時，agent 把 regression 判成無關 | **P0** |
| 最終 diff 檢查 | 無 | Claude Code / Codex 皆強調完成前 review diff（prompt 層級） | dump.rdb 進 patch；一題改 64 檔沒被攔下 | **P0** |
| 評測指標正確性 | `TestRuns` 漏算、`gold_file_recall` 誤導、沒有重複呼叫 / cache / context 組成指標 | SWE-bench Pro 失敗分類、成本 / 時間一併報告 | 無法判斷改動是否有效 | **P0** |
| Context 成長 | 只有 >20k eviction 與 80% 摘要 | Observation masking、tool result clearing、按需讀回 | 平均 1.8–10.7M input tokens / 題、577k context、成本長尾 | **P1** |
| Shell 輸出 | 只保留前 100KB，丟尾端 | Cursor：完整寫檔，agent 用 tail 讀；Anthropic：截斷要附下一步提示 | 測試摘要與錯誤在尾端時會遺失 | **P1** |
| 相同失敗偵測 | 只有 prompt | mini-SWE-agent 對重複格式錯誤設上限；SWE-agent 拒絕無效編輯 | 同一 patch 失敗 5 次、同一 diagnostics 跑 7 次 | **P1** |
| Prompt cache 穩定性 | git status 附在 system message 尾端；gate 邊界有 token 跳增 | Codex：每次請求是上一次的完全前綴延伸，動態內容放尾端 | 互動模式每次改檔都讓整段歷史失去 cache | **P1** |
| 搜尋工具 | 字面子字串 grep、不讀 `.gitignore`、glob 不排除依賴 | SWE-agent：結果上限 + 要求縮小查詢；rg + gitignore 為業界預設 | grep / glob 佔 30–40% tool output | **P1** |
| `git_diff` 輸出 | 每次回完整 diff | Anthropic：concise / detailed 模式 | 平均每次 9–10k 字元，被反覆呼叫 | **P1** |
| 預算感知 | agent 不知道剩多少步數 / 時間 / 成本 | 【推論】長任務需要 | 逾時前沒有收斂行為 | **P1** |
| Subagent 契約 | 有隔離、背景、用量統計；無預算與輸出格式約束 | Anthropic：subagent 回 1–2k tokens 摘要；Cognition：寫入單執行緒 | 一題 43 次 subagent 呼叫仍逾時 | **P1** |
| 符號導覽（outline / definition / references） | 無 | Claude Code LSP、Aider repo map、Agentless skeleton | 目前樣本中定位不是瓶頸；大型 repo 可能是 | **P2** |
| 本機 sandbox | 無 OS 隔離；`code/sandbox` 是 stub | Seatbelt / bwrap、檔案與網路分層 | 日常使用的安全風險；不影響 dbench | **P2** |
| Secret 防護 | 專案 `.env` 不能開 tracing；其他無 | 遮蔽、最小化環境變數、網路政策 | trace 可能存下 secret；子程序繼承完整環境 | **P2** |
| Crash 時的工具副作用 | checkpoint 在 node 結束時保存 | OpenHands：未配對的 action 要特別處理 | 重啟可能重放非冪等操作 | **P2** |
| Durable run / GUI 重連 | run 綁 HTTP request context | 服務擁有 run，UI 訂閱 | 日常體驗問題，`recontruct_plan_gpt6_astra.md` 有詳細設計 | **P2** |
| Repo map / LSP / 語意搜尋 | 無 | Aider、Claude Code、Cursor | 沒有本專案的收益證據 | **P3** |
| 獨立 reviewer / 不同模型 review | 無 | 部分系統有 | 多一次模型呼叫，收益未知 | **P3** |
| Model routing | 只有 `models.web` | 部分系統有 | 先 benchmark | **P3** |
| 跨 session 自動記憶 | 只有 AGENTS.md | 主流做法保守 | 容易存下過時資訊 | **P3** |

---

## 7. Phase 4：Target Architecture

### 7.1 原則

- **不重寫**。保留 `agent.loopAgent`、tool / backend / model 介面、dbench、GUI / TUI。
- **新增的每個模組都要對應到 §4 或 §6 的具體失敗**，並有對應的 benchmark 指標。對應不上的就不做。
- **Deterministic 問題用程式解**：版本比對、預算、重複偵測、baseline 比對、diff 檢查、context 淘汰都是程式。LLM 只負責理解需求、推理未知程式碼、提出修復假設。
- **核心控制邏輯不放在 middleware 排序裡**。完成判定、context 組裝、工具執行策略做成明確的元件，loop 直接呼叫；middleware 只留 tracing、HITL、plan mode 這類橫切功能。

### 7.2 架構圖

```text
               User / CLI / GUI / dbench
                         │
                         ▼
                Session / Run Service（現有 code/server，不改）
                         │
                         ▼
     ┌──────────────── Agent Loop（現有 loopAgent）────────────────┐
     │                                                             │
     │  model call ◄── Context Builder ◄── Task Ledger             │
     │      │             (穩定前綴 | 遮蔽後的歷史 | 動態尾端)      │
     │      ▼                                                      │
     │  tool calls ──► Tool Runtime ──► Backend（FS / shell / docker）│
     │                    │ 輸出信封 + artifact                     │
     │                    └──► Task Ledger（讀過 / 改過 / 證據 / 失敗）│
     │      │                                                      │
     │      ▼  模型想結束時                                         │
     │  Completion Gate ──► Verification Service（baseline 比對）    │
     │      │             └► Diff Inspector                        │
     │      ▼                                                      │
     │  Progress Monitor（相同失敗 / 重複呼叫 / 預算）               │
     └─────────────────────────────────────────────────────────────┘
                         │
                         ▼
          Trace / Metrics（現有 code/trace + dbench 擴充）
```

和假設架構（題目 Part 36）的差異：**不做獨立的 Task Understanding 層、Orchestrator、Researcher / Reviewer subagent**。理由見 §7.5。

### 7.3 模組、存在理由與介面

#### Task Ledger（session working memory）

- **為什麼存在**：observation masking 之後，agent 需要一個不會被遮蔽、很短的「目前狀態」；Completion Gate 與 Progress Monitor 需要結構化資料，而不是用 regex 掃 transcript。
- **怎麼來**：完全由 Tool Runtime 從工具事件推導（不需要模型呼叫）。
- **內容**：

```go
type Ledger struct {
	Requirements []string        // 從任務文字的 Requirements 清單解析；解析不到就留空
	FilesRead    map[string]Span // 路徑 → 讀過的行範圍
	FilesChanged []string        // 本 run 改過的檔案（含 execute 造成的，以 git status 比對）
	Evidence     []Evidence      // 測試 / build 結果，綁定 workspace 版本
	Failures     []Failure       // 正規化後的失敗指紋與出現次數
	Budget       Budget          // 已用步數 / 時間 / 成本與上限
}

type Evidence struct {
	Check       string   // run_tests / diagnostics / execute 指令
	Files       []string // 測試檔
	Filter      string   // 測試名稱過濾，"" 代表整檔
	TreeHash    string   // 驗證當下 `git diff` 的雜湊
	Passed      bool
	Failing     []string // 失敗的測試 ID
	BaselineRef string   // 有做 baseline 比對時的結果
}
```

- **呈現**：每次模型呼叫時，Context Builder 把 Ledger 渲染成 ≤ 1k tokens 的區塊放在動態尾端。
- **持久化**：放進 `runtime.State` 的一個 key，跟 checkpoint 一起保存；crash 後自然恢復。

#### Context Builder

- **為什麼存在**：解決 §3.8 的 context 只會長大與 cache 前綴不穩定。目前 context 由多個 middleware 各自改寫 system message 與 messages，沒有單一位置能控制預算。
- **輸出結構**：

```text
[穩定前綴]  system prompt + 專案指示（AGENTS.md）+ skills 清單 + 工具定義
            整個 run 內不變（AGENTS.md 改了才更新）
[歷史]      對話歷史；舊的 tool result 依規則遮蔽
[動態尾端]  Task Ledger + live git status + 預算；附在最後一則訊息之後，不進入歷史
```

- **遮蔽規則**（deterministic，依序）：
  1. **永遠保留**：使用者訊息、assistant 文字與 tool call 參數、最近 K 個 tool result（初始 K = 10，A/B 決定）、最近一次測試 / diagnostics 結果、所有 error 結果中最近的 3 個。
  2. **可重建 → 遮蔽**：舊的 `read_file`、`grep`、`glob`、`ls`、`git_status`、`git_diff`。換成一行：`[省略 3.2k 字元：read_file /src/x.js 1-100；已存為 <artifact>；需要時重新讀取]`。
  3. **不可重建 → 遮蔽但保證可讀回**：舊的 `execute` / `run_tests` 輸出寫入 artifact（Postgres，不放會過期的 Redis），context 留指令、exit code、失敗摘要與 artifact 路徑。
  4. 遮蔽後仍超過門檻才用現有的 LLM summarization。
- **與現有程式的關係**：取代 `liveContext`（移到動態尾端）；filesystem eviction 保留作為單一超大結果的保護；summarization 保留作為最後手段。
- **介面**：`Build(state, ledger, budget) *ModelRequest`，純函式，可以用 table-driven 測試。

#### Tool Runtime（輸出信封）

- **為什麼存在**：shell 只保留前 100KB 會丟失測試摘要（§3.9）；`git_diff` / `glob` 輸出過大（§4.4）；Ledger 需要統一的工具事件。
- **內容**：
  - 每個工具宣告 metadata：`ReadOnly | Writes | Executes`、可否平行、輸出預算。
  - 統一輸出信封：超過預算時，完整輸出寫入 artifact，context 放「指令 / exit code / 前 N 行 / 後 N 行 / 摘要 / artifact 路徑」。shell 改成保留頭尾，不再只保留頭。
  - `git_diff` 預設回 stat 加每檔前 N 行，帶 `path` 參數才回完整 diff。
  - 每次工具呼叫後更新 Ledger。
- **不做**：通用 retry 策略（工具錯誤交給模型處理已經足夠）。

#### Verification Service（baseline 比對）

- **為什麼存在**：直接對應 7/7 的失敗。
- **行為**：
  1. 測試失敗時，若失敗的測試 ID 不在本 run 的新增測試裡，自動在乾淨的 HEAD 上重跑**同一批失敗測試**（`git stash -u` → 跑 → `git stash pop`，或 `git worktree add` 到暫存目錄；dbench 容器用前者，本機與 Cursor 共用目錄時用 worktree，避免動到使用者的工作樹）。
  2. 結果寫入 `Evidence.BaselineRef`：`regression`（HEAD 上通過、現在失敗）、`pre-existing`（HEAD 上也失敗）、`flaky`（HEAD 上重跑結果不一致）。
  3. 結果附在工具輸出裡告訴 agent，例如：「`should export users posts` 在原始程式碼上通過，是你的改動造成的 regression」。
  4. 共用服務（Redis 等）的提示改成 `redis-server --daemonize yes --save '' --dir /tmp`，不在工作目錄留檔。
- **成本控制**：只重跑失敗的測試、每個失敗指紋只比對一次、有時間上限。

#### Completion Gate（取代 VerifyGate）

- **為什麼存在**：VerifyGate 只檢查「最後一次檢查是否通過」，不知道 regression，也不檢查 diff。
- **放行條件**（全部 deterministic）：
  1. 最後一次修改後有不帶過濾的通過證據（現有規則保留）。
  2. 沒有標記為 `regression` 的失敗；`pre-existing` 的失敗可以放行，但要在最終回覆列出。
  3. Diff Inspector 沒有阻擋項目。
  4. 擋下次數上限仍為 2（避免無限迴圈），第 2 次後放行並要求在回覆說明未解決項目。
- **Diff Inspector**：檢查最終 `git diff`：
  - 新增的二進位檔、`dump.rdb`、`*.log`、`__pycache__`、暫存腳本 → 阻擋並要求移除。
  - 改動檔案數超過門檻（例如 > 15）或改動了大量同類檔案（例如所有語系檔）→ 要求確認每個檔案都是需求的一部分。
  - 有 Requirements 清單時，附上清單請 agent 逐項對照（只是附上，不額外呼叫模型）。
- **適用範圍**：headless 預設開；互動模式改成在完成時「顯示」檢查結果而不強制擋下（使用者在場）。

#### Progress Monitor

- **為什麼存在**：同一 patch 失敗 5 次、同一組 diagnostics 跑 7 次、逾時前沒有收斂（§4.4、§3.11）。
- **規則**：
  - 相同 tool call（名稱 + 參數）且相同錯誤結果第 2 次 → 在結果附註「這個呼叫剛剛以同樣方式失敗」。第 3 次 → 拒絕執行並要求換方法。
  - 同一失敗指紋（正規化後的測試名稱 + 錯誤首行）在 3 次修改後仍存在 → 提示「重新檢查假設：重讀失敗位置與相關程式碼，不要再修同一處」。
  - 預算剩 20% → 動態尾端提示「開始收斂：完成驗證、檢查 diff、準備回報」。
- **實作位置**：Tool Runtime 呼叫後、模型呼叫前；不需要新的 graph node。

### 7.4 各 Flow

**Agent loop**：維持現有 5 個 node。差異只有：model node 透過 Context Builder 組請求；tools node 透過 Tool Runtime 執行並更新 Ledger；after_agent 呼叫 Completion Gate。

**Context flow**：`State.Messages`（完整、不改動）→ Context Builder（遮蔽、尾端注入）→ ModelRequest。State 保持完整，所以 trace、GUI、checkpoint 都不受影響；遮蔽只發生在送給模型的那一份。

**Tool flow**：tool call → HITL → Tool Runtime（metadata 檢查、執行、輸出信封、artifact、Ledger 更新、Progress Monitor 附註）→ tool message。

**Error flow**：

```text
工具 / 測試失敗
 → Tool Runtime 正規化失敗指紋並寫入 Ledger
 → 測試失敗且不是新增的測試：Verification Service 在 HEAD 上比對 → 標記 regression / pre-existing / flaky
 → Progress Monitor 檢查重複次數 → 必要時附上換策略提示
 → 模型決定修復
```

**Subagent flow**：維持現有 `task` 工具與 explorer。加上契約：輸入要有目標與期望輸出；subagent 有自己的步數上限（預設 30）並計入父 run 的成本預算；回傳限制在約 2k tokens（finding / evidence / files / uncertainty）。寫入型 subagent 不平行執行。

**Checkpoint flow**：Ledger 存在 State 裡，跟著現有 checkpoint 保存。工具副作用的 durable ledger（記錄工具開始 / 完成，以便 crash 後不重放非冪等操作）列為 P2，設計沿用 `recontruct_plan_gpt6_astra.md` Phase C。

### 7.5 明確不做（以及什麼證據會讓我改變主意）

| 不做 | 理由 | 重新考慮的條件 |
|---|---|---|
| Task Understanding / 分類模型呼叫 | 7/7 失敗與需求理解無關；多一次呼叫增加延遲 | regression 子集出現「誤解需求」類失敗 ≥ 2 題 |
| 獨立 Planner node / 強制 plan | `write_todos` 已佔 12.5% 模型回合；SWE-bench 任務已附 Requirements | 跨模組任務的失敗分析顯示計畫錯誤 |
| Reviewer subagent / 不同模型 review | Diff Inspector + baseline 比對是 deterministic 且直接針對已知失敗 | Phase C 後仍有 regression 漏網，且失敗是語意性的 |
| Model router | 沒有 benchmark；單題成本變異已經很大 | 有穩定的成本 / 品質量測後 |
| Repo map / LSP / 語意索引預設啟用 | 目前樣本定位不是瓶頸 | 正確的定位指標顯示定位失敗，或在 Go / 大型 repo 題上首次正確編輯前的工具呼叫明顯偏高 |
| 更多 graph node | loop 已經有需要的控制點 | — |
| 跨 session 自動記憶 | 容易存下過時的 repo 資訊 | — |

---

## 8. Phase 5：Migration Plan

每個 Phase 都要：可獨立測試、預設行為可用設定切回舊版、可用 §9 的協定量測、可回退（新功能都有開關）。實作開始時把對應 Stage 寫入 `IMPLEMENTATION_PLAN.md`，完成後更新 Status。

### Stage A：可信量測與 regression 子集（不改 agent 行為）

**Goal**：讓之後每一個改動都能被正確量測。
**變更**：
- A1 `metrics.go`：`TestRuns` 納入 `run_tests`；新增 `duplicate_tool_calls`、`masked_chars`（Stage B 後）、`cache_read_ratio`、`write_todos_only_turns`、`verify_blocks`、每個工具的輸出字元數、最高 context。
- A2 定位指標：`gold_src_recall`（只算 gold patch 中非測試、非文件的原始碼檔）與 `first_gold_src_touch_step`；保留舊欄位以便和歷史比較。
- A3 `dbench analyze <run>`：把本輪的一次性分析（tool 分布、重複呼叫、每步 token、cache 缺口、使用者訊息邊界的 token 跳增）做成 Go 指令，資料來源同時支援 `trace.jsonl` 與 Postgres trace。
- A4 Token 對帳：每次模型呼叫記錄本地估算的送出 token 數，與 provider 回報值比較；調查 §4.5 的 +25k 跳增（檢查 OpenRouter 回應的 provider 欄位與 reasoning 內容）。
- A5 Regression 子集 `code/bench/subsets/regress-v1.txt`（見 §9.1）。
- A6 Run 中繼資料：記錄 dcode commit、dirty diff 雜湊、prompt 雜湊、工具清單雜湊。
**Success Criteria**：對歷史 run 重算的指標與本文件 §4 一致（誤差來自定義修正的部分要說明）；`make check` 通過。
**Tests**：table-driven 測 metrics（run_tests 計入、重複呼叫、gold_src 過濾）；analyze 對固定 trace fixture 的輸出 golden test。
**Benchmark**：不需要新 run。
**Rollback**：只新增欄位與指令。
**Status**：Not Started

### Stage B：Context 與工具輸出（P1，成本）

**Goal**：每題 input tokens 減少 ≥ 40%，regression 子集解題數不下降。
**變更**：
- B1 Shell 輸出保留頭尾（各 50KB），完整輸出寫 artifact；`execute` 回傳附 artifact 路徑。
- B2 Context Builder 的 observation masking（§7.3 規則），預設在 headless 開啟，`Options.NoObservationMasking` 可關；artifact 存 Postgres。
- B3 `liveContext` 移到動態尾端，system message 整個 run 內固定。
- B4 `git_diff` 預設 stat + 每檔前 40 行，`path` 參數取完整 diff；`glob` 排除依賴目錄。
- B5 Task Ledger 的最小版本（FilesRead、FilesChanged、Budget），渲染在動態尾端。
**Success Criteria**：regress-v1 × 2 次，解題數 ≥ baseline；平均 input tokens −40% 以上；重複 tool call 不因遮蔽而上升超過 50%；互動模式 cache 命中率提升（用 GUI 手動 session 量測）。
**Tests**：Context Builder table-driven（哪些被遮蔽、最近 K 個保留、error 保留、遮蔽後 tool_call / tool message 配對不壞）；shell 頭尾保留在 rune 邊界；artifact 可讀回；system message 在多次呼叫間位元組相同。
**Rollback**：每個子項都有開關。
**Status**：Not Started

### Stage C：Regression 判定與完成閘門（P0，正確性）

**Goal**：消除「把 regression 當環境問題」這類失敗。
**變更**：
- C1 Verification Service：失敗測試的 HEAD 比對（dbench 用 stash，本機用 worktree），結果附在 `run_tests` / `execute` 輸出。
- C2 Completion Gate 取代 VerifyGate：加入 regression 與 Diff Inspector 規則；互動模式只顯示不擋。
- C3 服務提示改成不留檔的指令；Diff Inspector 攔下 `dump.rdb` 等副產品。
- C4 Requirements 解析（SWE-bench Pro 的 `## Requirements` 清單），放進 Ledger 與完成檢查訊息。
**Success Criteria**：bd80 與 b398 在 regress-v1 × 2 次中至少各解出 1 次；其他題不退步；被 Diff Inspector 攔下的 patch 數記錄在報表；閘門造成的額外步數中位數 ≤ 10。
**Tests**：用暫存 git repo 建立「改動前通過、改動後失敗」「改動前就失敗」「flaky」三種測試，驗證標記；stash 失敗時不得遺失工作樹改動（先 commit 到暫存 ref 再操作）；Diff Inspector 規則 table-driven；閘門擋下次數上限。
**Rollback**：`Options.NoVerifyGate` 沿用；新規則各有開關。
**Status**：Not Started

### Stage D：Progress Monitor 與預算（P1，可靠性 / 成本長尾）

**Goal**：消除逾時與盲目重試。
**變更**：
- D1 相同 tool call + 相同錯誤的附註與拒絕。
- D2 失敗指紋追蹤與換策略提示。
- D3 預算尾端提示（步數 / 時間 / 成本）；subagent 呼叫計入父 run 預算。
- D4 委派 prompt 拿掉「proactively」，explorer 保留；做一次「有 / 無委派段落」的 A/B。
**Success Criteria**：regress-v1 中 0 次逾時；平均成本下降；重複 tool call 數下降 ≥ 50%（相對 Stage B 後）。
**Tests**：FakeModel 重送同一個失敗 patch 3 次 → 第 3 次被拒；指紋正規化（行號、時間、路徑不同視為同一失敗）；預算提示在 80% 出現一次。
**Rollback**：各規則有開關。
**Status**：Not Started

### Stage E：搜尋工具（P1，token 與定位效率）

**Goal**：grep / glob 的輸出量下降，首次正確編輯前的工具呼叫數不上升。
**變更**：
- E1 `grep` 改用 ripgrep（找不到 rg 時退回現有實作）：支援 regex、`.gitignore`、`-C` context 行、依檔案分組；結果超過上限時回「共 N 筆，請縮小範圍」與前幾筆，不回全部（SWE-agent 模式）。
- E2 `glob` 套用相同的忽略規則（`.gitignore` + 依賴目錄，可被明確路徑覆寫）。
- E3 實驗：`outline(path)` 工具，回傳檔案的宣告清單（Go 用現有 `goparse`，JS / Python 用 tree-sitter 或語言內建 parser）。只在 A/B 中開啟。
**Success Criteria**：regress-v1 中 grep + glob 輸出字元 −30%；`first_gold_src_touch_step` 不變差。E3 只有在加入一組 Go 題後仍顯示改善才預設開啟。
**Tests**：rg 參數組裝、gitignore 生效、明確搜尋 `node_modules` 時照常回傳、上限訊息；rg 不存在時退回。
**Rollback**：開關退回舊 grep。
**Status**：Not Started

### Stage F：日常使用的安全與穩定（P2）

**Goal**：本機使用的安全邊界與 crash 正確性，不影響 dbench。
**變更**：
- F1 子程序環境變數最小化（白名單 + 使用者設定）；trace 寫入前遮蔽常見 secret 格式。
- F2 本機 sandbox adapter：macOS `sandbox-exec`、Linux bwrap；分「檔案寫入範圍」與「網路」兩層；沒有 sandbox 能力時明確顯示，不靜默降級。
- F3 工具執行 ledger（開始 / 完成），重啟後未完成的非冪等工具標記為「結果未知」並告知模型，不自動重跑。
- F4 Durable run 與 GUI 重連：沿用 `recontruct_plan_gpt6_astra.md` Phase C 的設計，單獨排程。
**Success Criteria**：sandbox 內無法寫入工作目錄以外、無法連到未允許的網域（整合測試）；trace 不含測試用的假 secret；crash 注入測試不重放寫入。
**Status**：Not Started

### Stage G：實驗（P3，每項獨立 A/B，預設關閉）

repo map（Aider 式）、LSP 工具、語意搜尋、reviewer subagent、model routing。每項實驗都要先寫下：針對哪一類失敗、用哪個指標判斷、成本上限、什麼結果算失敗（失敗就刪掉）。
**Status**：Not Started

### 執行順序與理由

```text
A（量測）→ B（成本）→ C（正確性）→ D（長尾）→ E（搜尋）→ F（安全）→ G（實驗）
```

C 的優先級最高（P0），但排在 B 之後，原因是：C 的 baseline 比對會增加測試執行與 context；先做 B 才不會讓 C 的成本把效果蓋掉，而且 B 讓每次 benchmark 便宜很多，C 可以跑更多次重複。如果只能做一件事，做 C。

---

## 9. Benchmark 協定與 Regression Dataset

### 9.1 Regression 子集 `regress-v1`（7 題）

| 題目 | 為什麼選 | 主要觀察的指標 |
|---|---|---|
| NodeBB-b398 | P2P regression × 3、逾時 × 1、最高成本、64 檔 patch | 解出、成本、逾時、patch 檔案數 |
| NodeBB-bd80 | regression 被判成無關 × 3、dump.rdb 進 patch | 解出、閘門行為、副產品 |
| NodeBB-8168 | 穩定解出但成本變異 5.6 倍 | 成本、token |
| NodeBB-a5af | 穩定解出（對照組） | 不退步 |
| ansible-c616 | 59 次模型呼叫、edit_file 15 次（編輯反覆） | 步數、重複呼叫 |
| ansible-e9e6 | 15 次重複 tool call | 重複呼叫 |
| ansible-34db | 最小、最便宜（對照組） | 不退步 |

已知缺口：沒有 Go 題。Stage E3 或任何定位相關改動之前，從 smoke50 加 3 題 Go 題形成 `regress-v2`。

### 9.2 執行方式

- 每個 Phase 結束：regress-v1 × 2 次（約 14 個 attempt；依過去成本估計每次 $1–3，Stage B 後應更低）。
- 每兩個 Phase：smoke50 前 20 題 × 1 次作為廣度檢查（不跑全部 50 題）。
- 固定模型 `openrouter:openai/gpt-6-luna-pro`、固定 tasks commit、記錄 dcode commit。

### 9.3 判斷規則

- 樣本小，解題率的統計檢定幾乎不可能顯著。所以：
  - **正確性改動**（C）看「目標題是否翻轉」加「其他題沒有翻成失敗」。
  - **成本改動**（B、D、E）看配對的 token / 成本 / 步數差異；這些指標變異比解題率小。
  - 任何一題從穩定解出變成 2 次都失敗 → 停下來分析，不合併。
- 每個 Phase 的報告寫進 `IMPLEMENTATION_PLAN.md` 的 Results，格式沿用 Stage 12 / 16。

### 9.4 Failure taxonomy（報表欄位）

`bench_attempts.failure_category` 目前有 resolved / regression / incomplete_fix / agent_timeout / model_error / infra_error。新增「人工 / 規則判讀」欄位，與自動分類分開：

| 類別 | 判斷依據 |
|---|---|
| regression_unnoticed | P2P 失敗，且 trace 中沒有看到該測試失敗 |
| regression_dismissed | P2P 失敗，trace 中看到失敗但 agent 判定無關 |
| local_pass_grade_fail | 最後一次本地完整測試通過，評分失敗 |
| wrong_solution | F2P 失敗，patch 改在正確檔案 |
| localization | F2P 失敗，patch 沒有改到 gold 原始碼檔 |
| loop / no_progress | 重複呼叫或相同失敗指紋超過門檻 |
| budget | 逾時 / 成本上限 |
| env_setup | 測試始終無法執行 |
| tool_misuse | 工具參數錯誤或格式錯誤反覆出現 |

前四類可以由規則從 trace + verifier 結果自動判讀，其餘需要抽樣人工確認。

---

## 10. 與 `recontruct_plan_gpt6_astra.md` 的關係

兩份計畫在「保留簡單 loop、量測先行、不急著加 graph / 多 agent」上一致。差異與取捨：

| 議題 | Astra 計畫 | 本計畫 | 理由 |
|---|---|---|---|
| 優先順序依據 | 系統正確性風險（crash、並行寫入、GUI 狀態） | dbench trajectory 中實際觀察到的失敗 | 7/7 失敗都是 regression / 逾時，沒有一個是 crash 或並行寫入造成 |
| 未知 window 的摘要門檻 | 恢復 16k 保守門檻 | 維持 200k 假設 | Stage 12 實測 16k 造成每幾步摘要一次、343 次 tool call 沒有任何修改 |
| Durable run / GUI | Phase C，P0 / P1 | Stage F4，P2 | 對解題率沒有直接影響；設計直接沿用 Astra 版本 |
| Context | 「先比較不需模型的策略」 | Stage B 直接實作 observation masking | 本輪 baseline 已證明 context 成長是成本主因，且文獻支持 |
| Baseline 比對 / Diff 檢查 | 未提及 | Stage C（P0） | 直接對應 7/7 失敗 |

---

## 11. Phase 8：Architecture Review 檢查點

每完成兩個 Stage 回答一次，答案寫進 `IMPLEMENTATION_PLAN.md`：

1. 新增的元件是否真的被觸發？（例如 Progress Monitor 在 regress-v1 中觸發幾次、觸發後行為有沒有改變）
2. 有沒有元件只在測試裡有效、在真實 trajectory 中從未觸發？→ 刪掉或合併。
3. Completion Gate 的額外步數與成本是否超過它挽救的題目價值？
4. Context Builder 的遮蔽規則是否造成反覆重讀？重讀成本是否吃掉節省的 token？
5. Middleware 數量是否下降或持平？若上升，哪一個可以變成 loop 裡的普通函式呼叫？
6. 是否有任何改動只在 dbench 有效、在日常互動中造成更差的體驗（例如閘門在互動模式擋人）？

---

## 12. 來源

**官方 engineering / 文件**
- OpenAI，[Unrolling the Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/)；[Codex sandbox](https://github.com/openai/codex/blob/13c42a077c88a0d04ae7680a9891d2daf4558577/docs/sandbox.md)
- Anthropic，[Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)；[Writing effective tools for AI agents](https://www.anthropic.com/engineering/writing-tools-for-agents)；[How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)；[Building effective agents](https://www.anthropic.com/engineering/building-effective-agents)；[Context editing](https://docs.anthropic.com/en/docs/build-with-claude/context-editing)；[Compaction](https://docs.anthropic.com/en/build-with-claude/compaction)
- Claude Code，[Sandboxing](https://code.claude.com/docs/en/sandboxing)；[Hooks](https://code.claude.com/docs/en/hooks)；[Memory](https://code.claude.com/docs/en/memory)；[Checkpointing](https://code.claude.com/docs/en/checkpointing)；LSP 工具整理見 [Piebald-AI/claude-code-lsps](https://github.com/Piebald-AI/claude-code-lsps)
- Cursor，[Dynamic context discovery](https://cursor.com/blog/dynamic-context-discovery)；[Improving agent with semantic search](https://cursor.com/blog/semsearch)
- Cognition，[Multi-agents: what's actually working](https://cognition.com/blog/multi-agents-working)
- OpenHands，[Condenser 架構](https://docs.openhands.dev/sdk/arch/condenser)
- Aider，[Repository map](https://aider.chat/docs/repomap.html)；[repomap.py](https://github.com/Aider-AI/aider/blob/main/aider/repomap.py)；[Edit formats](https://aider.chat/docs/more/edit-formats.html)；[Unified diffs](https://aider.chat/docs/unified-diffs.html)
- mini-SWE-agent，[repo](https://github.com/SWE-agent/mini-swe-agent)；SWE-bench，[Verified / bash-only 說明](https://www.swebench.com/verified.html)

**論文**
- Yang et al.，[SWE-agent: Agent-Computer Interfaces Enable Automated Software Engineering](https://arxiv.org/abs/2405.15793)（NeurIPS 2024）
- Xia et al.，[Agentless: Demystifying LLM-based Software Engineering Agents](https://arxiv.org/abs/2407.01489)
- Chen et al.，[LocAgent: Graph-Guided LLM Agents for Code Localization](https://aclanthology.org/2025.acl-long.426/)（ACL 2025）
- Lindenbauer et al.，[The Complexity Trap: Simple Observation Masking Is as Efficient as LLM Summarization](https://arxiv.org/abs/2508.21433)
- Deng et al.，[SWE-Bench Pro](https://arxiv.org/abs/2509.16941)
- Huang et al.，[Large Language Models Cannot Self-Correct Reasoning Yet](https://arxiv.org/abs/2310.01798)（ICLR 2024）
- Kamoi et al.，[When Can LLMs Actually Correct Their Own Mistakes?](https://arxiv.org/abs/2406.01297)（TACL 2024）

**本 repo 資料**
- `~/.deepagents/bench/reports/`：`20260930-160724`、`20260930-160743`、`20261002-032626` 報表與 Stage 12 compare
- dbench Postgres（`127.0.0.1:5433/dbench`）：`bench_attempts`、`dcode_trace_records`、`dcode_trace_blobs`
- `IMPLEMENTATION_PLAN.md` Stage 12、15、16、17 的 Results
