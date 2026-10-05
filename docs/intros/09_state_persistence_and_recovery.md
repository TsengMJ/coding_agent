# 09 狀態持久化、恢復與可靠性

## 問題

Coding agent 的 run 動輒數十分鐘，中間會遇到：

- **使用者取消**、server crash、`Ctrl-C` 重啟——工具可能執行到一半。
- **等待人工核准**時 server 重啟，核准要能接續。
- **GUI 重新整理頁面**，run 不應該因此中止。
- **LLM API 不穩**：429、5xx、連線中斷、HTTP 200 卻空回應、串流卡住不動。
- **同一個 thread 被兩個視窗同時操作**。
- **長對話讓 checkpoint 表無限成長**。

## 業界怎麼做

- **OpenHands**：事件可序列化並持久化；恢復時處理還沒配對 observation 的 action。
- **Roo Code**：workspace 快照與對話恢復分開，並列出外部修改的還原限制。
- 主流 server 模式：服務擁有 run 的生命週期，UI 只是訂閱者。

---

## 1. Checkpoint

實作：[agent/loop.go](../../agent/loop.go)（`stepCallback`、`save`）、[runtime/checkpoint.go](../../runtime/checkpoint.go)、[runtime/postgres.go](../../runtime/postgres.go)。

- **每個節點結束就存一次**（langgraphgo 的 `OnGraphStep` callback），存的是整個 state（訊息 + 各 middleware 的 state key）與步數。
- 中斷時另外存 `PendingInterrupts`（待核准的 tool call）與 `ResumeFrom`（要從哪個節點繼續）。
- 兩種實作：Memory、Postgres。quill 一律用 Postgres，所以**重啟 server 後 thread、待核准的 tool call 都還在**。
- **每個 thread 只保留最新 10 筆**（`DefaultCheckpointsPerThread`）：`version` 欄存序號，Postgres 用 SQL 取最新 / 裁剪；舊版（version=0）的資料仍讀得到。
- State 的合併由 schema 中的 reducer 決定（訊息依 ID 取代或附加、`files` 以 nil 表示刪除、`async_tasks` 依 task_id 合併），JSON round-trip 有測試。

## 2. 懸空的 tool call：取消或 crash 之後

### 問題

checkpoint 在節點**結束後**才存。工具執行到一半被取消或 crash，最後一筆 checkpoint 是「有 tool_calls、沒有結果」的 assistant 訊息。provider 會拒絕這種歷史（每個 tool call 都要有結果），之後每一輪都會失敗。

### 做法

[middleware/patchtoolcalls/patchtoolcalls.go](../../middleware/patchtoolcalls/patchtoolcalls.go) 在每次 run 開始（`BeforeAgent`）掃描歷史，替沒有結果的 tool call 補上一則 error tool message。

R3 的關鍵修改是**訊息內容**。原本寫「可能被取消」，模型容易照樣重跑。現在：

> Tool call X did not complete - no result was recorded. It was cancelled or interrupted, **possibly part-way through or after it already ran**, so files or processes it touches may already have changed. **Check the current state (for example with git_diff or read_file) before running it again.**

### 為什麼沒有做「工具執行 ledger」

原計畫是建 ExecutionLedger 記錄每個工具的 started / finished，crash 後標成「結果未知」。R3 先確認現況後發現：上面的懸空 call 機制**已經能在下一次 run 告訴模型「結果未知」**，ledger 只會多一份要和 checkpoint 同步的狀態。所以改成修訂訊息、不建 ledger。這是「先確認問題存在再加元件」的例子。

## 3. Durable run：GUI 斷線不中止（R3）

### 問題（先確認存在）

`POST /runs` 的 run 綁在 HTTP request 的 context 上，**重新整理頁面就取消 run**。另外 `loadThread` 會用 history 覆蓋正在串流的畫面。

### 做法

實作：[code/server/runs.go](../../code/server/runs.go)。

- **server 持有 run**：`liveRuns.start` 用獨立的 context 跑 run，事件存在 `liveRun.events`。
- `GET /threads/{id}/events`：**從頭重播**再持續跟隨；閒置時回 204。
- `POST /threads/{id}/cancel`：明確取消。
- `POST /runs?detach=1` 的 run 不隨斷線結束；不帶參數維持「斷線即取消」，TUI 與 remote client 行為不變（向後相容）。
- 通知機制：每次有新事件就 `close(changed)` 再換一個新 channel，所有跟隨者用 `select` 等待——一個廣播條件變數，不需要每個訂閱者一個 channel。

GUI 端配合：

- run 改用 detach；Stop 改呼叫 cancel，讓 run 正常收尾（取消後才存的訊息也看得到）。
- 正在串流的 thread 不再被 history 覆蓋；載入時若 server 上仍在跑就自動接回。
- 重播的訊息依 id（tool 訊息依 tool_call_id）去重，diff 也去重。

### 順手修掉的 history bug

- Postgres 版在 user turn 少於 limit 時退回「最後 limit 筆訊息」，第一頁從 turn 中間開始，和 memory 版不一致。
- 兩版都把 middleware 補的 user 訊息（摘要、驗證閘門、live context）當成 turn。
- 修法：只有沒有 `lc_source` metadata 的 user 訊息算 turn（`runtime.IsUserTurn`）；兩種 store 共用同一組測試。

## 4. 並行控制：thread 鎖與目錄綁定

實作：[code/server/service.go](../../code/server/service.go)。

- **同一個 thread 同時只能有一個 run**，第二個回 `ErrThreadBusy`（HTTP 409）。
- **thread 綁定建立它的目錄**：換目錄使用同一個 thread 會被拒絕（避免 agent 對錯的 repo 動手）；`cwd` 必須是存在的絕對路徑。
- **依 cwd 快取 agent**：同一個目錄的 run 共用組好的 agent（VersionGuard 的讀取紀錄也因此跨 run 保留）。
- 只把**本次新增的訊息**送給 client。

## 5. 優雅關閉

- `quill serve` 收到 `Ctrl-C` / SIGTERM：停止接新 run，進行中的 run 最多 30 秒收尾，之後取消；再按一次 `Ctrl-C` 立即結束。
- 被取消的 tool call 下一輪由 PatchToolCalls 補上結果，thread 可以直接接續。
- 寫入逾時：client 不讀串流時 run 會停，不會讓 goroutine 永遠卡住。
- shell 子程序以程序群組管理，取消時整組殺掉（見 04）。

## 6. LLM 呼叫的可靠性

實作：[llm/transport.go](../../llm/transport.go)。

| 情況 | 處理 |
|---|---|
| 429、5xx、連線錯誤 | 重試（預設 2 次，`retries.max_retries` 可調），指數退避 1s → 2s …，有 `Retry-After` 就照它，上限 30 秒 |
| HTTP 200 但沒有 choices（串流則是什麼都沒送） | 視為空回應，同樣重試 |
| 串流已經送出內容 | **不重試**（重試會讓使用者看到重複的文字，且 tool call 可能不一致） |
| 串流超過 5 分鐘沒有新資料 | `ErrStreamIdle`。用 `context.WithCancelCause` + 每次讀到資料就 reset 的 timer 實作，錯誤訊息能分辨是「閒置逾時」而不是一般取消 |
| 非串流請求 | header 等待上限 10 分鐘（推理模型可能沉默很久） |
| 400 | 不重試；超長錯誤轉成 `ErrContextOverflow` 交給摘要處理（見 05） |

- 重試前要能重放 request body（`GetBody`），否則明確報錯；重試前讀掉並關閉舊的 response body，讓連線可以重用。
- 所有模型共用一個 `http.Client`，連線重用。

## 7. 儲存分工與啟動檢查

- Postgres：一定要讀回的資料。Redis：可重新產生的快取，有 TTL；過期後讀取時提示重跑工具。
- 啟動時任何一個沒設定或連不上就**直接失敗**，不悄悄退回記憶體模式。
- 建表時加 `pg_advisory_xact_lock`：平行測試同時 `CREATE TABLE IF NOT EXISTS` 會撞 `pg_type` 唯一鍵而 flaky（Stage 16.0 修掉）。

## 8. 背景 subagent 的生命週期

背景 task 的 registry 只在 server 記憶體中：server 重啟、或 run 停下來等待核准時，背景 task 會中止，下一輪收到「cancelled」通知。run 結束前會等背景 task 完成（`AfterAgent`），取消 run 會一併取消（見 10）。這是刻意的取捨：不為背景 task 做持久化，但保證狀態不會不一致。

## 9. 測試

- checkpoint：memory / Postgres 只留最新 N 筆、刪除後序號重來、讀得到舊版資料、跨實例讀到 pending interrupt；
- panic：tool panic 變 error tool message、node panic 回 `ErrPanic`；取消的 tool call 下一輪補上結果；
- serve：關閉時讓進行中的 run 完成、超過寬限期取消、client 不讀時 run 會停；
- LLM：503 / 429 重試、超過上限放棄、400 不重試、`max_retries = 0`、header 等待逾時、串流閒置逾時；
- R0 修掉一個 data race：前一個測試的 `Serve` goroutine 收尾時讀全域 `shutdownGrace`，下一個測試同時在改它；改成在回報 ready 前讀一次。`go test -race -count=10` 驗證。

## 面試講法

> 「checkpoint 在每個節點結束時存進 Postgres，所以等待核准時重啟 server，核准還能接續。工具執行中被取消的話，最後一筆 checkpoint 會有沒結果的 tool call，provider 會拒絕這種歷史，所以每次 run 開始時補上一則錯誤結果，而且明確告訴模型『它可能已經部分執行了，先檢查現況再重跑』。我原本計畫做執行 ledger，但確認後發現這個機制已經提供了同樣的資訊，就沒有多加一份要同步的狀態。」
>
> 「GUI 重新整理會中止 run 這個問題，我讓 server 持有 run、緩存事件，client 斷線後重新訂閱會從頭重播再持續跟隨。」

可能的追問：

- **checkpoint 在節點後存，工具做到一半 crash 會重跑嗎？** 不會自動重跑；下一次 run 補上「結果未知」的訊息，由模型先檢查再決定。
- **事件緩存在記憶體會不會爆？** 只保留進行中的 run，run 結束就從 map 移除；完整歷史在 checkpoint 與 trace 裡。
- **為什麼串流開始後不重試？** 已送出的 token 無法收回，重試會產生重複或不一致的內容。
