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
make run PROMPT='continue' THREAD=thread-id
```

`dcode` 的 prompt 可以用 `-n`，也可以直接當參數：

```sh
go run ./cmd/dcode -- -n 'summarize README.md'
go run ./cmd/dcode -- summarize README.md
```
