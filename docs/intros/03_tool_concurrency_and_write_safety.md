# 03 工具並行與寫入安全

## 問題

模型常在一個回覆裡提出多個 tool call（讀 5 個檔、同時 grep）。全部循序執行很慢；全部平行又會出事：

1. 同一回合「改檔」和「跑測試」平行，測試可能跑在修改之前。
2. 兩個寫入改同一個檔，後寫的蓋掉先寫的。
3. `execute` 可能改任何檔案，和寫入同時跑結果不可預期。
4. **過期寫入**：agent 讀了檔案，之後使用者在 Cursor 裡改了、或某個指令（formatter、`git checkout`）改了，agent 再依舊的內容編輯，把別人的修改蓋掉。
5. 多檔 patch 寫到一半失敗，工作樹停在半套狀態。

## 業界怎麼做

- **Codex**（`parallel.rs`）：工具依「是否支援並行」選擇共享或排他執行。
- **Claude Code**：Edit 前必須先 Read；讀取後檔案被改過就拒絕編輯。
- **Codex `apply_patch`**：Codex 自己的多檔 patch 格式。

## 演進

| 階段 | 做法 | 問題 |
|---|---|---|
| Stage 7a | 同一回合最多 8 個 goroutine 平行，對同一個 state 快照執行，結果依呼叫順序寫回；同路徑的第二個寫入直接拒絕 | `execute` 可以和寫入同時跑；改檔再跑測試不保證順序 |
| R2 | 工具宣告副作用（`tool.Effect`），依衝突關係建立依賴；加上 VersionGuard | — |

## 1. 依副作用排程（R2）

定義在 [tool/tool.go](../../tool/tool.go)：

```go
const (
	EffectExec EffectKind = iota // 可能做任何事（跑指令、改任何檔），獨佔執行；是零值
	EffectRead                   // 只讀
	EffectWrite                  // 改 Paths 裡的檔案
)

func (e Effect) Conflicts(o Effect) bool {
	switch {
	case e.Kind == EffectExec || o.Kind == EffectExec:
		return true
	case e.Kind == EffectRead && o.Kind == EffectRead:
		return false
	}
	return pathsOverlap(e.Paths, o.Paths) // 路徑相同或互為前綴；nil 代表整個 workspace
}
```

設計重點：

- **安全的預設值**：`EffectExec` 是 iota 的零值，**沒宣告的工具一律獨佔執行**。新增工具忘了宣告只會變慢，不會出錯。
- **依賴而不是分批**：[agent/loop.go](../../agent/loop.go) 的 `nodeTools` 裡，第 i 個呼叫只等「前面和它衝突的呼叫」完成，不衝突的照樣平行。所以：
  - `read_file ×5` → 全部平行；
  - `edit_file a.go` + `read_file b.go` → 平行；
  - `edit_file a.go` + `run_tests` → `run_tests` 是 Exec，等編輯完成才跑，**一定看得到修改**；
  - 兩個 `execute` → 循序（這是 R2 的行為改變，TUI 測試同步改成顯示 sequential）。
- **結果依呼叫順序套用**：不論誰先跑完，tool message 與 state update 都照 call 順序寫回，transcript 是 deterministic 的。
- 誰宣告了什麼：檔案工具、git、diagnostics、web、todo 有宣告；`task` 對唯讀 subagent（explorer、web-researcher）與背景任務宣告 Read，其他 subagent 是 Exec。
- `apply_patch` 參數無法解析時宣告「寫整個 workspace」：它反正會被拒絕，但排程上保守處理。

**量測**：R1 的歷史 trace 顯示 `tool wall time / tool 時間總和 ≈ 0.7–0.9`，代表原本的平行已有效果。R2 的驗收條件之一就是「排程不能讓這個比例變差」。

## 2. VersionGuard：先讀再改、拒絕過期寫入

實作：[backend/versionguard.go](../../backend/versionguard.go)。

```go
func (g *VersionGuard) check(ctx context.Context, p string) string {
	sum, exists := g.current(ctx, p)   // 目前磁碟內容的 sha256
	if !exists {
		return ""                      // 新檔案不用先讀
	}
	seen, ok := g.seen[path.Clean(p)]  // 上次經過 guard 讀或寫時的 sha256
	switch {
	case !ok:
		return "Error: ... has not been read yet. Read it with read_file before changing it."
	case seen != sum:
		return "Error: ... changed since you last read it (by a command, the user or another editor). Read it again before changing it."
	}
	return ""
}
```

- `Read` 成功 → 記下內容 hash；`Write` / `Edit` 前比對 hash，成功後更新 hash；`Delete` 後移除紀錄。
- **用內容 hash 而不是 mtime**：`docker cp`、`git checkout` 會改 mtime 卻不改內容，hash 不會誤判。
- **包在 quill backend 的最外層**：一般編輯、`apply_patch`、subagent 都經過同一個 guard，沒有繞過的路徑。
- **核心 SDK 預設不套**：SDK 預設的 StateBackend 檔案存在 agent state 裡，不會有外部修改。
- 錯誤訊息直接告訴模型下一步（重讀），而不只是「失敗」。
- **為什麼不只寫在 prompt 裡**：原本 read-before-edit 只存在 `middleware/filesystem/prompt.go` 的文字中，程式沒有強制。這類確定性規則交給程式。
- 最終評測中觸發 1 次。

## 3. `apply_patch`：全有或全無

實作：[code/codetools/applypatch.go](../../code/codetools/applypatch.go)、[code/codetools/patch.go](../../code/codetools/patch.go)。

1. **先規劃、後寫入**：`PlanPatch` 讀所有相關檔案、套用所有 hunk、算出每個檔案的最終內容；任何一個 hunk 對不上就整個失敗，**一個檔案都不寫**。
2. **寫入時記 undo**：`writePlanned` 依序寫入 / 刪除 / 搬移；中途失敗就回滾。
3. **回滾也要防過期寫入**（R2）：只還原「內容仍等於本次 patch 寫入值」的檔案；如果寫入後已被別人改過，就保留並在錯誤中列出，請 agent 先檢查。
4. HITL 核准畫面顯示 patch 對磁碟現有內容的 unified diff。

## 4. 為什麼保留「同回合同路徑寫入就拒絕」

有了依賴排程，同路徑的兩個寫入已經會循序執行，看似不需要再拒絕。但在 StateBackend 下，兩個寫入都基於**同一個 state 快照**，循序執行仍會讓第二個覆蓋第一個的更新（lost update）。所以保留這個檢查，並在錯誤中請模型分兩回合做。

## 5. 測試

- barrier 工具證明唯讀工具真的同時執行；
- 同檔並行寫入只有一個成功；
- 編輯後的測試看得到編輯；
- 模擬外部插入修改後，編輯被拒絕；
- patch 中途失敗回滾、回滾遇到外部修改時保留；
- 全部跑 `go test -race`。

## 面試講法

> 「工具平行我用的是『宣告副作用 + 衝突就等待』。每個工具宣告自己是 Read、Write（哪些路徑）還是 Exec；Exec 是零值，所以沒宣告的工具自動獨佔執行，忘了宣告只會慢不會錯。每個呼叫只等前面和它衝突的呼叫，所以讀檔全部平行，但『改檔再跑測試』一定照順序。另外用內容 hash 做版本守衛：沒讀過或讀了之後被外部改過的檔案，一律拒絕編輯，這是從『使用者和 agent 同時在同一個目錄工作』這個真實情境來的。」

可能的追問：

- **為什麼用 hash 不用 mtime？** 容器複製與 git 操作會改 mtime 不改內容；hash 只在內容真的變了才擋。
- **guard 的狀態存在記憶體，重啟後呢？** 重啟後 `seen` 是空的，所有既有檔案都要重讀一次才能改，偏保守但正確。
- **8 個並行上限怎麼來的？** 是經驗值（`maxParallelTools`），用來避免一次開太多子程序與 HTTP 連線；R1 量測顯示現有平行度已經足夠，沒有調整的證據。
