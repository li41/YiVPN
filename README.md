# 自訂品牌的 Tailscale Windows GUI

取代官方 Tailscale Windows 用戶端的整套安裝：系統匣 GUI 的品牌可以自訂，選單結構照官方 Windows 版；底層用 Tailscale 官方簽章的 tailscaled，但**不安裝官方 MSI**，所以「已安裝的應用程式」裡只會看到你的產品。

## 架構

```
C:\Program Files\<dirName>\
  MyVPN.exe        GUI（每位使用者登入時啟動）＋安裝程式＋更新器
  tailscaled.exe   ┐
  tailscale.exe    ├ 從官方 MSI 解出的原始檔案，保留 Tailscale 的數位簽章
  wintun.dll       ┘

Windows 服務「Tailscale」（顯示名稱可自訂）→ tailscaled.exe
系統排程「<appID> Updater」（以 SYSTEM 執行）→ MyVPN.exe --update
```

| 需求 | 做法 |
|---|---|
| 「已安裝的應用程式」不出現 Tailscale | 不安裝官方 MSI，只用 `msiexec /a` 解出檔案（不會向 Windows 註冊），再由 tailscaled 自己註冊服務 |
| tailscaled 自動更新 | 系統排程定期向 pkgs.tailscale.com 查最新版，下載後驗證 Tailscale 官方簽章，停服務、替換檔案、再啟動；新版啟動失敗會自動還原舊版。tailscaled 內建的更新器會被系統原則關閉（它只會裝 MSI） |
| 由 GUI 觸發更新 | 選單出現「Update available」時按下去，會直接啟動上面的排程。排程開放一般使用者觸發，**不用 UAC** |
| 使用習慣不變 | 選單文字與結構照官方：帳號切換、This device、Network devices、Exit nodes、Preferences、Admin console… 也會遵守官方的系統原則 |
| 名稱與圖示可自訂 | 全部寫在 `branding.jsonc`，打包時寫進 EXE |

## 打包

需要 Go 1.27 以上。

1. 編輯 `branding.jsonc`（每一欄都有中文註解）。
2. 把圖示換成你自己的 `.ico`，放在 `assets\`（或改設定檔裡的路徑）。
3. 執行 `build.cmd`，產出：
   - `dist\<exeName>`：發給使用者的安裝程式，同時也是 GUI 本體
   - `dist\latest.json`：GUI 自動更新用的版本資訊

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
| `install.serviceDisplayName`、`install.serviceDescription` | 「服務」清單、工作管理員「服務」頁 |
| `appID` | 系統排程名稱「<appID> Updater」、登錄機碼 |
| `icons.app` | EXE、已安裝的應用程式清單、開始功能表 |
| `icons.connected` 等四個 | 系統匣在各種連線狀態下的圖示 |

## 使用者端行為

- **雙擊 EXE**：確認後跳一次 UAC，安裝到 `C:\Program Files\<dirName>`，並下載部署 tailscaled、註冊服務、開機啟動、開始功能表、「已安裝的應用程式」與更新排程，最後以目前使用者身分啟動 GUI。
- **電腦上已裝官方 Tailscale**：安裝時會詢問是否改由本產品管理，同意後移除官方 MSI（登入狀態保留）。
- **靜默安裝**（IT 部署）：`MyVPN.exe --quiet`（仍需系統管理員權限）
- **解除安裝**：從「已安裝的應用程式」移除，或執行 `MyVPN.exe --uninstall [--quiet]`。會移除服務、排程、檔案和登錄項目；`C:\ProgramData\Tailscale` 裡的登入狀態會保留，重新安裝後不用重新登入。
- **開發測試**：`MyVPN.exe --run-here` 不安裝，直接在原地執行 GUI。
- **記錄檔**：
  - GUI：`%LOCALAPPDATA%\<dirName>\<appID>.log`
  - 安裝／更新：`%ProgramData%\<dirName>\install.log`、`updater.log`

## GUI 自動更新

1. 修改 `branding.jsonc` 的 `version`，執行 `build.cmd`。
2. 把 `dist\` 裡的 EXE 和 `latest.json` 上傳到 `update.baseURL` 指向的位置。
   - 用 GitHub Releases：`baseURL` 填 `https://github.com/<owner>/<repo>/releases/latest/download`，每次發布把這兩個檔案附加到 Release。
3. 系統排程發現新版時會下載、比對 SHA-256（有設 `signerSubject` 時也驗證簽章），然後替換執行檔。各使用者的 GUI 發現執行檔被換掉後會自動重啟成新版。

更新器以 SYSTEM 身分執行，所以 `baseURL` 的內容等同擁有這台電腦的最高權限。**正式發布時務必幫 EXE 做程式碼簽章並設定 `signerSubject`**，只靠 SHA-256 的話，能改 `baseURL` 上檔案的人就能推送任意程式。

## 限制

- 服務的內部名稱固定是「Tailscale」（tailscaled 寫死的），能改的只有顯示名稱與描述。
- `tailscaled.exe` 是官方簽章檔案，工作管理員「處理程序」頁會顯示它的描述「Tailscale service」，網路介面卡名稱也是「Tailscale」。要連這些都改，就得自己編譯 tailscaled。
- 這不是 Tailscale 官方支援的部署方式。如果日後官方安裝包增加了必要的檔案或步驟，部署程式會因為「MSI 裡找不到某個檔案」而停止更新（舊版會繼續運作），需要跟著調整 `internal/tsdeploy`。
- 官方 MSI 另外會做的事這裡沒做：把安裝資料夾加進 PATH、設定程序防護（process mitigation）原則。
- 選單文字是英文，跟官方一樣；要翻譯的話改 `internal/tray/text.go`。複製 IP 之後不會跳通知。

## 程式結構

```
branding.jsonc           品牌設定（使用者編輯）
assets/                  預設圖示（go run ./tools/genicons 產生，可換掉）
tools/build/             打包工具：讀設定 → 產生資源 → 編譯（→ 簽章）
cmd/tray/                程式進入點：GUI／安裝／解除安裝／更新器
internal/brand/          設定檔格式與檢查
internal/brand/res/      打包時寫入的品牌資料（嵌入 EXE）
internal/tray/           系統匣選單（參考 tailscale.com/client/systray，BSD-3-Clause）
internal/tsdeploy/       不經 MSI 部署與更新官方 tailscaled
internal/install/        整台電腦的安裝、解除安裝、更新流程
internal/task/           SYSTEM 更新排程
internal/update/         GUI 自我更新（下載、驗證、替換）
internal/winx/           Win32 小工具
```
