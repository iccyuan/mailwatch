# 部署:推源码 → 服务器上编译 → 重启服务(编译在服务器,不在开发机)
# 用法: .\deploy.ps1   (srv profile 与远端路径可用环境变量覆盖)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$P = if ($env:MAILWATCH_PROFILE) { $env:MAILWATCH_PROFILE } else { "网站服务" }
$R = if ($env:MAILWATCH_REMOTE) { $env:MAILWATCH_REMOTE } else { "/mnt/project/mailwatch" }

foreach ($f in @("go.mod","go.sum","config.example.toml","mailwatch.service","README.md") + (Get-ChildItem *.go).Name) {
    srv -P $P push $f "$R/src/$f"
}
srv -P $P push web\index.html "$R/src/web/index.html"

srv -P $P "cd $R/src && go build -o $R/mailwatch.new . && mv $R/mailwatch.new $R/mailwatch && cp mailwatch.service /etc/systemd/system/ && systemctl daemon-reload && systemctl restart mailwatch && sleep 1 && systemctl is-active mailwatch"
