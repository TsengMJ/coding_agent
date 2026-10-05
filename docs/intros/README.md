# Quill 專案介紹（面試用）

> 這一組文件把整個專案依「開發 Coding Agent 會遇到的問題」分類整理。每一章都照同一個順序寫：**遇到什麼問題 → 業界怎麼做 → 我怎麼做（附程式位置）→ 量測結果 → 面試講法**。

## 一句話

用 Go 從零寫一套可組裝的 agent SDK，並在上面做出一個可以日常使用的 coding agent **`quill`**（CLI / TUI / 瀏覽器 GUI / 常駐 server），再用自建的 **`quillbench`** 在 SWE-bench Pro V2 上量測，依量測結果迭代 agent 的架構。

## 規模

| 項目 | 數字 |
|---|---|
| Go 程式碼（不含測試） | 約 37,000 行 |
| Go 測試 | 870 個 `Test*` 函式，約 20,700 行 |
| GUI 前端（React + Vite，內嵌進 binary） | 約 5,400 行 TypeScript |
| 開發階段 | Stage 1–23（功能）＋ R0–R10（以 benchmark 證據為依據的重構） |
| Commit | 79 個，每個 commit 可單獨編譯、測試通過 |

## 三個層次

1. **核心 SDK**（根套件 `quill` 與 `agent`、`llm`、`tool`、`runtime`、`backend`、`middleware/*`）：任何人都能拿來組自己的 agent。
2. **quill 產品層**（`code/*`、`cmd/quill`）：在 SDK 上加 coding 專用的工具、驗證閘門、安全機制、server、GUI。
3. **quillbench 評測層**（`code/bench`、`cmd/quillbench`）：在斷網 Docker 容器裡跑 SWE-bench Pro，用官方 verifier 評分，結果存 Postgres，附統計檢定。

## 文件目錄

| 檔案 | 回答的問題 |
|---|---|
| [01_overview_and_architecture.md](01_overview_and_architecture.md) | 整個系統長什麼樣？分幾層？一個請求怎麼流過系統？ |
| [02_agent_loop.md](02_agent_loop.md) | Agent 主迴圈怎麼設計？為什麼不做 multi-agent / planner？ |
| [03_tool_concurrency_and_write_safety.md](03_tool_concurrency_and_write_safety.md) | 一回合多個工具呼叫要不要平行？怎麼避免互踩、過期寫入？ |
| [04_tool_interface_design.md](04_tool_interface_design.md) | 工具該回傳什麼給 LLM？輸出太大、錯誤訊息怎麼設計？ |
| [05_context_management.md](05_context_management.md) | 長任務 context 爆掉、成本暴增、prompt cache 失效怎麼辦？ |
| [06_verification_and_completion_gate.md](06_verification_and_completion_gate.md) | Agent 說「做完了」卻沒驗證、造成 regression、改測試配合實作，怎麼擋？ |
| [07_progress_monitoring_and_budget.md](07_progress_monitoring_and_budget.md) | 無限迴圈、盲目重試、時間用完還在調查，怎麼處理？ |
| [08_safety_and_permissions.md](08_safety_and_permissions.md) | Agent 在使用者電腦上跑指令，怎麼防破壞、防洩漏 key？ |
| [09_state_persistence_and_recovery.md](09_state_persistence_and_recovery.md) | Crash、斷線、取消、重啟後怎麼續跑？LLM API 不穩怎麼辦？ |
| [10_subagents_and_delegation.md](10_subagents_and_delegation.md) | 什麼時候該用 subagent？怎麼隔離 context、共用預算、背景執行？ |
| [11_planning_and_human_interaction.md](11_planning_and_human_interaction.md) | Todo、Plan 模式、人工核准、GUI / TUI 怎麼設計？ |
| [12_observability_and_evaluation.md](12_observability_and_evaluation.md) | 怎麼知道改動有沒有變好？Trace、benchmark、統計怎麼做？ |
| [13_llm_provider_layer.md](13_llm_provider_layer.md) | 多家模型、串流、重試、token 計算、成本怎麼抽象？ |
| [14_design_decisions_and_lessons.md](14_design_decisions_and_lessons.md) | 做過哪些取捨？踩過哪些坑？面試常見問題怎麼答？ |

## 建議的面試講法（3 分鐘版）

1. **動機**：想真正理解 coding agent 的內部，所以不用現成框架包一層，而是用 Go 從頭寫，從 agent loop、middleware、backend、checkpoint 一路做到產品。
2. **架構**：核心是一個 5 個節點的 model → tool 迴圈；所有能力（檔案工具、摘要、subagent、人工核准、驗證）都是 middleware，用 Go 的「可選介面」掛上去。
3. **轉折點**：做完功能後接上 SWE-bench Pro。分析 28 次失敗的 trace，發現**7 個失敗裡 F2P（要修的測試）全部通過，失敗原因都是 P2P regression（把原本會過的測試改壞）或逾時**。問題不在「不會修」，而在「不知道自己弄壞了東西」。
4. **對策**：寫一個確定性的完成閘門：改了檔案卻沒跑測試就擋回去；測試失敗時自動 `git stash` 回原始 HEAD 重跑同一個測試，區分「我造成的 regression」「原本就壞」「flaky」；結束前檢查 diff 的殘留檔與被改過的既有測試。
5. **方法論**：確定性的問題交給程式，LLM 只負責理解與推理；每個新元件都有開關、都量觸發次數，從不觸發的就刪掉。例如 observation masking 離線估計可省 52% token，實測卻讓步數 +60%，所以刪掉。
6. **結果**：歷史 6 次都沒解出的 NodeBB-b398 第一次解出；bd80 在歷史 baseline 是 1/4，重構後的兩輪評測都有解出；刪掉 masking 後三題步數都回到 baseline 以下（樣本小，不宣稱統計顯著，面試時要主動講這一點）。
