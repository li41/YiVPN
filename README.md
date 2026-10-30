# 自訂品牌的 Tailscale Windows GUI

取代官方 Tailscale Windows 用戶端的整套安裝：系統匣 GUI 的品牌可以自訂，選單結構照官方 Windows 版；底層的 tailscaled 由 Tailscale 原始碼自己編譯，Windows 看得到的「Tailscale」名稱全部換成 `appID`，也**不安裝官方 MSI**，所以「已安裝的應用程式」、服務、網路介面卡、工作管理員裡都只會看到你的產品。

## 架構

```
C:\Program Files\<dirName>\
  MyVPN.exe                GUI（每位使用者登入時啟動）＋安裝程式＋更新器，內含下面兩個檔案
  <appID>-service.exe      改名後的 tailscaled（自己編譯）
  <appID>-cli.exe          改名後的 tailscale 指令（自己編譯；安裝資料夾會加進系統 PATH）
  wintun.dll               官方 Wintun 驅動（WireGuard LLC 簽章，未修改）
  THIRD-PARTY-NOTICES.txt  Tailscale 與 Wintun 的授權聲明
  （install.extraFiles）   例如 Yi.exe，同樣包在 MyVPN.exe 裡

C:\ProgramData\<appID>\    登入狀態與服務記錄檔（只有 SYSTEM 和 Administrators 能讀）

Windows 服務「<appID>」（顯示名稱可自訂）→ <appID>-service.exe
網路介面卡「<appID>」
系統排程「<appID> Updater」（以 SYSTEM 執行）→ MyVPN.exe --update
```

| 需求 | 做法 |
|---|---|
| 「已安裝的應用程式」不出現 Tailscale | 不安裝官方 MSI，由 `<appID>-service.exe install-system-daemon` 自己註冊服務 |
| 到處都不出現 Tailscale | `build.cmd` 複製 go.mod 指定版本的 Tailscale 原始碼，把服務名稱、網路介面卡、named pipe、ProgramData 資料夾、系統原則機碼、防火牆規則、hosts 區段等名稱改成 `appID`（見 `tools/build/patch.go`），再編譯 |
| tailscaled 自動更新 | tailscaled 包在 GUI 的 EXE 裡，GUI 更新時一起更新：新版 GUI 比對 SHA-256，不同就停服務、替換、再啟動；新版啟動失敗會自動還原舊版。tailscaled 內建的更新器在編譯時就拿掉了（它只會裝官方 MSI） |
| VPN 網卡不開 NetBIOS | 每次服務啟動、網卡拿到 IP 之前，把它的 NetBIOS over TCP/IP 停用，tailnet 上的其他裝置連不到這台的 137–139 埠。官方 tailscaled 也想這麼做，但那段程式實際上不會執行；Windows 每次建立網卡都會把設定重設，所以要在指派 IP 前寫入 |
| 由 GUI 觸發更新 | 有設 `update.baseURL` 時，選單有「Check for updates」，按下去會直接啟動上面的排程。排程開放一般使用者觸發，**不用 UAC** |
| 使用習慣不變 | 選單文字與結構照官方：帳號切換、This device、Network devices、Exit nodes、Preferences、Admin console… 也會遵守官方的系統原則（機碼改成 `HKLM\SOFTWARE\Policies\<appID>`） |
| 名稱與圖示可自訂 | 全部寫在 `branding.jsonc`，打包時寫進 EXE |

## 打包

需要 Go 1.27 以上，第一次打包要能連網（下載 Tailscale 的相依套件與 Wintun）。

1. 編輯 `branding.jsonc`（每一欄都有中文註解）。
2. 把圖示換成你自己的 `.ico`，放在 `assets\`（或改設定檔裡的路徑）。
3. 執行 `build.cmd`，產出：
   - `dist\<exeName>`：發給使用者的安裝程式，同時也是 GUI 本體（內含改名後的 tailscaled）
   - `dist\latest.json`：自動更新用的版本資訊

`_build\` 是打包時的暫存資料夾（Tailscale 原始碼複本、編譯結果），可以隨時刪掉。

### 升級 Tailscale

tailscaled 的版本就是 `go.mod` 裡 `tailscale.com` 的版本：

```bash
go get tailscale.com@latest
```

```bash
go mod tidy
```

再調高 `branding.jsonc` 的 `version`、執行 `build.cmd`、發布。Tailscale 改了被修補的程式時，打包會停在「修補 …：預期找到 N 處」，照訊息調整 `tools/build/patch.go` 即可，不會產出改名不完整的版本。

不過 Tailscale 在**新的地方**加上「Tailscale」名稱時，打包不會發現。升級後請比對新舊兩版原始碼，確認沒有新增 Windows 看得到的名稱（例如服務、網卡、登錄機碼、檔案路徑、防火牆規則）。

有程式碼簽章憑證的話，打包時一起簽（工具會先簽章再計算 `latest.json` 的 SHA-256）：

```bash
build.cmd -sign "signtool sign /a /fd sha256 /tr http://timestamp.digicert.com /td sha256"
```

### 設定檔對應到 Windows 哪裡

| 設定 | 出現在 |
|---|---|
| `exeName` | 工作管理員「詳細資料」頁、安裝資料夾裡的檔名 |
| `fileDescription` | 工作管理員「處理程序」頁與「啟動應用程式」頁、工作列「其他系統匣圖示」設定 |
| `productName` | 系統匣滑鼠提示、選單、「關於」視窗 |
| `companyName`、`copyright`、`version` | EXE 內容 → 詳細資料 |
| `install.displayName`、`install.publisher` | 設定 → 應用程式 → 已安裝的應用程式 |
| `install.dirName` | `C:\Program Files\<dirName>`、記錄檔資料夾 |
| `install.startMenuName` | 開始功能表捷徑（所有使用者） |
| `install.extraFiles` | 一起放進安裝資料夾的其他檔案（例如 `Yi.exe`）；跟著 GUI 更新，解除安裝時刪除（執行中的 .exe 會先被關閉） |
| `install.serviceDisplayName`、`install.serviceDescription` | 「服務」清單、工作管理員「服務」頁；顯示名稱也是 `<appID>-service.exe` 在「處理程序」頁的名稱 |
| `appID` | 系統排程名稱「<appID> Updater」、登錄機碼；服務內部名稱、網路介面卡、`<appID>-service.exe`、`C:\ProgramData\<appID>`、系統原則機碼、防火牆規則 |
| `icons.app` | EXE、已安裝的應用程式清單、開始功能表 |
| `icons.connected` 等四個 | 系統匣在各種連線狀態下的圖示 |

## 使用者端行為

- **雙擊 EXE**：確認後跳一次 UAC，安裝到 `C:\Program Files\<dirName>`，並部署內建的 tailscaled（不需連網）、註冊服務、開機啟動、開始功能表、「已安裝的應用程式」與更新排程，最後以目前使用者身分啟動 GUI。
- **電腦上已裝官方 Tailscale**：安裝時會詢問是否改由本產品管理，同意後移除官方 MSI，並把 `C:\ProgramData\Tailscale` 的登入狀態複製到 `C:\ProgramData\<appID>`（不用重新登入）。
- **從舊版（使用官方 tailscaled、服務名稱「Tailscale」）升級**：會移除舊服務與 `tailscaled.exe`、`tailscale.exe`，同樣複製登入狀態。
- **靜默安裝**（IT 部署）：`MyVPN.exe --quiet`（仍需系統管理員權限）
- **解除安裝**：從「已安裝的應用程式」移除，或執行 `MyVPN.exe --uninstall [--quiet]`。會移除服務、排程、檔案和登錄項目；`C:\ProgramData\<appID>` 裡的登入狀態會保留，重新安裝後不用重新登入。
- **開發測試**：`MyVPN.exe --run-here` 不安裝，直接在原地執行 GUI。
- **記錄檔**：
  - GUI：`%LOCALAPPDATA%\<dirName>\<appID>.log`
  - 安裝／更新：`%ProgramData%\<dirName>\install.log`、`updater.log`
  - 服務：`%ProgramData%\<appID>\Logs\`

## 自動更新

1. 修改 `branding.jsonc` 的 `version`，執行 `build.cmd`。
2. 把 `dist\` 裡的 EXE 和 `latest.json` 上傳到 `update.baseURL` 指向的位置。
   - 用 GitHub Releases：`baseURL` 填 `https://github.com/<owner>/<repo>/releases/latest/download`，每次發布把這兩個檔案附加到 Release。
3. 系統排程發現新版時會下載、比對 SHA-256（有設 `signerSubject` 時也驗證簽章），然後替換執行檔，再由新版部署它內建的 tailscaled。各使用者的 GUI 發現執行檔被換掉後會自動重啟成新版。

更新器以 SYSTEM 身分執行，所以 `baseURL` 的內容等同擁有這台電腦的最高權限。**正式發布時務必幫 EXE 做程式碼簽章並設定 `signerSubject`**，只靠 SHA-256 的話，能改 `baseURL` 上檔案的人就能推送任意程式。

## 限制

- **Tailscale 的安全修補要自己跟上**：tailscaled 不再從 Tailscale 自動更新，官方發布新版時要照「升級 Tailscale」重新打包發布，不然使用者會停在舊版。
- **沒有官方簽章**：`<appID>-service.exe` 是自己編譯的，沒有 Tailscale 的數位簽章。以 SYSTEM 執行的未簽章程式比較容易被防毒軟體或 SmartScreen 攔下，正式發布建議用自己的程式碼簽章憑證簽 GUI 的 EXE。
- **服務本身還是 Tailscale**：登入頁面、管理主控台（`adminConsoleURL`）、MagicDNS 的 `*.ts.net` 網域、服務記錄檔與錯誤訊息的內文，都還是 Tailscale 的名稱。改的是 Windows 上看得到的名稱，不是背後的服務。
- **CLI 的文字還是 Tailscale**：`<appID>-cli.exe` 就是 `tailscale.exe` 改名，預設連到本產品的服務，但說明與輸出的文字（例如「Tailscale is stopped」）沒有改。用法跟官方一樣，例如 `<appID>-cli status`。安裝時把安裝資料夾加進系統 PATH，解除安裝時移除；已經開著的命令提示字元要重開才看得到。
- 舊版官方 tailscaled 留下的 `C:\ProgramData\Tailscale` 不會刪除（登入狀態已複製過去）。
- 官方 MSI 另外會做的事這裡沒做：設定程序防護（process mitigation）原則。
- 選單文字是英文，跟官方一樣；要翻譯的話改 `internal/tray/text.go`。複製 IP 之後不會跳通知。

## 程式結構

```
branding.jsonc           品牌設定（使用者編輯）
assets/                  預設圖示（go run ./tools/genicons 產生，可換掉）
tools/build/             打包工具：讀設定 → 產生資源 → 修補並編譯 tailscaled → 編譯 GUI（→ 簽章）
tools/build/patch.go     tailscaled 原始碼裡要改名的地方
cmd/tray/                程式進入點：GUI／安裝／解除安裝／更新器
internal/brand/          設定檔格式與檢查、由 appID 推導的服務名稱等
internal/brand/res/      打包時寫入的品牌資料（嵌入 EXE）
internal/tray/           系統匣選單（參考 tailscale.com/client/systray，BSD-3-Clause）
internal/tsdeploy/       部署與更新內建的 tailscaled（bundle/ 由打包工具產生，嵌入 EXE）
internal/install/        整台電腦的安裝、解除安裝、更新流程
internal/task/           SYSTEM 更新排程
internal/update/         GUI 自我更新（下載、驗證、替換）
internal/winx/           Win32 小工具
```
