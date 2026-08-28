# mailwatch

多邮箱 IMAP 监听转发服务,单二进制零依赖,自带 Web 管理后台。

- **多邮箱监听**:`[[mailboxes]]` 配多个账户,每个独立 IMAP 连接/UID 游标/断线重连;IMAP IDLE 实时推送,轮询兜底,服务器不支持 IDLE 自动降级
- **规则转发**:发件人/关键词「包含即命中」(无需正则),可限定生效邮箱、多目标转发,原始邮件可选附为 .eml(默认不附);一封邮件按第一条命中规则处理
- **Web 后台**:登录制(bcrypt + 会话 Cookie + 失败限速),概览统计面板、邮件历史与送达详情、规则可视化编辑 + 规则测试器 + 全链路真实转发测试、服务商智能识别(QQ/163/Gmail 等自动填服务器与授权码提示)、IMAP 文件夹拉取下拉选择、AI 一句话生成规则(可选,OpenAI 兼容接口)
- **持久化**:配置 `config.toml`、游标 `state.json`、邮件历史 `history.jsonl`、运行日志 `events.jsonl`,重启不丢、不重复转发
- **扩展口**:命中规则后走 `Action` 接口(action.go),加 webhook/Telegram 等新动作只需实现接口并在 `buildActions` 注册

## 配置

复制 `config.example.toml` 为 `config.toml`,填好 `[admin]` 后其余都可在 Web 后台完成(保存热生效,无需重启)。后台密码哈希用 `./mailwatch -hash '你的密码'` 生成。**config.toml 含凭据,勿入仓库。**

## 构建与部署

需 Go 1.24+,静态页 go:embed 进二进制:

```bash
go build -o mailwatch .
./mailwatch -c config.toml
```

生产环境建议 systemd 常驻(参考 `mailwatch.service`,按实际路径调整),Web 后台只绑 `127.0.0.1`,公网经反代(Caddy/Nginx)加 HTTPS,反代需透传 `X-Forwarded-For` / `X-Forwarded-Proto`。

`deploy.ps1` 是基于 [srv](https://github.com/iccyuan/srv) 的推源码→远端编译→重启的一键脚本,profile 与路径可用环境变量 `MAILWATCH_PROFILE` / `MAILWATCH_REMOTE` 覆盖。

概览数字使用 [DSEG](https://github.com/keshikan/DSEG) 七段字体(SIL OFL 1.1),已随仓库内嵌。
