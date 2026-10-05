# quill GUI：下一輪優先順序

目前 GUI 已有 timeline、tool 詳情、LLM input/output、單次檔案 diff、核准卡片與模型／用量指令。本文件只列出日常 coding agent 工作仍欠缺的 GUI 能力。

## 已完成：thread 可接續與命名

- 透過 `~/.quill/.env` 的 `QUILL_POSTGRES_CONN_STRING`，將 checkpoint 與 thread 清單保存在本機 Docker PostgreSQL。
- 重啟 GUI 自動打開目前目錄最近有 checkpoint 的 thread，保留 `-thread` 與 `-r`。
- 側欄可以更改已開始的 thread 名稱；名稱會跨重啟保留。
- 執行中禁止從側欄切換 thread；切換時只採用最新一次載入的結果。

## P1：長任務可靠性

讓 run 由 server 持有生命週期，GUI 只是訂閱者。頁面重新整理或關閉後，重新開啟仍能看到該 thread 正在執行、等待核准、已完成或失敗，並可接回事件串流、取消或繼續。要有明確的 run ID、狀態 API 和事件游標；同一 thread 跨行程只允許一個有效 run。

## P1：完整的改動審查與回復

目前只看得到個別 tool call 的 diff。新增整個 thread 的 Files changed 面板：依檔案看目前 Git 工作樹與基準的差異、測試結果和代理修改來源。提供明確的檔案回復操作；回復前偵測使用者或其他工具之後的修改，避免覆蓋手動工作。Git commit／PR 可在此基礎上再加。

## P2：thread 管理

支援依名稱、路徑與內容搜尋歷史 thread，依專案分組；可封存與還原，刪除先進可恢復的垃圾桶。大量 thread 時加入分頁／虛擬列表。歷史清單不應固定只顯示前 50 筆。

## P2：輸入與工作區界線

輸入框支援挑選檔案、貼入圖片／附檔與 `@` 引用，送出前清楚顯示會帶入哪些內容。新 thread 可選擇專案與 Git worktree，明確顯示分支和未提交改動，讓平行任務不共用同一份工作樹。
