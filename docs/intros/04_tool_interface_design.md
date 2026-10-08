# 04 工具介面設計（Agent-Computer Interface）

## 問題

模型只能透過工具看見世界。工具回傳的東西直接決定 agent 的表現與成本：

- 測試輸出幾千行，真正有用的只有「哪幾個失敗、在哪一行」。
- 搜尋結果混進 `node_modules`、build 產物，淹沒真正的程式碼。
- 錯誤訊息只寫「失敗」，模型不知道下一步怎麼做，只好重試。
- 指令輸出被截斷，偏偏把尾端的測試摘要截掉。
- 網頁整頁 20k tokens 直接塞進主 context。

## 業界怎麼做

- **SWE-agent 論文**：搜尋結果超過 50 筆就要求縮小查詢；summarized search 18.0% vs 逐筆翻頁 12.0%；編輯時跑 linter 擋下語法錯誤（18.0% vs 15.0%）。
- **Anthropic《Writing tools for agents》**：只回傳有助決策的欄位；截斷與錯誤訊息要說明下一步怎麼做。
- **Cursor《Dynamic context discovery》**：長輸出寫成檔案，agent 按需讀回。
- **Claude Code `WebFetch(url, prompt)`**：由小模型讀整頁，只把答案回給主 agent。

## 我的設計原則

1. **結構化**：把指令輸出壓成精簡 JSON，而不是丟原始文字。
2. **有界**：每個工具的輸出有上限，超過就保留頭尾並說明怎麼看其他部分。
3. **可操作的錯誤**：錯誤訊息告訴模型下一步。
4. **用證據決定**：每個改動都先在歷史 trace 裡找到問題才做。

---

## 1. 檔案工具（`middleware/filesystem`）

`ls`、`read_file`、`write_file`、`edit_file`、`delete`、`glob`、`grep`、`execute`。

- **虛擬根目錄**：quill 把專案目錄當成 `/`，檔案工具不能碰根目錄以外的路徑。[backend/filesystem.go](../../apps/agent/backend/filesystem.go) 先 `EvalSymlinks` 解析真實路徑再檢查前綴，所以 `..` 和 symlink 都逃不出去；刪除 symlink 只刪 link 本身。
- **`read_file`**：帶行號、可分頁（offset / limit，預設 2000 行），大檔讀取有上限。
- **`edit_file`**：`old_string` 出現多次時，錯誤訊息列出前 5 處的**行號與前一行**，模型可以直接補上下文讓它唯一。
- **工具依 backend 能力出現**：backend 不能執行指令就沒有 `execute`；不支援刪除就沒有 `delete`。

### grep / glob 的三個改進（R8，都來自 trace 證據）

| 證據（28 個歷史 attempt） | 改動 |
|---|---|
| glob 59 次中 12 次含被忽略的路徑，佔 2,526 / 3,663 行（69%）：NodeBB 的 `build/` 1,897 行、`node_modules` 629 行 | 在 git work tree 裡，候選檔案由 `git ls-files --cached --others --exclude-standard` 列出，**忽略規則完全照 git**；glob 也跳過依賴目錄 |
| grep 413 次中 11 次是 regex 寫法（`export.*posts`），字面比對找不到，多花一步改寫 | **先字面比對**；沒結果且 pattern 看起來像 regex 時，用 RE2 再搜一次，有結果才採用並附註 |
| Stage 15：從根目錄 grep NodeBB 要 11–15 秒，第一次撞到 15 秒上限回傳 0 筆 | 跳過 `node_modules`、`.venv`、`__pycache__`（明確搜尋這些目錄時照常搜）→ 0.6–2 秒 |

設計細節：

- **明確搜尋被忽略的目錄照常回傳**（`path=/build` 或 pattern 寫出 `node_modules`）：使用者意圖優先。
- **regex 重試只在字面沒結果時發生**：字面有結果時行為完全不變，所以原本「字面比對」的工具說明不用改，也不會讓 `.` 之類的字元突然變成萬用字元。
- **沒有引入 ripgrep**：觀察到的兩個問題都不需要它，速度也沒問題；多一個外部依賴還要處理「有 rg / 沒 rg」兩種行為不一致。
- 函式庫預設關閉 gitignore，quill 與 quillbench 開啟。

## 2. 結構化 coding 工具（`code/codetools`）

| 工具 | 輸出 | 細節 |
|---|---|---|
| `run_tests` | `{status: passed/failed/error/no_tests, passed, failed, failures: [{file, line, test, message}], build_errors}` | Go：`go test -json`；JS/TS：依 `package.json` 選 mocha / jest / vitest 的 JSON reporter，都沒有退回 `npm test`；Python：pytest junit XML（大小可控），沒裝 pytest 改跑 `unittest -v` 並解析 |
| `diagnostics` | `[{file, line, severity, message}]` | Go：`go build` + `go vet`；JS/TS：`tsc --noEmit`、eslint，都沒有時 `node --check`；Python：ruff，否則在記憶體內做語法檢查（不寫 `__pycache__`） |
| `git_status` / `git_diff` | 檔案清單、增刪行數、按檔切開的 diff | porcelain v2、numstat、untracked、沒有 HEAD 的 repo、輸出上限；唯讀不需核准 |
| `apply_patch` | 成功 / 失敗原因 | Codex 格式多檔 patch，全有或全無（見 03） |

- 測試報告由內嵌的 node / python 腳本先壓成摘要，再交給 Go 解析；平均輸出 **0.1–0.5k 字元**。
- **monorepo 支援**：沒給 `dir` 時，從 `files` 往上找語言標記檔（`go.mod`、`package.json`、`pyproject.toml`），再往下找 3 層；唯一一個就在那裡跑，多個就回 `unsupported` 並列出候選。回報路徑一律相對於 workspace 根目錄。
- 指令一律經 `SandboxBackend.Execute`，所以在 quillbench 的 Docker 容器裡一樣能用。
- **「沒有跑任何測試」也是失敗**：`no_tests`、`0 passing`、`collected 0 items` 都會被驗證閘門視為沒有驗證（見 06）。

**效果**（Stage 12，smoke50 前 10 題 A/B）：加入多語言 run_tests / diagnostics 後，每題都用 run_tests 2–8 次與 diagnostics；之前幾乎不用（Node 題會回 unsupported）。NodeBB 題因此從 F2P 2/4 變成 resolved：eslint 抓到重複宣告，mocha 回報 `ECONNREFUSED 6379` 後 agent 自己啟動 Redis。

### git 工具的效能問題（Stage 15）

quillbench 用 `docker cp` 複製 repo，每個檔案的 stat 都變了；git 工具為了不改 index 帶了 `GIT_OPTIONAL_LOCKS=0`，於是每次 `git status` 都重新 hash 整個工作樹：6–8 秒，`git diff` 9 秒。解法是容器啟動後先跑一次 `git update-index -q --refresh`（0.3 秒），之後分別是 0.9 秒、0.4 秒。

## 3. `apply_patch` 的容錯比對

模型寫的 patch 常有空白或標點的小差異。[code/codetools/patch.go](../../apps/agent/code/codetools/patch.go) 的 `seekSequence` 逐層放寬比對：

1. 完全相同；
2. 忽略行尾空白；
3. 忽略前後空白；
4. Unicode 標點正規化（全形引號、破折號等）。

回傳**最嚴格那一層**的第一個匹配。全部失敗時，`closestHint` 用識別字的 Jaccard 相似度找出檔案中最像的行，告訴模型去那附近重讀。另外接受包在 shell heredoc 裡的 patch。

## 4. Shell 輸出

實作：[backend/local_shell.go](../../apps/agent/backend/local_shell.go)。

- **程序群組**：`Setpgid`，逾時或取消時 `kill(-pgid)`，背景子程序一起殺掉，不會留下殭屍 server。
- **保留頭尾**（R2）：原本只保留前 100KB，**測試摘要通常在尾端**，正好被丟掉。改成頭尾各保留一半，中間標 `[... output omitted ...]`，並說明省略了幾個位元組、建議用 grep / head / tail 重跑。截斷在 rune 邊界。
- 最後附上 `[Command succeeded/failed with exit code N]`，驗證閘門與進度監控都靠這行判斷結果。
- 敏感環境變數不傳給子程序（見 08）。

## 5. 大型輸出 offload（eviction）

實作：[middleware/filesystem/eviction.go](../../apps/agent/middleware/filesystem/eviction.go)。

- tool 結果超過 **20k tokens**：完整內容寫到 `/.quill/large_tool_results/<id>`（quill 路由到 Redis，預設保留 7 天），回給模型的是路徑 + **頭 5 行、尾 5 行**的帶行號預覽 + 如何用 `read_file` 分頁讀回。
- 使用者訊息超過 50k tokens 同樣處理，存到 `conversation_history`（Postgres），只 evict 一次，state 本身不變。
- **自己已經有上限的工具不 evict**（`ls`、`glob`、`grep`、`read_file`、`edit_file`、`write_file`、`delete`）。
- 預覽只在真的有省略時才說明省略規則，不加多餘的警語。
- 寫入失敗時退回截斷，不讓 run 失敗。

## 6. Web 工具（Stage 14）

| 工具 | 設計 |
|---|---|
| `fetch_url(url, prompt)` | 用 `models.web`（未設定用主模型）讀整頁，**只把答案回給主 agent**；完整頁面存成 `/.quill/web/<hash>.md` 供需要時 `read_file` / `grep`；網頁內容以 `<web_content>` 框住以隔離 prompt injection；擷取失敗退回原文 |
| `web_search` | Brave，預設 8 筆（原本 20 筆加 rich JSON），可限定 / 排除網域 |
| `resolve_library_id` / `query_docs` | Context7，查指定版本的函式庫文件 |

- 搜尋與擷取快取 15 分鐘，Context7 快取 1 小時。
- 單頁擷取是「一次 LLM 呼叫、不帶工具」，放在工具內部而不是做成 subagent：可平行、可用 `url + prompt` 快取、單頁失敗不影響其他頁。多步研究才交給 `web-researcher` subagent。
- SSRF 防護見 08。

## 7. 讓工具結果「說下一步」的例子

| 情境 | 回給模型的訊息 |
|---|---|
| 測試連不上 Redis | 附 `[hint] Connection refused on port 6379 (Redis)... redis-server --daemonize yes --save '' --dir /tmp`（`--save ''` 是因為曾經在 patch 裡留下 `dump.rdb`） |
| 檔案讀後被改 | 「changed since you last read it ... Read it again before changing it.」 |
| 被取消的工具 | 「可能已部分或全部執行，先用 git_diff / read_file 確認現況再重跑」 |
| 重複失敗 | 「this exact call failed twice with the same result and nothing changed since...」 |
| 破壞性指令被拒 | 「找一個只碰到需要檔案的較小指令，或把確切指令告訴使用者讓他自己跑」 |

## 面試講法

> 「我把工具當成給 LLM 用的 API 來設計。測試工具不回原始輸出，而是回 JSON：狀態、通過與失敗數、每個失敗的檔案與行號，平均只有幾百字元。搜尋工具照 git 的忽略規則，因為我在 trace 裡量到 glob 結果有 69% 是 build 產物和 node_modules。shell 輸出改成保留頭尾，因為測試摘要在尾端，原本的截斷剛好把最重要的部分丟掉。每個錯誤訊息都要告訴模型下一步做什麼。」

可能的追問：

- **為什麼 grep 不直接改成 regex？** 會改變既有語意（`.`、`(` 變特殊字元），也可能讓原本有結果的搜尋變成錯誤；「字面沒結果才試 regex」對既有行為零影響。
- **run_tests 為什麼用 junit XML 而不是 pytest `-rA`？** 輸出大小可控、結構穩定。
- **eviction 和摘要差在哪？** eviction 是單一工具結果太大時當場移出；摘要是整段對話累積太大時壓縮歷史（見 05）。
