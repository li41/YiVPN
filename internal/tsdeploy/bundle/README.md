build.cmd（tools/build）會在這裡寫入改名後的 tailscaled、wintun.dll 與授權聲明（gzip 壓縮），以及列出它們的 manifest.json。這些檔案會嵌入 GUI 的 EXE，不放進 git。
