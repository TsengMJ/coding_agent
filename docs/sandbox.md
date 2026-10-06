# Shell 沙箱：使用與設定

quill 讓模型執行的 shell 指令（`execute`、`run_tests`、`diagnostics`）跑在 OS 層沙箱裡：macOS 用 Seatbelt（`sandbox-exec`），Linux 用 bubblewrap（`bwrap`）。quill 本身不進沙箱（它要連 LLM、Postgres、Redis），只包住它啟動的指令。設計與取捨見 [intros/08 §12](intros/08_safety_and_permissions.md#12-os-層-process-sandbox)。

## 它擋什麼

| | `workspace-write`（預設） | `read-only` | `full-access` |
|---|---|---|---|
| 寫專案目錄 | 可以，但 `.git/hooks`、`.git/config`、`.quill/` 不行 | 不行 | 可以 |
| 寫暫存目錄、建置 cache | 可以 | 不行 | 可以 |
| 寫其他地方（家目錄、系統） | 不行 | 不行 | 可以 |
| 讀 credential（`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、`~/.config/gh`、`~/.config/gcloud`、`~/.azure`、`~/.netrc`、`~/.git-credentials`、`~/.docker/config.json`、`~/.quill/.env`、docker socket） | 不行 | 不行 | 可以 |
| 讀其他檔案 | 可以 | 可以 | 可以 |
| 網路 | 依 `network` | 依 `network` | 不限制 |

建置 cache 指：系統 cache 目錄（macOS `~/Library/Caches`、Linux `~/.cache`）、`~/.npm`、`$GOCACHE`、`$GOMODCACHE`、`$GOPATH/pkg/mod`、`$GOPATH/pkg/sumdb`。其他工具的 cache（`~/.cargo`、`~/.m2`、`~/.gradle` 等）要自己加到 `writable_roots`。

`.git/hooks` 與 `.git/config` 不可寫，是因為寫進去的 hook 會在你下次 `git commit` 時在沙箱外執行。`read-only` 模式下，`write_file`、`edit_file`、`apply_patch`、`delete` 這些檔案工具也一併拒絕。

網路（`network`）：

- `none`（預設）：只能連 localhost，所以測試裡啟動的本機 server 照常可用。
- `allowlist`：只能透過 quill 內建的 HTTP proxy 連到允許的網域（見[網路 allowlist](#網路-allowlist)）。
- `full`：不限制。

## 快速開始

### macOS

不用任何設定，沙箱預設開啟（`sandbox-exec` 是系統內建）。

### Linux

```sh
sudo apt install bubblewrap        # Fedora: sudo dnf install bubblewrap
```

bubblewrap 需要 unprivileged user namespace。多數發行版預設允許；Ubuntu 24.04 以後被 AppArmor 限制時，quill 啟動會失敗並說明，解法見[常見問題](#常見問題)。

### 確認有生效

在專案目錄執行：

```sh
quill sandbox -- touch ~/x                       # 應該失敗
quill sandbox -- cat ~/.ssh/config               # 應該失敗
quill sandbox -- curl -sS https://example.com    # 應該失敗（network = none）
quill sandbox -- go test ./...                   # 應該成功
quill sandbox -print-policy -- true              # 印出實際規則
```

`quill sandbox` 用的就是 agent 執行指令時的同一套規則，所以這裡能跑的，agent 也能跑。被擋時 macOS 通常顯示 `Operation not permitted`，Linux 顯示 `Read-only file system` 或 `Permission denied`。

## 在 GUI、TUI 與腳本中使用

沙箱設定和介面無關，GUI、TUI、`-n` 都一樣：

```sh
quill                               # GUI（預設的互動介面）
quill -tui                          # 終端介面
quill -n '跑測試並修好'              # 單次執行
quill -sandbox read-only            # 這次只讀，任何介面都可以加
QUILL_SANDBOX_MODE=read-only quill  # 同上，用環境變數
```

常駐 server 的用法，沙箱由 server 端決定：

```sh
quill serve -sandbox workspace-write    # server 讀 ~/.quill/config.toml，-sandbox 覆寫 mode
quill -server http://127.0.0.1:8123     # 客戶端的 -sandbox 不會生效
```

### 和核准的關係

- 沙箱開啟時，`execute`、`run_tests`、`diagnostics` 不再逐一詢問，因為沙箱已經限制了它們能做的事（對標 Claude Code 的 `autoAllowBashIfSandboxed`）。
- 破壞性指令（`rm -rf ~`、`git reset --hard`、`git push --force` 等）仍一律要核准，就算用了 `-y`。
- 寫檔工具（`write_file`、`edit_file`、`apply_patch`、`delete`）依原本的核准設定（`startup.mode`）。

### 指令被擋時

指令輸出看起來像被沙箱擋下（權限錯誤、唯讀檔案系統、DNS 失敗、proxy 回 403）時，工具結果會附上一段 `[sandbox]` 說明，讓模型知道原因、改用沙箱內可行的做法，而不是一直重試。GUI 展開那個 execute 就看得到。

如果真的需要在沙箱外執行，模型可以對 `execute` 傳 `"sandbox": false`。這個請求**一律要你核准**，核准框會寫明 `OUTSIDE the sandbox (full access to this machine and the network)`。

- `-n` 加自動核准（沒有人能核准）時，這類請求直接被拒絕。
- `allow_unsandboxed = false` 完全關掉這個逃生口。

trace 會記錄每個 `execute` 的沙箱模式（沙箱外執行的記為 `unsandboxed`）與是否疑似被擋。

### 在 GUI 看指令有沒有跑在沙箱裡

`execute`、`run_tests`、`diagnostics` 展開後，Output 旁有沙箱 badge：

- `sandbox: workspace-write`（或其他模式）：在沙箱內執行。
- 紅色 `unsandboxed`：經你核准後在沙箱外執行。
- 紅色 `sandbox blocked`：失敗訊息看起來是被沙箱擋下（只有 `execute` 會判斷）。

收合的工具列只在 `unsandboxed` 或 `sandbox blocked` 時於右側加註，一般沙箱內執行不另外標示。沒有 badge 代表沙箱沒有開啟（`full-access`）。

## 設定

沙箱只能在 `~/.quill/config.toml` 的 `[sandbox]` 設定；專案目錄裡的任何設定都改不了它，避免 clone 下來的 repo 自己把沙箱關掉。優先順序：flag > 環境變數 > config.toml > 預設。格式錯誤時 quill 啟動失敗並指出哪個欄位。

```toml
[sandbox]
mode = "workspace-write"        # read-only / workspace-write / full-access
network = "none"                # none / allowlist / full
allowed_domains = []            # allowlist 在內建清單之外額外放行的網域
writable_roots = []             # workspace-write 額外可寫的目錄
deny_read = []                  # 額外不可讀的路徑
on_unavailable = "error"        # 沙箱無法使用時：error（拒絕啟動）/ warn
allow_unsandboxed = true        # 允許模型請求（經核准）在沙箱外執行
```

| 設定 | 預設 | 說明 |
|---|---|---|
| `mode` | macOS、Linux `workspace-write`；其他平台 `full-access` | 也可用 `-sandbox`（`quill`、`quill serve`）或 `QUILL_SANDBOX_MODE` 覆寫 |
| `network` | `none` | `quill sandbox -network` 只影響那個試跑指令 |
| `allowed_domains` | 空 | `example.com` 只比對該網域；`*.example.com` 比對所有子網域，但不含 `example.com` 本身 |
| `writable_roots` | 空 | 必須是絕對路徑或 `~/` 開頭；只在 `workspace-write` 有效 |
| `deny_read` | 空 | 必須是絕對路徑或 `~/` 開頭；疊加在內建的 credential 清單上 |
| `on_unavailable` | `error` | `warn` 時指令不受限執行，且每個指令結果都帶一行 `[stderr] warning: not sandboxed: <原因>` |
| `allow_unsandboxed` | `true` | `false` 時模型不能請求在沙箱外執行 |

### 常用設定範例

讓 agent 能安裝套件：

```toml
[sandbox]
network = "allowlist"
```

另外需要 GitHub release 下載、raw 檔案或 Go 直接抓 GitHub 原始碼：

```toml
[sandbox]
network = "allowlist"
allowed_domains = ["*.githubusercontent.com", "codeload.github.com"]
```

Rust、Java 專案的建置 cache：

```toml
[sandbox]
writable_roots = ["~/.cargo", "~/.m2", "~/.gradle"]
```

只想讓 agent 讀程式、回答問題、不准改任何東西：

```toml
[sandbox]
mode = "read-only"
```

需要連公司內網的服務（內部 registry、資料庫）：allowlist 一律拒絕內網位址，只能放寬網路，或改用逃生口逐次核准。

```toml
[sandbox]
network = "full"
```

完全關掉沙箱（不建議；shell 指令可以存取整台機器）：

```toml
[sandbox]
mode = "full-access"
```

## 網路 allowlist

`network = "allowlist"` 時，quill 在自己的行程裡啟動一個 HTTP proxy，只轉送到允許的網域，並把 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`（大小寫兩種）注入指令的環境；`NO_PROXY` 設為 `localhost,127.0.0.1,::1`，本機服務不經 proxy。

內建允許的網域：`proxy.golang.org`、`sum.golang.org`、`github.com`、`registry.npmjs.org`、`pypi.org`、`files.pythonhosted.org`，`allowed_domains` 是在這之上額外放行。

- 會讀這些環境變數的工具都能用：`go`、`npm`、`pip`、`git`（https）、`curl` 等。
- 不支援 HTTP proxy 的連線一律不通，例如 SSH（`git@github.com:...` 要改用 https 網址）、直接連資料庫。
- 網域解析出的位址如果是內網、loopback、link-local（含雲端 metadata `169.254.169.254`），proxy 一律拒絕；連線釘在檢查過的位址上，防 DNS rebinding。
- 本機開著 Clash、Surge 等 TUN 模式的 proxy 工具時，網域會被解析到 `198.18.0.0/15`；這段不視為內網，allowlist 照常運作。
- 被拒絕時，`curl` 顯示 `CONNECT tunnel failed, response 403`，`pip` 顯示 `Tunnel connection failed: 403 Forbidden`，`go` 顯示 `Forbidden`；代表網域不在清單內。

運作方式：macOS 的沙箱允許連 localhost，指令直接連 proxy 的 port。Linux 的沙箱有獨立的 network namespace，連不到主機的 localhost，所以指令外層包一個 `quill __netbridge`，在 namespace 內聽同一個 port，經由 unix socket 轉到 proxy。

## 常見問題

**Linux 啟動失敗：`bubblewrap (bwrap) not found`**
安裝 bubblewrap（`sudo apt install bubblewrap`）。

**Linux 啟動失敗：`cannot create namespaces`**
系統不允許 unprivileged user namespace：

```sh
# Ubuntu 24.04 以後（AppArmor 限制）
sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0
# Debian 舊版
sudo sysctl kernel.unprivileged_userns_clone=1
```

要永久生效，寫進 `/etc/sysctl.d/`。不能改系統設定時，設 `mode = "full-access"`（關掉沙箱）或 `on_unavailable = "warn"`（不受限執行並在每個結果警告）。

**在 Docker 容器裡跑 quill**
Docker 預設的 seccomp profile 與被拿掉的 capability 會讓 bubblewrap 無法建立 namespace，容器要用 `--privileged` 啟動；否則容器本身就是隔離層，可以設 `mode = "full-access"`。

**建置工具寫入失敗（`Operation not permitted` / `Read-only file system`）**
用 `quill sandbox -print-policy -- true` 看可寫的目錄。工具的 cache 不在清單裡時，加到 `writable_roots`。

**`go`、`npm`、`pip` 下載失敗**
預設 `network = "none"`，改成 `allowlist`。已經是 allowlist 卻失敗時，看錯誤訊息裡的網域，加到 `allowed_domains`。

**allowlist 的網域全部 403**
用 `quill sandbox -network allowlist -- curl -sS https://proxy.golang.org/` 測。網域解析到內網位址會被拒絕；公司 proxy 或 DNS 把公開網域解析到內網時，allowlist 無法使用，請改用 `network = "full"`。

**`git commit` 失敗**
沙箱允許 git commit、stash、reset；會失敗通常是想改 `.git/config`（例如 `git config user.name`）或寫 hook，這兩個刻意不可寫。用 `git -c user.name=... commit` 或由你自己設定。

**Linux 上被禁止讀的目錄看起來是空的**
bubblewrap 用空的 tmpfs 蓋掉這些目錄，macOS 則是直接拒絕。兩者都讀不到內容。

## 限制

- 「被沙箱擋下」是看輸出關鍵字判斷，可能漏判或誤判。
- macOS 的沙箱允許連任何 localhost port，主機上的本機服務在沙箱內也連得到。
- Linux 上，不存在的 deny-write 路徑（例如還沒有 `.git/hooks`）無法預先擋住；檔案工具仍會拒絕寫入這些路徑。
- Windows 沒有沙箱，預設 `full-access`。
- 檔案工具的沙箱對應（`read-only` 拒絕寫入、保護 `.git/hooks`）只套用在主 agent。
