# Quill

Quill 是用 Go 寫的 coding agent：一套可組裝的 agent SDK、上面的 `quill` CLI / TUI / GUI，以及評測工具 `quillbench`。

- 分階段計畫：[`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md)
- GUI 後續優先順序：[`GUI_ROADMAP.md`](GUI_ROADMAP.md)

## 設計原則

- 圖執行、checkpoint、interrupt、訊息合併用 [langgraphgo](https://github.com/smallnest/langgraphgo)；OpenAI Chat Completions 用 [langchaingo](https://github.com/tmc/langchaingo)。Go 沒有官方 LangGraph。
- middleware、backend、profiles、CLI 自建。跨 thread `Store` 與 `files` reducer 套件沒有對等物，也留在這個專案。
- 未實作的函式一律回傳 `notimpl.Err("pkg.Func")`（包裝 `notimpl.ErrNotImplemented`），用 `errors.Is` 判斷。
- Python 的 sync / async 雙版本 API（如 `ls` / `als`）在 Go 統一為帶 `context.Context` 的單一版本。
- Python 的 `AgentMiddleware` 以 Go「可選介面」表達：middleware 只需實作 `Name()`，再依需要實作各 hook 介面。

## 分層

```
cmd/quill, code/*        CLI
quill (根套件)            CreateAgent：組裝預設 middleware、subagents、profiles
middleware/*             Agent middleware（filesystem、subagents、summarization ...）
backend                  檔案/shell 後端（state、store、filesystem、sandbox、composite ...）
profiles                 Harness / Provider profiles
agent                    Middleware 介面；圖用 langgraphgo MessageGraph 組裝
llm, tool                訊息與 ChatModel；OpenAI 呼叫走 langchaingo
runtime                  State、Checkpointer（轉接 langgraphgo）、Store、Interrupt
internal/notimpl         未實作 sentinel error
```

依賴方向由上往下，下層不可 import 上層。

## 開發指令

```sh
make test                         # go test ./...
make test PKG=./runtime ARGS=-v   # 單一 package、額外參數
make check                        # gofmt、vet、test
make run PROMPT='summarize README.md'
make run PROMPT='hi' MODEL=openai:gpt-5.5
make run PROMPT='continue' THREAD=thread-id   # 續跑 thread（需要設定 Postgres 才能跨行程保存）
make serve                                    # 常駐 server，預設 127.0.0.1:8123
make install                                  # go install ./cmd/quill
```

`quill` 的 prompt 可以用 `-n`，也可以直接當參數：

```sh
go run ./cmd/quill -n 'summarize README.md'
go run ./cmd/quill summarize README.md
```

### 寫檔與執行指令

`quill` 以目前目錄為專案根目錄。檔案工具（`ls`、`read_file`、`write_file`、`edit_file`、`glob`、`grep`）把它當成 `/`，不能讀寫根目錄以外的路徑；`execute` 在根目錄下用 `sh -c` 執行指令，執行過的工具會列在 stderr（`[tool] write_file /main.go`）。

- 在 git 專案裡，`grep` 與 `glob` 只搜尋 git 沒有忽略的檔案（依 `.gitignore`）；兩者也都跳過 `node_modules`、`.venv`、`__pycache__`。若明確搜尋被忽略的目錄（`path` 指向它，或 glob pattern 寫出 `node_modules`），照常搜尋。
- `grep` 以字面比對；字面沒有結果、而 pattern 看起來是 regex（例如 `export.*posts`）時，自動改用 RE2 regex 再搜一次，並在結果附註。
- `AGENTS.md` 每個 turn 開始時重新讀取：同一個 thread 中途改了記憶（你或 agent 改的），下一個 turn 就看得到。

另外有幾個把指令輸出整理成 JSON 的 coding 工具（`code/codetools`）：

- `git_status` / `git_diff`：改了哪些檔案、每個檔案的增刪行數與 diff（唯讀，不需核准）
- `apply_patch`：Codex 格式的多行、多檔案修改；任何一個 hunk 對不上就整個不套用
- `run_tests`：Go（`go test -json`）、JS/TS（依 `package.json` 選 mocha / jest / vitest 的 JSON 報告，都沒有時退回 `npm test` 原始輸出）、Python（pytest junit XML；沒裝 pytest 時改跑標準庫 `unittest -v` 並解析輸出），整理成通過 / 失敗數、失敗位置（file:line）與狀態 `passed` / `failed` / `error` / `no_tests`
- `diagnostics`：Go（`go build` + `go vet`）、JS/TS（有 `tsconfig.json` 跑 `tsc --noEmit`、有 ESLint 設定跑 eslint）、Python（有 `ruff` 跑 ruff，否則對每個 `.py` 做語法檢查）；JS 專案既沒有 `tsconfig.json` 也沒有 ESLint 設定時，對每個 `.js` / `.cjs` / `.mjs` 做 `node --check` 語法檢查（有 `.ts` 檔會附一則 info 說明沒做型別檢查），整理成 file / line / severity / message 清單
- 兩個工具都吃 `dir`（專案子目錄，例如 monorepo 的 `web`）；沒給時根目錄若沒有該語言的標記檔（`go.mod`、`package.json`、`pyproject.toml` 等），會從 `files` 往上找，再往下找 3 層，唯一一個就在那裡執行，有多個則回 `unsupported` 並列出候選；回報的路徑一律相對於 workspace 根目錄
- 語言依 `language` 參數、`files` 副檔名、根目錄標記檔（`go.mod`、`pyproject.toml` 等 Python 標記、`package.json`）依序決定
- `edit_file` 的 `old_string` 出現多次時，錯誤訊息會列出前 5 處的行號與前一行

`-n` 與 quillbench 這類 headless 執行另外掛了驗證閘門（`code/agent/verify.go`）：這一輪改過檔案，之後卻沒有通過的測試或 build（`run_tests` 或 `execute` 跑 `go test`、`npm test`、`pytest` 等），agent 想結束時會被擋回去繼續驗證，最多擋 2 次；測試因 `ECONNREFUSED` 失敗時會附上啟動對應服務（Redis、MongoDB、PostgreSQL、MySQL）的提示。檢查失敗時，閘門會先 `git stash`（含未追蹤檔）在原始 HEAD 上重跑同一個檢查再還原：HEAD 上也失敗的測試不擋，HEAD 上通過的明確告訴 agent 是它造成的 regression；重跑又通過的視為 flaky。結束前另外檢查一次 diff：殘留檔（`dump.rdb`、log、`__pycache__` 等）、改到的既有測試，以及任務 `## Requirements` 清單逐項對照。TUI 與 GUI 不掛。

`write_file`、`edit_file`、`apply_patch`、`delete`、`execute`、`run_tests`、`diagnostics` 執行前會先問你（`ls`、`read_file`、`grep`、`glob`、`git_status`、`git_diff` 不問）：

- TUI：跳出核准框，`a` 核准、`r` 拒絕（可輸入理由，理由會回傳給模型）、`A` 本次全部核准
- `-n` 且 stdin 是終端機：逐一問 `approve? [y]es / [a]ll / [N]o / or type a reason to reject`
- `-n` 但 stdin 不是終端機（管線、CI）：自動拒絕並告訴模型沒有人可以核准
- `-y`（或 TUI 裡的 `/yolo`、config.toml 的 `startup.mode = "yolo"`）：全部自動核准
- `shell.allow_list`：符合的 `execute` 指令免核准；`run_tests`、`diagnostics` 會依專案啟動不同的執行器，清單要包含它們可能用到的全部指令才免核准（`run_tests`：`go`、`npx`、`npm`、`node`、`python3`、`python`；`diagnostics`：`go`、`npx`、`node`、`python3`、`python`、`ruff`）（見下方設定檔）

核准框會顯示 `write_file` / `edit_file` / `apply_patch` 對照磁碟上現有檔案的 unified diff。

> **注意**：macOS 與 Linux 預設在 OS 沙箱（`workspace-write`）內跑 shell 指令，見下方「Shell 沙箱」。設成 `full-access`（或在其他平台）時 shell 指令沒有沙箱，核准後可以存取整台機器，`-y` 等於沒有保護。

### Shell 沙箱

`execute`、`run_tests`、`diagnostics` 的 shell 子程序在 OS 層沙箱內執行（macOS Seatbelt `sandbox-exec`，Linux bubblewrap `bwrap`），做法對標 Claude Code、Codex CLI 與 Cursor。quill 本身不進沙箱，只包住 shell 指令。完整的使用與設定說明見 [docs/sandbox.md](docs/sandbox.md)。

#### 快速開始

macOS 不用任何設定，沙箱預設就開著；Linux 先裝 bubblewrap（`sudo apt install bubblewrap`）。GUI、TUI、`-n` 都套用同一套沙箱：

```sh
cd 你的專案
quill sandbox -- touch ~/x          # 先確認：應該被擋（Operation not permitted / Read-only file system）
quill sandbox -- go test ./...      # 專案內的建置、測試照常
quill                               # GUI（-tui 終端介面、-n '...' 單次執行）
quill -sandbox read-only            # 這次只讀：模型不能改任何檔案
```

需要讓 agent 安裝套件（`go get`、`npm install`、`pip install`）時，在 `~/.quill/config.toml` 加上：

```toml
[sandbox]
network = "allowlist"
```

#### 模式與網路

| `sandbox.mode` | 寫入 | 讀取 |
|---|---|---|
| `workspace-write`（macOS、Linux 預設） | 專案目錄、暫存目錄、建置 cache（Go、npm、pip ...）與 `sandbox.writable_roots`；但 `.git/hooks`、`.git/config`、`.quill/` 不可寫 | 整台機器，但 `~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、`~/.config/gh`、`~/.netrc`、`~/.quill/.env`、docker socket 等不可讀 |
| `read-only` | 都不可寫（`write_file`、`edit_file`、`apply_patch`、`delete` 也一併拒絕） | 同上 |
| `full-access`（其他平台預設） | 不限制，沒有沙箱 | 不限制 |

| `sandbox.network` | 網路 |
|---|---|
| `none`（預設） | 只有 localhost |
| `allowlist` | 只能透過 quill 內建的 HTTP proxy 連到允許的網域：預設 `proxy.golang.org`、`sum.golang.org`、`github.com`、`registry.npmjs.org`、`pypi.org`、`files.pythonhosted.org`，`sandbox.allowed_domains` 可擴充（`*.example.com` 代表子網域）；解析到內網、loopback、link-local 的位址一律拒絕。指令透過 `HTTP(S)_PROXY` 環境變數使用 proxy（go、npm、pip、git、curl 都支援） |
| `full` | 不限制 |

#### 行為

- 核准：沙箱開啟時 `execute`、`run_tests`、`diagnostics` 不再逐一詢問（對標 Claude Code 的 `autoAllowBashIfSandboxed`）；破壞性指令仍一律要核准。
- 逃生口：指令被沙箱擋下時，結果會附上 `[sandbox]` 說明；模型可以對 `execute` 傳 `"sandbox": false` 請求在沙箱外執行，**一律要你核准**（核准框標示 OUTSIDE the sandbox）；`-n` 搭配自動核准時沒人能核准，直接拒絕。`sandbox.allow_unsandboxed = false` 完全關掉逃生口。
- Trace：每個 `execute` 結果記錄沙箱模式（或 `unsandboxed`）與是否疑似被擋。
- 沙箱擋的是 shell；檔案工具本來就鎖在專案目錄內（virtual mode），`.git/hooks`、`.git/config` 也同樣不可寫。

#### 設定

只有 `~/.quill/config.toml` 的 `[sandbox]` 能設定沙箱，專案目錄裡的設定改不了它（完整範例見下方「設定檔」）。優先順序：flag > 環境變數 > config.toml > 預設。

| 設定 | 預設 | 其他覆寫方式 |
|---|---|---|
| `mode` | macOS、Linux `workspace-write`；其他平台 `full-access` | `-sandbox <mode>`（`quill`、`quill serve`）、`QUILL_SANDBOX_MODE` |
| `network` | `none` | `quill sandbox -network <n>`（只影響這個試跑指令） |
| `allowed_domains` | 空（只用內建清單） | — |
| `writable_roots` | 空 | — |
| `deny_read` | 空 | — |
| `on_unavailable` | `error` | — |
| `allow_unsandboxed` | `true` | — |

用 `quill serve` + `quill -server URL` 時，沙箱由 server 端的設定與 `quill serve -sandbox` 決定。

#### 確認與除錯

- `quill sandbox [-mode m] [-network n] [-print-policy] -- 指令`：用 agent 同一套規則跑一個指令，`-print-policy` 印出可寫 / 不可寫 / 不可讀的路徑（macOS 另外印出 Seatbelt profile）。
- 寫入被擋、但那個目錄確實需要寫：加到 `writable_roots`（必須是絕對路徑或 `~/` 開頭）。
- 要連網：`network = "allowlist"`，被擋的網域加到 `allowed_domains`；proxy 回 403 代表網域不在清單內。
- Linux 啟動失敗說找不到 bwrap：安裝 bubblewrap；說無法建立 namespace：允許 unprivileged user namespace（Ubuntu 24.04+ 為 `sysctl kernel.apparmor_restrict_unprivileged_userns=0`），或在 Docker 裡要 `--privileged`。暫時不想處理時設 `mode = "full-access"`（關掉沙箱），或 `on_unavailable = "warn"`（不受限執行，每個指令結果都會警告）。
- 更多狀況見 [docs/sandbox.md](docs/sandbox.md#常見問題)。

### 腳本與 CI

```sh
quill -q -n '用一句話總結 README.md' > summary.txt   # stdout 只有回覆，沒有 thread id 與 tool 行
quill -no-stream -n '...'                            # 回覆完成後一次寫出，不逐字串流
quill -timeout 10m -n '跑測試並修好'                 # 逾時取消，exit code 124；Ctrl-C 為 130
quill threads -json                                  # {"schema_version":1,"command":"threads list","data":[...]}
```

`-q`、`-no-stream`、`-timeout` 需要搭配 `-n`（或位置參數 prompt）。

### 設定檔 `~/.quill/config.toml`

優先順序：flag > 環境變數 > config.toml > 預設。檔案不存在時全部用預設；格式錯誤時啟動失敗並指出行號。

```toml
[models]
default = "openrouter:openai/gpt-6-luna-pro"   # 等同 QUILL_MODEL

[startup]
mode = "manual"                            # 或 "yolo"（等同 -y）

[shell]
# TOML 陣列或逗號字串；"recommended" 展開成唯讀指令（ls、cat、grep、wc、diff ...），可與其他指令混用；
# "all" 必須單獨使用，所有 execute 都免核准。環境變數 QUILL_SHELL_ALLOW_LIST 會覆寫。
allow_list = ["recommended", "go", "make"]

[retries]
max_retries = 2                            # 模型呼叫遇到 429 / 5xx / 連線錯誤或空回應的重試次數，0 = 不重試

[server]
url = "http://127.0.0.1:8123"              # 等同 QUILL_SERVER

[sandbox]
mode = "workspace-write"                   # read-only / workspace-write（macOS、Linux 預設）/ full-access；等同 QUILL_SANDBOX_MODE
network = "allowlist"                      # none（預設）/ allowlist / full
allowed_domains = ["*.githubusercontent.com"]  # allowlist 額外放行的網域
writable_roots = ["~/scratch"]             # workspace-write 額外可寫的目錄
deny_read = ["~/.config/secret-tool"]      # 額外不可讀的路徑
on_unavailable = "error"                   # 平台沙箱無法使用時：error（預設，拒絕啟動）或 warn
allow_unsandboxed = true                   # 允許模型請求（經核准）在沙箱外執行單一指令
```

allow-list 只比對每段指令（以 `|`、`;`、`&&`、`||` 切開）的第一個字；含 `$(`、反引號、`$VAR`、`${`、重導向（`>`、`<`、`>>`、`<<`）、背景 `&`、換行或 tab 的指令一律要核准。例如 `ls | grep x` 免核准，`ls "$(rm -rf /)"` 與 `ls &` 仍會問。

不論是否開啟沙箱，下列保護都會生效：

- 破壞性指令（`rm -rf` 根目錄、家目錄或整個專案，`git reset --hard`、`git clean -f`、`git checkout -- .`、`git push --force`、`curl ... | sh`、`mkfs`、`dd of=/dev/...` 等）：
  - 互動模式下一律要核准，即使用了 `-y`、allow-list 為 `"all"`，或本次已選擇自動核准；
  - `-n` 這類沒人能核准的模式下直接拒絕執行，並告訴 agent 改用範圍較小的指令。
- 環境變數：quill 會把 `~/.quill/.env` 載入自己的環境，但 shell 指令看不到名稱像 credential 的變數（`*_API_KEY`、`*_TOKEN`、`*_SECRET`、`*_PASSWORD`、`*_CONN_STRING` 等）。需要時以 `QUILL_SHELL_PASS_ENV=GITHUB_TOKEN,NPM_TOKEN` 逗號列出要放行的變數。
- Trace：寫入前把上述變數的值、常見 token 格式（`sk-...`、`ctx7sk-...`、`ghp_...`、`AKIA...`、`xox?-...`、`AIza...`）與 URL 中的密碼換成 `[REDACTED]`。

```sh
mkdir /tmp/demo && cd /tmp/demo
quill -n '建立 main.go 印出 hello，然後 go run main.go'
```

### 常駐 server 與 TUI（邊寫邊測）

```sh
# 終端 A：常駐 server（只綁 loopback；先在 ~/.quill/.env 設定 QUILL_POSTGRES_CONN_STRING 與 QUILL_REDIS_URL）
quill serve                        # 或 make serve；-addr 127.0.0.1:8123、-model provider:model

# 終端 B：任何專案目錄
export QUILL_SERVER=http://127.0.0.1:8123
cd ~/work/demo
quill                              # 開啟瀏覽器 GUI；-tui 改用全螢幕 TUI，非終端機或加 -plain 時改用逐行 REPL
quill -r                           # 接續這個目錄最近的 thread
quill -thread <id>                 # 接續指定 thread
quill -n '加上 --upper 參數' -r    # 單次續跑
quill threads                      # 列出這個目錄的 threads（-all 列出全部）
```

- 沒設 `QUILL_SERVER`（或 `-server`）時，agent 直接在 `quill` 行程內執行。
- quill 需要 Postgres 與 Redis，任一個沒設定或連不上就啟動失敗。把 `QUILL_POSTGRES_CONN_STRING`（相容 `POSTGRES_CONN_STRING`）與 `QUILL_REDIS_URL`（相容 `REDIS_URL`）放在 `~/.quill/.env`。
  - Postgres 存重啟後要讀回的內容：thread 與待核准的 tool call（`quill_checkpoints`、`quill_threads`）、計畫（`quill_plans`）、trace（`quill_trace_records`、`quill_trace_blobs`）、摘要前的完整對話（`conversation_history`）。
  - Redis 存可重新產生的快取，會過期：網頁原文（24 小時）、搜尋 / 擷取 / Context7 結果、被移出 context 的大型 tool 輸出（`storage.results_ttl`，預設 7 天）。過期後讀取會提示重跑工具。
  - 舊版留下的 `~/.quill/plans/` 與 `~/.quill/traces/` 已不再使用，可以刪除。
  - `quillbench` 仍使用 `POSTGRES_CONN_STRING`，可連到同一容器的另一個資料庫。
- 一個 thread 綁定建立它的目錄，換目錄使用同一個 thread 會被拒絕；同一個 thread 同時只能跑一個 run。
- 回覆會邊產生邊顯示（token 串流）；tool call 的參數要等模型說完才會出現。完成的回覆在 TUI 裡以 markdown 渲染。
- `quill serve` 收到 Ctrl-C / SIGTERM 時停止接新 run，進行中的 run 最多有 30 秒收尾，之後被取消；再按一次 Ctrl-C 立即結束。被取消的 tool call 下一輪會自動補上「未完成」的結果，thread 可以直接接續。
- 模型呼叫遇到 429、5xx、連線錯誤，或 HTTP 200 卻沒有任何 choices（串流則是什麼都沒送出）的空回應會自動重試（預設 2 次，`retries.max_retries` 可調；串流已經送出內容後就不重試）；串流超過 5 分鐘沒有新資料視為卡住並報錯。
- 每個 thread 只保留最新 10 筆 checkpoint，長對話不會讓 checkpoint 表無限成長。

### GUI

`quill` 預設在 `127.0.0.1` 的隨機 port 開一個網頁介面並自動開瀏覽器（`-no-open` 只印出網址）。網址裡的 `#token=` 是這次啟動才有效的存取碼，不會送進 server log；API 只接受 loopback Host 與同源請求。

重新啟動 GUI 時，預設開啟目前目錄最近有對話內容的 thread；`-thread <id>` 可指定，`-r` 可明確要求最近的 thread。側欄的 ✎ 可替已開始的 thread 命名，名稱會隨 thread 保存；執行中暫停切換 thread，避免把串流內容顯示到另一段對話。

- 時間軸保留 TUI 的收合結構（`main · LLM #n`、tool call 樹、subagent 縮排），可以一路往上捲到 thread 開頭。
- 點 tool call 展開詳細內容：grep 依檔案分組並標出命中、read_file 帶行號、execute 顯示終端輸出、exit code 與沙箱 badge（見 [docs/sandbox.md](docs/sandbox.md#在-gui-看指令有沒有跑在沙箱裡)）、edit/write 以 GitHub 風格 diff（unified / split）呈現；另有 Arguments、Output、Raw JSON 分頁。
- 輸入框可附圖片：直接貼上、拖進輸入框或按 Attach image。支援 PNG、JPEG、GIF、WebP，單張 5 MB、合計 10 MB；目前只有 OpenAI 相容的 provider（OpenAI、OpenRouter）會送出圖片，模型本身要支援 vision。圖片存在 thread 裡，之後每一輪都會一起送給模型。
- 點 LLM 那一行（或 `inspect`）打開側欄，看這次呼叫實際送出的 messages（標出和上一次相比新增的部分）、回覆、提供的 tools 與 token 用量。
- 每個 turn 結尾有 Files changed 摘要；需要核准時直接在頁面上看 diff 預覽後核准或附理由拒絕。
- 上方可篩選 Edits / Commands / Errors / LLM calls、搜尋內容；鍵盤 `j`/`k` 移動、Enter 展開、`e` 全部展開、`/` 搜尋、`i` 打開 LLM 側欄、`G` 跳到底、Esc 關閉。

### Plan 模式與 todo

互動 session（GUI、TUI、REPL）有 Agent / Plan 兩種模式：

- **Agent**（預設）：直接動手。多步驟的工作會先探索、再用 `write_todos` 列出具體的 todo（每項有 `id`、`content`、`status`，可選 `notes`；狀態有 `pending`、`in_progress`、`completed`、`cancelled`）。之後用 `merge: true` 依 `id` 只更新變動的項目。還沒查清楚的事情以 `Investigate:` 開頭列出。
- **Plan**：GUI 輸入框旁的 Agent/Plan 切換，或 TUI/REPL 的 `/plan`。只開放唯讀工具（`ls`、`read_file`、`glob`、`grep`、`web_search`、`fetch_url`、`resolve_library_id`、`query_docs`、`git_status`、`git_diff`），其他工具一律擋下。模型先研究，有需要用 `ask_user` 問你，最後呼叫 `create_plan`，把計畫存進 Postgres（id 為 `<thread>-<名稱>`）後停下來。計畫是查證過的結果：包含發現（附路徑）、做法、驗證方式與風險，結尾是 todo checklist，不應再有待調查的項目。
- **Build**：核准計畫只是標記，要按 GUI 的 Build 或輸入 `/build` 才會切回 Agent 模式開始實作。計畫的 todo 會成為 thread 的 todo list（第一項設為 `in_progress`），模型依序完成並回報進度。同一個 thread 再進 Plan 模式修改時，會覆寫同一個計畫。

`ask_user` 會中斷 run 等你回答（選項可多選，另有自由輸入的 Other），即使開了自動核准也一樣。GUI 在核准卡片上作答；TUI 用方向鍵或數字選擇、空白鍵切換多選、Esc 略過；REPL 輸入以逗號分隔的選項編號，或直接打字當作 Other。`POST /runs` 用 `mode`（`agent` / `plan`，plan 需要 `interactive`）與 `build_plan` 控制模式，`GET /threads/{id}` 會回傳 `todos` 與 `plan`。

完整的 LLM 輸入、檔案 diff 與 subagent 訊息記在 Postgres（重複內容只存一次）；`fetch_url` 擷取網頁用的模型呼叫也會記下，GUI 在該 tool call 的詳細內容顯示，GUI 從 `GET /threads/{id}/trace` 與 `GET /threads/{id}/trace/llm/{seq}` 讀取。

前端原始碼在 `code/gui/web`（React + Vite），建置結果 `code/gui/dist` 有進版控，一般 `go build` 不需要 Node。改前端後執行 `make web`（需要 Node）重新產生，`make web-test` 跑前端測試；開發時先跑 `quill -no-open`，再到 `code/gui/web` 執行 `QUILL_GUI_URL=http://127.0.0.1:<port> npm run dev`，用 Vite 的網址加上同一段 `#token=...` 開啟即可熱更新。

TUI 按鍵（`quill -tui`）：

| 按鍵 / 指令 | 作用 |
| --- | --- |
| Enter | 送出 |
| Ctrl-C | 執行中：取消這次 run；閒置：清空輸入框，輸入框已空則離開 |
| Ctrl-D | 離開 |
| PgUp / PgDn | 捲動對話 |
| `/new`、`/thread`、`/threads`、`/resume <id>` | 新 thread、顯示 id、選擇 thread、切換 thread |
| `/yolo` | 切換自動核准 |
| `/plan`、`/build` | 切換 Plan 模式、實作最新的計畫 |
| `/help`、`/quit` | 說明、離開 |

server 的 HTTP 介面：`GET /healthz`、`POST /runs`（回傳 NDJSON：`delta`、`message`、`interrupt`、`trace`、`done`、`error`）、`GET /threads?cwd=&limit=`、`GET /threads/{id}`、`GET /threads/{id}/trace`、`GET /threads/{id}/trace/llm/{seq}`。

不要在 `go run ./cmd/quill` 後面加 `--`：Go 的 `flag` 套件會把 `--` 之後的東西都當成一般參數，`-n` 就會變成 prompt 的一部分。

模型設定：`-model` 優先，其次是環境變數 `QUILL_MODEL`、config.toml 的 `models.default`，預設 `openai:gpt-5.5`。格式是 `provider:model`，只以第一個 `:` 切分，所以 OpenRouter 的 slug 可以直接寫：

| provider | 範例 | API key | 端點覆寫 |
| --- | --- | --- | --- |
| `openrouter` | `openrouter:openai/gpt-6-luna-pro` | `OPENROUTER_API_KEY` | `OPENROUTER_API_BASE` |
| `openai` | `openai:gpt-5.5` | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |

OpenRouter 會帶上 app attribution header（`HTTP-Referer`、`X-Title`），可用 `OPENROUTER_APP_URL`、`OPENROUTER_APP_TITLE` 覆寫，設成空字串則不送。

### API key 與 `.env`

```sh
cp .env.example .env   # 填入 OPENAI_API_KEY
make run PROMPT='hello'
```

`quill` 啟動時會載入 `.env`，優先順序（高到低）：

1. shell 已設定的環境變數（永遠不會被 `.env` 覆寫）
2. 專案 `.env`：從目前目錄往上找到的第一個 `.env`
3. 全域 `~/.quill/.env`

`BRAVE_API_KEY`（Brave Search）也放在 `.env`：有設定時 quill 才會掛上 `web_search` 工具，`fetch_url` 不需要 key。`CONTEXT7_API_KEY` 會掛上 `resolve_library_id` / `query_docs`，查詢指定版本的函式庫文件。

web 工具的做法對齊 Claude Code：

- `fetch_url(url, prompt)` 用 `models.web`（config.toml，未設定用主模型）讀整頁，只把答案回給主 agent。完整頁面存成 `/.quill/web/<hash>.md`，需要細節時可以 `read_file` / `grep`。
- `web_search` 預設 8 筆，可用 `allowed_domains` / `blocked_domains` 限定來源。
- 搜尋與擷取結果快取 15 分鐘，Context7 快取 1 小時。
- 有 Brave 或 Context7 key 時，會多一個 `web-researcher` subagent：它用 `models.web`，只有 web 工具與唯讀檔案工具，每次最多 4 次搜尋、8 頁，最後回報結論、引用 URL、適用版本與來源衝突。啟動它的 `task` 需要核准，核准後裡面的網路呼叫不再逐一詢問。
- `task` 可加 `run_in_background: true` 在背景執行。主 agent 先做別的事，結果以 `<task-notification>` 送回，或用 `task_output` 等待；run 會等背景 task 結束才收尾，取消 run 會一併取消。背景 task 只存在 server 記憶體：`quill serve` 重啟，或 run 停下來等待核准時，背景 task 會中止，下一輪會收到「cancelled」通知。

`.env` 已列在 `.gitignore`，只有 `.env.example` 會進版控。

### SWE-bench Pro V2 評測（`quillbench`）

之後的迭代以真實能力為準：`quillbench` 用 [SWE-bench Pro V2](https://github.com/scaleapi/SWE-bench_Pro-os) 固定 50 題（`code/bench/subsets/smoke50.txt`，依語言、HARD 比例與 repo 分層抽樣）衡量 quill，表現穩定後再跑 `-subset full`（642 題）或 `-subset hard`（51 題）。每次執行的結果、每題的 F2P / P2P 測試、失敗分類、步數、token、成本與時間都寫進 PostgreSQL（`bench_*` 表與 `bench_run_summary`、`bench_history` 等 view），方便追蹤是否改善。

評測遵照官方 locked protocol：agent 在斷網的 task 映像裡工作（沒有 web 工具、每題 50 分鐘、1 CPU / 4 GB），quill 編輯掛載進容器的 repo 副本，`execute` 以 `docker exec` 在容器內執行；結束後用 `git add -A; git diff --cached` 取出 patch，在乾淨映像上用官方 `tests/test.sh` 重新評分。

```sh
# 專用 Postgres（不和其他專案共用）
docker run -d --name quillbench-postgres -p 127.0.0.1:5433:5432 \
  -e POSTGRES_USER=quillbench -e POSTGRES_PASSWORD=quillbench -e POSTGRES_DB=quillbench \
  -v quillbench-pgdata:/var/lib/postgresql/data postgres:16-alpine
export POSTGRES_CONN_STRING='postgres://quillbench:quillbench@127.0.0.1:5433/quillbench?sslmode=disable'
export REDIS_URL='redis://127.0.0.1:6379/0'   # quill 的快取，quillbench run 也需要

make bench-prepare                 # 固定 commit 的 task 目錄、驗證 SHA256SUMS、HF metadata、寫入 bench_tasks
make bench-gate                    # 驗收評分器：官方 patch（oracle）要全過、空 patch（nop）要全不過
make bench-smoke MODEL=openrouter:z-ai/glm-5.3   # 跑 smoke50

go run ./cmd/quillbench run -limit 3 -model openrouter:moonshotai/kimi-k3   # 先試前 3 題
go run ./cmd/quillbench run -resume <run-id>          # 中斷後接續（已完成的題目不重跑）
go run ./cmd/quillbench report <run-id>               # 解題率（Wilson 95% 區間）、分語言 / repo / 難度、失敗分類、過程指標
go run ./cmd/quillbench compare <base-run> <new-run>  # 同題配對：McNemar、paired bootstrap、翻轉的題目
go run ./cmd/quillbench history                       # 歷次執行
go run ./cmd/quillbench refs import -harness mini-swe-agent -model glm-5.3 eval_results.json
go run ./cmd/quillbench compare -ref mini-swe-agent/glm-5.3 <run-id>          # 和別人同題比較
```

- 資料放在 `~/.quill/bench/`：task repo、metadata、每次嘗試的 log 與 agent trace（`logs/<run>/<instance>/t<trial>/`）、markdown 報表（`reports/`）。
- 每題映像約 1 GB（`ghcr.io/scaleapi/swe-bench_pro-v2`，linux/amd64）；磁碟吃緊時加 `-prune-images`，網路慢時建議先跑 `bench-gate` 把映像拉好。
- `-max-cost`（預設每題 3 美元）與 `-max-steps`（預設 250）是額外上限；`infra_error` 不算分，可用 `-retry-infra` 重跑。
- `-disable baseline,diff_review,test_edits,progress,budget` 關掉個別 agent 功能做消融比較，會記在 run 的 config。`test_edits`：結束前若改過既有測試，先把這些測試檔暫時換回原版、在目前的程式碼上跑一次，原版失敗就列出來，請 agent 還原測試修正程式，或指出任務哪一條要求這個行為改變。`progress`：同一個呼叫連續兩次以同樣方式失敗、中間沒有編輯、shell 指令或新的使用者訊息時，第三次不執行。`budget`：headless run 用掉 80% 的時間或步數後，每個請求尾端提醒收斂。
- `go run ./cmd/quillbench analyze <run-id>...` 從 trace 重算過程指標並分類失敗，報表最後有離線重播（progress monitor 會介入幾次），最後是各元件實際觸發的次數（verify gate 各種類、progress、budget、危險指令、version guard、grep regex 重試、service hint、shell 截斷），以及 HEAD baseline 的每次判定（regression、pre-existing、flaky ...，記在 attempt 的 process JSON），用來找出從不觸發的元件。
- 比較別的 harness 需要逐題成績：CSV（`instance_id,resolved[,steps,cost_usd]`）或 `{"instance_id": true}` JSON。
