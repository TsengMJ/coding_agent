# 13 LLM / Provider 層

## 問題

- 要支援多家模型（OpenAI、OpenRouter 上的各家模型），上層不想知道差異。
- 串流時 tool call 的參數是分段送來的，要正確組回。
- 不同模型的 context window、價格不同，摘要門檻與成本計算都依賴它。
- 執行中要能切換模型，subagent 也要跟著換。
- 測試不能打真的 API。

---

## 1. 介面

實作：[llm/model.go](../../llm/model.go)、[llm/message.go](../../llm/message.go)。

- `ChatModel`：`Generate(ctx, req)`、`Stream(ctx, req) iter.Seq2[Chunk, error]`、`Info()`（provider、model 名稱）、`Profile()`（`MaxInputTokens` 等）。
- 訊息由 content blocks 組成（text、reasoning、image……），保留 cache marker 等 metadata；tool call、tool call ID、status（success / error）、usage、timing 都是訊息的欄位。
- 串流用 Go 1.23 的 range-over-func 迭代器：呼叫端 `for chunk, err := range m.Stream(...)`，提前 break 就自動停止。
- 不支援串流的模型回 `ErrNotImplemented`，迴圈自動退回 `Generate`。

## 2. Provider 與 model spec

實作：[llm/registry.go](../../llm/registry.go)、[llm/providers.go](../../llm/providers.go)、[llm/openrouter.go](../../llm/openrouter.go)。

- 格式 `provider:model`，**只以第一個 `:` 切分**，所以 OpenRouter 的 slug（`openrouter:openai/gpt-6-luna-pro`）可以直接寫。
- `RegisterProvider` / `InitChatModel`：provider 註冊表，對應 Python 的 `init_chat_model`。
- OpenAI Chat Completions 經 langchaingo；OpenRouter 用同一套，換 base URL 與 key，並帶 app attribution header（`HTTP-Referer`、`X-Title`，可覆寫或設空字串不送）。
- 模型設定優先序：`-model` flag > `QUILL_MODEL` > config.toml > 預設。

## 3. 串流組裝

實作：[llm/openai_stream.go](../../llm/openai_stream.go)。

- SSE 經 tee 取出文字 delta 即時送給 UI；tool call **依 index 組回**（同一個 tool call 的 name 與 arguments 分散在多個 chunk）。
- 最後一個 chunk（`Done`）帶完整訊息，迴圈用它寫進 state，確保串流與非串流的結果一致。
- 中途停止、空串流都有測試；空回應會被重試（見 09）。

## 4. 錯誤分類

| Provider 回應 | 轉成 | 誰處理 |
|---|---|---|
| `context_length_exceeded` / `maximum context length` | `llm.ErrContextOverflow` | 摘要 middleware 強制摘要後重送（見 05） |
| 429 / 5xx / 連線錯誤 / 空回應 | 重試 | `retryDoer`（見 09） |
| 串流閒置 5 分鐘 | `ErrStreamIdle` | 往上傳 |
| 401 / 其他 400 | 原樣錯誤 | 往上傳；缺 API key 在啟動時就報錯 |

用 `errors.Is` 判斷，上層不需要知道是哪家 provider。

## 5. Profile 表：context window 與價格

- [llm/profile_table.go](../../llm/profile_table.go)：模型的 `MaxInputTokens`，以前綴比對。摘要門檻（80% window）、system prompt 裡的「Your context window is N tokens」都從這裡來。
- **不在表內的模型**假設 200k window，退回 170k 觸發摘要。（踩過坑：曾經退回 16k，導致每幾步就摘要一次，見 05。）
- 價格表內嵌，並可用 `~/.quill/prices.json` 覆寫；成本計算區分 input、output、**cache read**；未定價的模型明確標示。
- Harness profile（[profiles](../../profiles/harness.go)）可依模型調整 system prompt 前後綴、工具描述、排除特定 middleware 或工具。

## 6. 執行中切換模型（Stage 10d）

- `RunRequest.Model` 帶 spec；動態 model wrapper 從 ctx 取出本次 run 的模型（`llm.ModelForContext`），所以**同一個 agent 實例**不同 run 可以用不同模型，不用重建 agent。
- subagent、摘要、live context 的模型身分都跟著換；摘要的門檻依新模型重算（`DynamicDefaults`）。
- 每則回覆標上 `model_name`，用量與成本可以依模型分開計算（含 subagent）。
- thread 記住上次的模型；`/model --default` 寫回 config.toml。

## 7. Token 計算

[llm/tokens.go](../../llm/tokens.go) 的 `ApproximateTokenCounter`（對應 langchain `count_tokens_approximately`）：約 4 字元 1 token、每則訊息加 3、圖片固定 85。估算不準的部分由 overflow 重試兜底；實際用量以 provider 回傳的 usage 為準（成本與 quillbench 指標都用 usage）。

## 8. 推理內容（reasoning）

OpenAI / OpenRouter 文件要求 tool call 之間要把推理內容原樣回傳，否則品質與 cache 會下降。R4 先送 3 個 probe 請求（約 $0.001）：我們的模型經 Chat Completions **完全不回傳**推理內容。OpenAI 的 reasoning items 只有 Responses API 會給，那是更大的改動，等有需求再評估。訊息格式已保留 `BlockReasoning`，token 計算也會算進去。

## 9. 測試

- `FakeModel`：依序回傳預先寫好的訊息、支援串流切片（片段合起來等於全文），整個 agent 的行為測試都靠它。
- httptest 假 server：請求 JSON 形狀、純文字回應、tool_calls 回應、SSE 文字 delta 與 tool call 依 index 組回、中途停止、401、超長錯誤、重試與逾時。

## 面試講法

> 「LLM 層是一個很小的介面：Generate、Stream、Info、Profile。串流用 Go 的 range-over-func 迭代器，呼叫端 break 就自動停。provider 的錯誤統一轉成幾個 sentinel error，例如超長錯誤轉成 ErrContextOverflow，上層的摘要 middleware 用 errors.Is 判斷就能自動壓縮後重送，不需要知道是哪家 provider。執行中切換模型是透過 ctx 帶本次 run 的模型，同一個 agent 實例可以服務不同模型的 run。」

可能的追問：

- **為什麼用 langchaingo 而不是自己打 API？** Stage 2 其實先自己寫了 HTTP client，Stage 3 改接套件，減少要維護的程式；重試、閒置逾時這些可靠性邏輯則在自己的 transport 層實作。
- **為什麼 token 用估算？** 各家 tokenizer 不同，引入多個 tokenizer 成本高；估算只用來決定何時摘要，有 overflow 重試兜底。
