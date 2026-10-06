# 14 設計決策、教訓與面試問答

## 1. 貫穿整個專案的原則

1. **確定性問題用程式解，LLM 只做理解與推理。**版本核對、排程、預算、重複偵測、baseline 比對、diff 檢查都是程式；LLM 負責理解需求、推理未知程式碼、提出修復假設。例：read-before-edit 原本只寫在 prompt，改成 VersionGuard；「Verify before declaring done」寫在 prompt 沒用，改成驗證閘門。
2. **先證據、後元件。**每個重構 Stage 都先在歷史 trace 裡確認問題存在，再決定做不做。R2–R9 有不少計畫項目在確認後被縮小或取消（見第 3 節）。
3. **每個元件都要可觀察。**有開關（`-disable`）、有觸發次數統計；**從不觸發的就刪掉**。
4. **保持迴圈簡單。**單一 model → tool 迴圈，新能力是 middleware 或節點間的路由。
5. **誠實標示限制。**沒有 OS sandbox 就寫出來；樣本小就不宣稱顯著；放行沒留紀錄就補紀錄。
6. **保護 prompt cache。**任何會變的內容放在請求尾端、不寫進 state。
7. **Fail fast。**設定錯誤、儲存連不上、middleware 名稱重複、排除了不存在的 middleware，都在啟動時報錯。

## 2. 明確不做的事與理由

| 不做 | 理由 | 重新考慮的條件 |
|---|---|---|
| Planner / Orchestrator / Reviewer subagent | 7/7 失敗與需求理解、規劃無關；閘門是確定性且針對已知失敗 | 閘門上線後仍有語意性 regression 漏網 |
| 任務分類的模型呼叫、model router | 沒有證據、沒有穩定的成本 / 品質量測 | regression 子集出現 ≥ 2 題誤解需求 |
| 強制 planning | `write_todos` 已佔 12.5% 模型回合 | 跨模組任務的失敗分析顯示計畫錯誤 |
| Repo map / LSP / 語意索引 | first gold file touch 在第 1–4 步，定位不是瓶頸 | Go / 大型 repo 題的定位指標變差 |
| 跨 session 自動記憶 | 容易存下過時的 repo 資訊 | — |
| OS sandbox adapter | 平台相依，無法在評測中驗證 | — |
| 引入 ripgrep | 問題（ignore、regex）不需要它；多一個依賴與兩種行為 | 搜尋速度成為瓶頸 |

## 3. 計畫了但確認後縮小或取消的項目

這一節展示「先確認問題再動手」的紀律，面試很好用：

| 原計畫 | 確認後發現 | 處理 |
|---|---|---|
| 工具執行 ledger（crash 恢復） | 懸空 tool call 機制已能告知「結果未知」 | 只改訊息內容，不加 ledger（R3） |
| 推理內容 passthrough | probe 顯示 provider 不回傳 | 不做（R4） |
| 依 import 找受影響的測試 | 28 個 attempt 中，壞掉的測試檔 agent 都跑過；缺的是「判斷是否自己造成」 | 改做 HEAD baseline（R5） |
| 「改動檔案數過多」檢查 | b398 改 49 個語系檔可能是需求本身 | 不做（R5） |
| TaskLedger（結構化任務狀態） | 需要的資訊都還在 context 裡 | 不做（R6） |
| 精簡 git_diff | 72 次中 55 次帶 paths（agent 要的就是完整內容），其餘只省 4% | 不做（R6） |
| subagent 步數 / 回傳上限 | 唯一用到 subagent 的 attempt 不會觸發 | 不加程式（R7） |
| 「3 次編輯後指紋不變」提示 | 重播 0 次觸發 | 實作後刪除（R7） |
| Environment Profile | service hint 已在第一次失敗給出確切指令；從 CI 推導反而會誤導 | 不做（R8） |
| Shell 輸出串流到 Postgres artifact | eviction 已處理 >20k tokens；execute 只佔輸出 ≤6% | 只改成頭尾保留（R2） |
| Observation masking | 實測步數 +60% | 實作後刪除（R6 / 最終評測） |

## 4. 踩過的坑（附教訓）

| 坑 | 教訓 |
|---|---|
| 未知模型摘要門檻 16k → 每幾步摘要一次，343 次 tool call 零修改 | context 管理過度積極比不做更糟；所有相關常數要從同一個來源推導 |
| Observation masking 離線估計 −52% token，實測步數 +60% | 離線評估要考慮 agent 的行為回饋 |
| 只靠 prompt「Verify before declaring done」，agent 仍沒驗證就結束 | 確定性規則要用程式強制 |
| 委派次數是 0 | 能力接好了不代表會被用；prompt 要說清楚何時用 |
| `redis-server` 在工作目錄留下 `dump.rdb` 被收進 patch | agent 的副作用要檢查；提示指令本身也要考慮副作用 |
| `docker cp` 讓 git index 失效，`git status` 6–8 秒 | 環境差異會變成效能問題；量測每個工具的時間 |
| 官方 `test.sh` 靜默遺失 `output.json` | 評分器要先用 oracle / nop 驗收 |
| `TestRuns` 漏算 `run_tests` | 改 agent 之前先修好量測 |
| 平行測試同時建表撞 `pg_type` 唯一鍵 | DDL 加 advisory lock |
| server 測試改全域變數造成 data race | 全域可調參數在 goroutine 啟動前讀一次；`-race -count=10` |
| 摘要模型回 empty response，整個 attempt 中止 | 輔助模型失敗不應該讓主流程失敗；先 offload 再摘要 |
| HEAD baseline 放行時不留紀錄 | 每個「放行」也要可觀察，才能評估元件 |

## 5. 常見面試問題與回答要點

**Q1. 這個專案最難的部分是什麼？**
驗證閘門的 HEAD baseline：要在 agent 的工作樹上安全地 stash、在原始碼重跑測試、再完整還原（含未追蹤檔），任何一步失敗都不能遺失修改，還要區分 regression、既有失敗與 flaky。見 06。

**Q2. 你怎麼決定要做什麼功能？**
看 benchmark trace 的失敗分類與過程指標。例：7 個失敗全是 P2P regression 或逾時 → 驗證閘門；glob 結果 69% 是被忽略的檔案 → gitignore；重複呼叫集中在「看到失敗卻原地重跑」→ ProgressMonitor。見 12。

**Q3. 有沒有做錯的決定？**
Observation masking。離線重播估計省一半 token，實測 agent 重讀被遮蔽的檔案，步數 +60%。刪掉，並記錄原因與「若要重做該怎麼做」。見 05。

**Q4. 工具平行執行怎麼避免衝突？**
工具宣告 Read / Write（路徑）/ Exec 副作用，Exec 是零值；每個呼叫只等前面衝突的呼叫；結果依呼叫順序寫回。再加上內容 hash 的版本守衛防過期寫入。見 03。

**Q5. Context 爆了怎麼辦？**
分層：工具輸出有界 → 單筆 offload → 舊參數截斷 → 80% 時摘要（不改寫歷史、切點不拆 tool call 配對、先 offload）→ provider 回超長時強制摘要重試。另外把會變的內容放尾端保護 cache。見 05。

**Q6. 怎麼防止 agent 做危險的事？**
路徑沙箱、人工核准（含 diff 預覽）、allow-list 的嚴格解析、破壞性指令一律要核准（headless 直接拒絕）、shell 看不到 secret、trace 遮蔽、SSRF 防護、GUI token。並明確說明沒有 OS sandbox。見 08。

**Q7. Crash 或取消後怎麼恢復？**
每個節點後存 checkpoint；下次 run 開始時替沒結果的 tool call 補上「可能已部分執行，先檢查再重跑」；server 持有 run，GUI 斷線可重播事件。見 09。

**Q8. 為什麼不用 multi-agent？**
約 15 倍 token、平行寫檔互相矛盾；唯讀的研究型 subagent 有用，所以做了 explorer 與 web-researcher，並支援背景執行。見 10。

**Q9. 怎麼證明改動有效？樣本這麼小。**
解題率不宣稱顯著（10 題 8:8、p=1.0 我有記錄）。可信的證據是機制層面（trace 中閘門擋下 → agent 修好；FakeModel 重現測試）與大幅度的過程指標變化。比較用同題配對的 McNemar 與 bootstrap。見 12。

**Q10. 為什麼用 Go？**
並行（工具、subagent、背景任務、串流）是一等公民；`context.Context` 讓取消一路傳到 shell 程序群組；單一 binary 內嵌 GUI，`go install` 就能用。

**Q11. 和 Claude Code / Codex / Cursor 比，你的設計有什麼對應？**
Codex 的前綴延伸 cache 與依並行能力排程 → R4、R2；Claude Code 的 read-before-edit 與 Stop hook → VersionGuard、驗證閘門；Claude Code 的 WebFetch(url, prompt) → fetch_url；Cursor 的長輸出寫檔 → eviction；Codex 的 apply_patch 格式 → apply_patch；Codex 的 read-only / workspace-write / full-access 與 Claude Code 的 Seatbelt / bubblewrap + domain allowlist proxy + `autoAllowBashIfSandboxed` → OS 層 shell sandbox（見 08 §12）。HEAD baseline 的概念接近 Agentless「只用原始碼上會通過的 regression test 篩 patch」，差別是我把它做成 agent 執行中的即時回饋：agent 還在場，可以根據「這是你造成的」繼續修。

**Q12. 如果再多給你時間，下一步做什麼？**
（1）sandbox 的下一步：Linux Landlock + seccomp fallback、Windows 支援，以及量測 sandbox 對評測解題率的影響；（2）更大的評測樣本（smoke50 × 多次）讓解題率的比較有統計意義；（3）環境一致性：bd80 的 regression 在 agent 環境裡看不到，屬於任何閘門都擋不了的類型；（4）若要重做 context 壓縮，只遮蔽 grep / glob / ls，或不遮蔽之後還會編輯的檔案。

**Q13. 測試策略？**
870 個測試函式。FakeModel 讓 agent 行為可以 deterministic 測試；httptest 假 provider；暫存 git repo 測 baseline；Postgres / Redis 整合測試（沒設定時 skip）；`-race`；前端 vitest。benchmark 失敗轉成 FakeModel 回歸測試。

**Q14. 每個 commit 的紀律？**
每個 commit 可單獨編譯、`make check`（gofmt、vet、test）通過；R0 把一大包未提交的改動依層拆成 4 個 commit，各自在只含該層改動的工作樹上驗證過。

## 6. 數字速查

| 項目 | 數字 |
|---|---|
| Baseline 失敗組成 | 7 個失敗：F2P 全過；6 個 P2P regression、1 個逾時 |
| 工具輸出佔比 | read_file 36–56%、grep 16–32%、git_diff 14–19%、execute ≤6% |
| glob 結果中被忽略的檔案 | 69%（2,526 / 3,663 行） |
| grep regex 寫法 | 413 次中 11 次；最終評測自動重試觸發 13 次 |
| tool wall / sum | 0.7–0.9 |
| 首次碰到正確檔案 | 第 1–4 步 |
| Masking 離線估計 vs 實測 | −52% token vs 步數 +60%、token −15% |
| 最終評測 | 15/22（68%）→ 10/14（71%），$0.170 → $0.157 / attempt |
| R10 | b398 首次解出、bd80 18 步 $0.02、步數全部回到 baseline 以下 |
| 破壞性指令規則對評測的影響 | 133 個歷史指令 0 命中 |
| git 工具效能 | `git status` 6–8 秒 → 0.9 秒 |
| 根目錄 grep | 11–15 秒（會逾時回 0 筆）→ 0.6–2 秒 |
| quillbench gate | oracle 50/50、nop 0/50 |
