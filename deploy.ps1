# 部署:推源码 → 服务器上编译 → 重启服务(编译在服务器,不在开发机)
# 用法: .\deploy.ps1   (srv profile 与远端路径可用环境变量覆盖)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$P = if ($env:MAILWATCH_PROFILE) { $env:MAILWATCH_PROFILE } else { "网站服务" }
$R = if ($env:MAILWATCH_REMOTE) { $env:MAILWATCH_REMOTE } else { "/mnt/project/mailwatch" }

# 推 git 跟踪的全部文件(先建远端目录)
$files = git ls-files
$dirs = $files | ForEach-Object { Split-Path $_ -Parent } | Where-Object { $_ } |
    ForEach-Object { $_ -replace '\\','/' } | Sort-Object -Unique
$mkdir = ($dirs | ForEach-Object { "$R/src/$_" }) -join ' '
if ($mkdir) { srv -P $P "mkdir -p $mkdir" }
foreach ($f in $files) {
    srv -P $P push $f "$R/src/$($f -replace '\\','/')"
}

srv -P $P "cd $R/src && go build -o $R/mailwatch.new . && mv $R/mailwatch.new $R/mailwatch && cp mailwatch.service /etc/systemd/system/ && systemctl daemon-reload && systemctl restart mailwatch && sleep 1 && systemctl is-active mailwatch"
