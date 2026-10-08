# 瀏覽器 MCP spike：Electron 內嵌面板

日期：2026-10-09。結論：**可行（go）**。Playwright MCP 與 Chrome DevTools MCP 都能透過 CDP 過濾 proxy 操作 Electron 裡的 `WebContentsView`，而且看不到 quill 自己的 UI。

## 環境

- Electron 44.7.0（Chromium 內核，`--remote-debugging-port` 綁 127.0.0.1）
- `@playwright/mcp` 0.0.83（`--cdp-endpoint`）
- `chrome-devtools-mcp` 1.10.1（`--browserUrl`，底層 Puppeteer）
- 視窗內兩個 `WebContentsView`：GUI（模擬 quill UI）與面板（獨立 session partition）

## 結果

| 情境 | Playwright MCP | Chrome DevTools MCP |
|---|---|---|
| 不加 proxy | 能操作，但**預設選中 GUI 分頁並把它導航走** | 能操作，但**預設選中 GUI 頁** |
| 加 proxy：列分頁 | 只看到面板 | 只看到面板 |
| navigate / snapshot / click | 成功 | 成功 |
| 開新分頁 | proxy 改寫成在面板內導航，成功 | 同左，成功 |
| 截圖 | 成功（PNG） | 成功（PNG） |

不加 proxy 的結果證實風險存在：agent 會直接操作 quill 自己的介面。

## 發現

1. **Electron 不支援 `Target.createTarget`**（回 `Not supported`）。兩種 MCP 的「開新分頁」都會失敗，必須由 proxy 改寫成「面板導航到該網址，回傳面板的 targetId」。
2. **新版 Chromium 每個 page 外面還有一層 `tab` target**。只用 page 的 targetId 過濾會把面板的 tab 丟掉，Puppeteer 因此卡住；同時 GUI 的 tab 被放行，再透過它 attach 到 GUI page，形成漏洞。
3. **用 `browserContextId` 過濾才正確**。面板用獨立 session partition，它的 tab、page、popup 都屬於同一個 browser context，跟 GUI 不同。proxy 只放行這個 context 的 target，tab 與 page 的關係就不用追。
4. **沒有渲染幀時 Playwright 的 click 會逾時**：它要用 `requestAnimationFrame` 確認元素「穩定」。Stage 4 實測發現，即使面板可見，每次導航後換上的新 renderer widget 都停在 hidden 狀態，rAF 為 0（`document.visibilityState` 仍是 `visible`，因為關了 background throttling，不能拿來判斷）；CDP 截圖會暫時喚醒，所以有截過圖時看起來正常。解法是面板 `did-navigate` 時再呼叫一次 `webContents.setBackgroundThrottling(false)`，Electron 會把 hidden 的 widget 重新 show。停用 `MacWebContentsOcclusion`、延後顯示視窗都沒有效果。
5. Playwright MCP 新版 `browser_click` 參數是 `target`（不是 `ref`）；Chrome DevTools MCP 1.10 的工具都要求 `pageId`。這些對模型是透明的（看 schema），但寫測試要注意。
6. Playwright MCP 預設把快照與 console log 寫到工作目錄的 `.playwright-mcp/`，preset 要用 `--output-dir` 指到暫存目錄，避免汙染專案。

## proxy 規則（Stage 4 實作依據）

- HTTP：`/json/version` 與 `/json/list` 的 WebSocket 位址改寫成 proxy；`/json/list` 只留面板 page。
- 連線建立時 proxy 先用自己的請求 `Target.getTargets` 建立 targetId → browserContextId 對照表，之後從事件持續更新。
- 客戶端 → 上游：
  - `sessionId` 不是經由允許的 attach 取得的，拒絕。
  - 參數帶 `targetId` 而該 target 不在面板 context，拒絕。
  - `Target.createTarget` 改寫成面板導航；`createBrowserContext` / `disposeBrowserContext` 拒絕。
- 上游 → 客戶端：
  - 帶 `targetInfo` 的事件，不在面板 context（`browser` 類型除外）就丟掉。
  - 不屬於允許 session 的訊息丟掉。
  - `Target.getTargets` 結果過濾；`Target.getBrowserContexts` 改寫成只有面板 context（含 `defaultBrowserContextId`）。

攻擊測試（直接 attach / activate / close GUI target、偽造 sessionId、全域 auto-attach）全部被拒，事件流中沒有出現 GUI 標題。
