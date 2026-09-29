# deepagents_golang

以 Go 重新實作 [deepagents](../deepagents)（LangChain Deep Agents）的練習專案。

- 進度追蹤：[`TODO.md`](TODO.md)（所有 Python 功能/物件與 Go 實作狀態的對照表，持續更新）
- 分階段計畫：[`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md)

## 設計原則

- 不依賴 LangChain / LangGraph 的 Go 替代品，底層 agent loop、state、checkpointer、interrupt 皆自建。
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
agent                    Middleware 介面與 agent loop（對應 langchain create_agent）
llm, tool                訊息、ChatModel、Tool（對應 langchain_core）
runtime                  State、Checkpointer、Store、Interrupt、事件串流（對應 LangGraph）
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
make run PROMPT='continue' THREAD=thread-id   # thread 續跑尚未支援
```

`dcode` 的 prompt 可以用 `-n`，也可以直接當參數：

```sh
go run ./cmd/dcode -n 'summarize README.md'
go run ./cmd/dcode summarize README.md
```

不要在 `go run ./cmd/dcode` 後面加 `--`：Go 的 `flag` 套件會把 `--` 之後的東西都當成一般參數，`-n` 就會變成 prompt 的一部分。

模型設定：`-model` 優先，其次是環境變數 `DCODE_MODEL`，預設 `openai:gpt-5.5`。格式是 `provider:model`，只以第一個 `:` 切分，所以 OpenRouter 的 slug 可以直接寫：

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

`.env` 已列在 `.gitignore`，只有 `.env.example` 會進版控。
