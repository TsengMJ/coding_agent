# deepagents_golang

以 Go 重新實作 [deepagents](../deepagents)（LangChain Deep Agents）的練習專案。

- 進度追蹤：[`TODO.md`](TODO.md)（所有 Python 功能/物件與 Go 實作狀態的對照表，持續更新）
- 分階段計畫：[`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md)

## 設計原則

- 圖執行、checkpoint、interrupt、訊息合併用 [langgraphgo](https://github.com/smallnest/langgraphgo)；OpenAI Chat Completions 用 [langchaingo](https://github.com/tmc/langchaingo)。Go 沒有官方 LangGraph。
- Deep Agents 的 middleware、backend、profiles、CLI 仍自建。跨 thread `Store` 與 `files` reducer 套件沒有對等物，也留在這個專案。
- 未實作的函式一律回傳 `notimpl.Err("pkg.Func")`（包裝 `notimpl.ErrNotImplemented`），用 `errors.Is` 判斷。
- Python 的 sync / async 雙版本 API（如 `ls` / `als`）在 Go 統一為帶 `context.Context` 的單一版本。
- Python 的 `AgentMiddleware` 以 Go「可選介面」表達：middleware 只需實作 `Name()`，再依需要實作各 hook 介面。

## 分層

```
cmd/dcode, code/*        CLI（對應 libs/code）
deepagents (根套件)       CreateDeepAgent：組裝預設 middleware、subagents、profiles
middleware/*             Deep Agents middleware（filesystem、subagents、summarization ...）
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
make run PROMPT='continue' THREAD=thread-id   # 續跑 thread（in-process 需要 Postgres 才留得住）
make serve                                    # 常駐 server，預設 127.0.0.1:8123
make install                                  # go install ./cmd/dcode
```

`dcode` 的 prompt 可以用 `-n`，也可以直接當參數：

```sh
go run ./cmd/dcode -n 'summarize README.md'
go run ./cmd/dcode summarize README.md
```

### 寫檔與執行指令

`dcode` 以目前目錄為專案根目錄。檔案工具（`ls`、`read_file`、`write_file`、`edit_file`、`glob`、`grep`）把它當成 `/`，不能讀寫根目錄以外的路徑；`execute` 在根目錄下用 `sh -c` 執行指令，執行過的工具會列在 stderr（`[tool] write_file /main.go`）。

`write_file`、`edit_file`、`execute` 執行前會先問你（`ls`、`read_file`、`grep`、`glob` 不問）：

- TUI：跳出核准框，`a` 核准、`r` 拒絕（可輸入理由，理由會回傳給模型）、`A` 本次全部核准
- `-n` 且 stdin 是終端機：逐一問 `approve? [y]es / [a]ll / [N]o / or type a reason to reject`
- `-n` 但 stdin 不是終端機（管線、CI）：自動拒絕並告訴模型沒有人可以核准
- `-y`（或 TUI 裡的 `/yolo`、config.toml 的 `startup.mode = "yolo"`）：全部自動核准
- `shell.allow_list`：符合的 `execute` 指令免核准（見下方設定檔）

核准框會顯示 `write_file` / `edit_file` 對照磁碟上現有檔案的 unified diff。

> **注意**：shell 指令沒有沙箱，核准後可以存取整台機器；`-y` 等於沒有保護。請在可以放心讓它改動的目錄下使用。

### 腳本與 CI

```sh
dcode -q -n '用一句話總結 README.md' > summary.txt   # stdout 只有回覆，沒有 thread id 與 tool 行
dcode -no-stream -n '...'                            # 回覆完成後一次寫出，不逐字串流
dcode -timeout 10m -n '跑測試並修好'                 # 逾時取消，exit code 124；Ctrl-C 為 130
dcode threads -json                                  # {"schema_version":1,"command":"threads list","data":[...]}
```

`-q`、`-no-stream`、`-timeout` 需要搭配 `-n`（或位置參數 prompt）。

### 設定檔 `~/.deepagents/config.toml`

優先順序：flag > 環境變數 > config.toml > 預設。檔案不存在時全部用預設；格式錯誤時啟動失敗並指出行號。

```toml
[models]
default = "openrouter:openai/gpt-6-luna"   # 等同 DCODE_MODEL

[startup]
mode = "manual"                            # 或 "yolo"（等同 -y）

[shell]
# TOML 陣列或逗號字串；"recommended" 展開成唯讀指令（ls、cat、grep、wc、diff ...），可與其他指令混用；
# "all" 必須單獨使用，所有 execute 都免核准。環境變數 DEEPAGENTS_CODE_SHELL_ALLOW_LIST 會覆寫。
allow_list = ["recommended", "go", "make"]

[retries]
max_retries = 2                            # 模型呼叫遇到 429 / 5xx / 連線錯誤的重試次數，0 = 不重試

[server]
url = "http://127.0.0.1:8123"              # 等同 DCODE_SERVER
```

allow-list 只比對每段指令（以 `|`、`;`、`&&`、`||` 切開）的第一個字；含 `$(`、反引號、`$VAR`、`${`、重導向（`>`、`<`、`>>`、`<<`）、背景 `&`、換行或 tab 的指令一律要核准。例如 `ls | grep x` 免核准，`ls "$(rm -rf /)"` 與 `ls &` 仍會問。

```sh
mkdir /tmp/demo && cd /tmp/demo
dcode -n '建立 main.go 印出 hello，然後 go run main.go'
```

### 常駐 server 與 TUI（邊寫邊測）

```sh
# 終端 A：常駐 server（只綁 loopback）
export POSTGRES_CONN_STRING='postgres://user:pass@localhost:5432/db?sslmode=disable'   # 可省略
dcode serve                        # 或 make serve；-addr 127.0.0.1:8123、-model provider:model

# 終端 B：任何專案目錄
export DCODE_SERVER=http://127.0.0.1:8123
cd ~/work/demo
dcode                              # 開啟瀏覽器 GUI；-tui 改用全螢幕 TUI，非終端機或加 -plain 時改用逐行 REPL
dcode -r                           # 接續這個目錄最近的 thread
dcode -thread <id>                 # 接續指定 thread
dcode -n '加上 --upper 參數' -r    # 單次續跑
dcode threads                      # 列出這個目錄的 threads（-all 列出全部）
```

- 沒設 `DCODE_SERVER`（或 `-server`）時，agent 直接在 `dcode` 行程內執行。
- 有 `POSTGRES_CONN_STRING` 時，thread 內容（含待核准的 tool call）與 threads 列表存在 Postgres（`deepagents_checkpoints`、`dcode_threads` 表），server 重啟後仍可接續；沒設時存在記憶體，行程結束就消失。
- 一個 thread 綁定建立它的目錄，換目錄使用同一個 thread 會被拒絕；同一個 thread 同時只能跑一個 run。
- 回覆會邊產生邊顯示（token 串流）；tool call 的參數要等模型說完才會出現。完成的回覆在 TUI 裡以 markdown 渲染。
- `dcode serve` 收到 Ctrl-C / SIGTERM 時停止接新 run，進行中的 run 最多有 30 秒收尾，之後被取消；再按一次 Ctrl-C 立即結束。被取消的 tool call 下一輪會自動補上「未完成」的結果，thread 可以直接接續。
- 模型呼叫遇到 429、5xx 或連線錯誤會自動重試（預設 2 次，`retries.max_retries` 可調）；串流超過 5 分鐘沒有新資料視為卡住並報錯。
- 每個 thread 只保留最新 10 筆 checkpoint，長對話不會讓 checkpoint 表無限成長。

### GUI

`dcode` 預設在 `127.0.0.1` 的隨機 port 開一個網頁介面並自動開瀏覽器（`-no-open` 只印出網址）。網址裡的 `#token=` 是這次啟動才有效的存取碼，不會送進 server log；API 只接受 loopback Host 與同源請求。

- 時間軸保留 TUI 的收合結構（`main · LLM #n`、tool call 樹、subagent 縮排），可以一路往上捲到 thread 開頭。
- 點 tool call 展開詳細內容：grep 依檔案分組並標出命中、read_file 帶行號、execute 顯示終端輸出與 exit code、edit/write 以 GitHub 風格 diff（unified / split）呈現；另有 Arguments、Output、Raw JSON 分頁。
- 點 LLM 那一行（或 `inspect`）打開側欄，看這次呼叫實際送出的 messages（標出和上一次相比新增的部分）、回覆、提供的 tools 與 token 用量。
- 每個 turn 結尾有 Files changed 摘要；需要核准時直接在頁面上看 diff 預覽後核准或附理由拒絕。
- 上方可篩選 Edits / Commands / Errors / LLM calls、搜尋內容；鍵盤 `j`/`k` 移動、Enter 展開、`e` 全部展開、`/` 搜尋、`i` 打開 LLM 側欄、`G` 跳到底、Esc 關閉。

完整的 LLM 輸入、檔案 diff 與 subagent 訊息記在 `~/.deepagents/traces/<thread>.jsonl`（重複內容只存一次），GUI 從 `GET /threads/{id}/trace` 與 `GET /threads/{id}/trace/llm/{seq}` 讀取。

前端原始碼在 `code/gui/web`（React + Vite），建置結果 `code/gui/dist` 有進版控，一般 `go build` 不需要 Node。改前端後執行 `make web`（需要 Node）重新產生，`make web-test` 跑前端測試；開發時先跑 `dcode -no-open`，再到 `code/gui/web` 執行 `DCODE_GUI_URL=http://127.0.0.1:<port> npm run dev`，用 Vite 的網址加上同一段 `#token=...` 開啟即可熱更新。

TUI 按鍵（`dcode -tui`）：

| 按鍵 / 指令 | 作用 |
| --- | --- |
| Enter | 送出 |
| Ctrl-C | 執行中：取消這次 run；閒置：清空輸入框，輸入框已空則離開 |
| Ctrl-D | 離開 |
| PgUp / PgDn | 捲動對話 |
| `/new`、`/thread`、`/threads`、`/resume <id>` | 新 thread、顯示 id、選擇 thread、切換 thread |
| `/yolo` | 切換自動核准 |
| `/help`、`/quit` | 說明、離開 |

server 的 HTTP 介面：`GET /healthz`、`POST /runs`（回傳 NDJSON：`delta`、`message`、`interrupt`、`trace`、`done`、`error`）、`GET /threads?cwd=&limit=`、`GET /threads/{id}`、`GET /threads/{id}/trace`、`GET /threads/{id}/trace/llm/{seq}`。

不要在 `go run ./cmd/dcode` 後面加 `--`：Go 的 `flag` 套件會把 `--` 之後的東西都當成一般參數，`-n` 就會變成 prompt 的一部分。

模型設定：`-model` 優先，其次是環境變數 `DCODE_MODEL`、config.toml 的 `models.default`，預設 `openai:gpt-5.5`。格式是 `provider:model`，只以第一個 `:` 切分，所以 OpenRouter 的 slug 可以直接寫：

| provider | 範例 | API key | 端點覆寫 |
| --- | --- | --- | --- |
| `openrouter` | `openrouter:openai/gpt-6-luna` | `OPENROUTER_API_KEY` | `OPENROUTER_API_BASE` |
| `openai` | `openai:gpt-5.5` | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |

OpenRouter 會帶上 app attribution header（`HTTP-Referer`、`X-Title`），可用 `OPENROUTER_APP_URL`、`OPENROUTER_APP_TITLE` 覆寫，設成空字串則不送。

### API key 與 `.env`

```sh
cp .env.example .env   # 填入 OPENAI_API_KEY
make run PROMPT='hello'
```

`dcode` 啟動時會載入 `.env`，優先順序（高到低）：

1. shell 已設定的環境變數（永遠不會被 `.env` 覆寫）
2. 專案 `.env`：從目前目錄往上找到的第一個 `.env`
3. 全域 `~/.deepagents/.env`

`BRAVE_API_KEY`（Brave Search）也放在 `.env`：有設定時 dcode 才會掛上 `web_search` 工具，`fetch_url` 不需要 key。

`.env` 已列在 `.gitignore`，只有 `.env.example` 會進版控。

### SWE-bench Pro V2 評測（`dbench`）

之後的迭代以真實能力為準：`dbench` 用 [SWE-bench Pro V2](https://github.com/scaleapi/SWE-bench_Pro-os) 固定 50 題（`code/bench/subsets/smoke50.txt`，依語言、HARD 比例與 repo 分層抽樣）衡量 dcode，表現穩定後再跑 `-subset full`（642 題）或 `-subset hard`（51 題）。每次執行的結果、每題的 F2P / P2P 測試、失敗分類、步數、token、成本與時間都寫進 PostgreSQL（`bench_*` 表與 `bench_run_summary`、`bench_history` 等 view），方便追蹤是否改善。

評測遵照官方 locked protocol：agent 在斷網的 task 映像裡工作（沒有 web 工具、每題 50 分鐘、1 CPU / 4 GB），dcode 編輯掛載進容器的 repo 副本，`execute` 以 `docker exec` 在容器內執行；結束後用 `git add -A; git diff --cached` 取出 patch，在乾淨映像上用官方 `tests/test.sh` 重新評分。

```sh
# 專用 Postgres（不和其他專案共用）
docker run -d --name dbench-postgres -p 127.0.0.1:5433:5432 \
  -e POSTGRES_USER=dbench -e POSTGRES_PASSWORD=dbench -e POSTGRES_DB=dbench \
  -v dbench-pgdata:/var/lib/postgresql/data postgres:16-alpine
export POSTGRES_CONN_STRING='postgres://dbench:dbench@127.0.0.1:5433/dbench?sslmode=disable'

make bench-prepare                 # 固定 commit 的 task 目錄、驗證 SHA256SUMS、HF metadata、寫入 bench_tasks
make bench-gate                    # 驗收評分器：官方 patch（oracle）要全過、空 patch（nop）要全不過
make bench-smoke MODEL=openrouter:z-ai/glm-5.3   # 跑 smoke50

go run ./cmd/dbench run -limit 3 -model openrouter:moonshotai/kimi-k3   # 先試前 3 題
go run ./cmd/dbench run -resume <run-id>          # 中斷後接續（已完成的題目不重跑）
go run ./cmd/dbench report <run-id>               # 解題率（Wilson 95% 區間）、分語言 / repo / 難度、失敗分類、過程指標
go run ./cmd/dbench compare <base-run> <new-run>  # 同題配對：McNemar、paired bootstrap、翻轉的題目
go run ./cmd/dbench history                       # 歷次執行
go run ./cmd/dbench refs import -harness mini-swe-agent -model glm-5.3 eval_results.json
go run ./cmd/dbench compare -ref mini-swe-agent/glm-5.3 <run-id>          # 和別人同題比較
```

- 資料放在 `~/.deepagents/bench/`：task repo、metadata、每次嘗試的 log 與 agent trace（`logs/<run>/<instance>/t<trial>/`）、markdown 報表（`reports/`）。
- 每題映像約 1 GB（`ghcr.io/scaleapi/swe-bench_pro-v2`，linux/amd64）；磁碟吃緊時加 `-prune-images`，網路慢時建議先跑 `bench-gate` 把映像拉好。
- `-max-cost`（預設每題 3 美元）與 `-max-steps`（預設 250）是額外上限；`infra_error` 不算分，可用 `-retry-infra` 重跑。
- 比較別的 harness 需要逐題成績：CSV（`instance_id,resolved[,steps,cost_usd]`）或 `{"instance_id": true}` JSON。
