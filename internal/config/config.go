// Package config 定义配置模型与加载/校验/回写。
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type IMAPConfig struct {
	Host            string `toml:"host" json:"host"`
	Port            int    `toml:"port" json:"port"`
	User            string `toml:"user" json:"user"`
	Password        string `toml:"password" json:"password"`
	Folder          string `toml:"folder" json:"folder"`
	PollIntervalSec int    `toml:"poll_interval_sec" json:"poll_interval_sec"`
	Idle            bool   `toml:"idle" json:"idle"`
	MarkSeen        bool   `toml:"mark_seen" json:"mark_seen"`
}

// MailboxConfig 一个被监听的邮箱账户(嵌入的 IMAP 字段在 toml/json 中平铺)。
type MailboxConfig struct {
	Name       string `toml:"name" json:"name"` // 展示名,也是游标/规则限定的 key
	IMAPConfig        // 匿名嵌入,字段自动平铺
}

type SMTPConfig struct {
	Host     string `toml:"host" json:"host"`
	Port     int    `toml:"port" json:"port"`
	SSL      bool   `toml:"ssl" json:"ssl"` // true=SMTPS(465), false=STARTTLS(587)
	User     string `toml:"user" json:"user"`
	Password string `toml:"password" json:"password"`
	From     string `toml:"from" json:"from"`
}

// AdminConfig Web 后台。listen 留空则不启动后台。
type AdminConfig struct {
	Listen       string `toml:"listen" json:"listen"`
	Username     string `toml:"username" json:"username"`
	PasswordHash string `toml:"password_hash" json:"-"`
}

// AIConfig 可选:OpenAI 兼容接口,用于后台"AI 生成规则"。留空则该功能隐藏。
type AIConfig struct {
	BaseURL string `toml:"base_url" json:"base_url"` // 如 http://127.0.0.1:8000/v1 (litellm)
	APIKey  string `toml:"api_key" json:"api_key"`
	Model   string `toml:"model" json:"model"`
}

// Rule 一条匹配规则。匹配条件都是"包含即命中",条件留空表示不限。
// 规则从上到下匹配,第一条命中的生效。动作字段(目前只有 forward_to)
// 以后可平行扩展:webhook_url、telegram_chat 等,在 action.Build 里注册即可。
type Rule struct {
	Name           string   `toml:"name" json:"name"`
	Disabled       bool     `toml:"disabled" json:"disabled"`   // 停用后不参与匹配
	Mailboxes      []string `toml:"mailboxes" json:"mailboxes"` // 只对这些邮箱生效;空=全部邮箱
	FromContains   []string `toml:"from_contains" json:"from_contains"`
	Keywords       []string `toml:"keywords" json:"keywords"`
	ForwardTo      []string `toml:"forward_to" json:"forward_to"`
	AttachOriginal bool     `toml:"attach_original" json:"attach_original"` // 转发时附带原始邮件 .eml,默认否
}

type Config struct {
	LegacyIMAP *IMAPConfig     `toml:"imap,omitempty" json:"-"` // 旧单账户配置,加载时迁移进 Mailboxes
	Mailboxes  []MailboxConfig `toml:"mailboxes" json:"mailboxes"`
	SMTP       SMTPConfig      `toml:"smtp" json:"smtp"`
	Admin      AdminConfig     `toml:"admin" json:"admin"`
	AI         AIConfig        `toml:"ai" json:"ai"`
	Rules      []Rule          `toml:"rules" json:"rules"`
}

// Load 解析配置。语义校验失败时仍返回解析出的 cfg(err 非 nil),
// 让调用方可以只起 Web 后台供用户修正,而不是整个进程起不来。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return &cfg, err
	}
	return &cfg, nil
}

// Validate 校验并补默认值。Web 后台保存前也走这里。
func (cfg *Config) Validate() error {
	// 旧版单账户 [imap] → [[mailboxes]] 迁移
	if len(cfg.Mailboxes) == 0 && cfg.LegacyIMAP != nil && cfg.LegacyIMAP.Host != "" {
		cfg.Mailboxes = []MailboxConfig{{Name: cfg.LegacyIMAP.User, IMAPConfig: *cfg.LegacyIMAP}}
	}
	cfg.LegacyIMAP = nil
	if len(cfg.Mailboxes) == 0 {
		return fmt.Errorf("至少需要一个 [[mailboxes]] 收信邮箱")
	}
	seen := map[string]bool{}
	for i := range cfg.Mailboxes {
		mb := &cfg.Mailboxes[i]
		CleanIMAP(&mb.IMAPConfig)
		mb.Name = strings.TrimSpace(mb.Name)
		if mb.Host == "" || mb.User == "" {
			return fmt.Errorf("邮箱 #%d host/user 未配置", i+1)
		}
		if mb.Name == "" {
			mb.Name = mb.User
		}
		if seen[mb.Name] {
			return fmt.Errorf("邮箱名称重复: %s", mb.Name)
		}
		seen[mb.Name] = true
		if mb.Port == 0 {
			mb.Port = 993
		}
		if err := CheckIMAPHost(&mb.IMAPConfig); err != nil {
			return fmt.Errorf("邮箱[%s] %w", mb.Name, err)
		}
		if mb.Folder == "" {
			mb.Folder = "INBOX"
		}
		if mb.PollIntervalSec <= 0 {
			mb.PollIntervalSec = 60
		}
	}
	if len(cfg.Rules) == 0 {
		return fmt.Errorf("至少需要一条 [[rules]] 规则")
	}
	for i := range cfg.Rules {
		if cfg.Rules[i].Name == "" {
			cfg.Rules[i].Name = fmt.Sprintf("rule#%d", i+1)
		}
		for _, mbName := range cfg.Rules[i].Mailboxes {
			if !seen[mbName] {
				return fmt.Errorf("规则[%s]限定的邮箱不存在: %s", cfg.Rules[i].Name, mbName)
			}
		}
	}
	cfg.SMTP.Host = strings.TrimSpace(cfg.SMTP.Host)
	cfg.SMTP.User = strings.TrimSpace(cfg.SMTP.User)
	cfg.SMTP.Password = strings.TrimSpace(cfg.SMTP.Password)
	cfg.SMTP.From = strings.TrimSpace(cfg.SMTP.From)
	if cfg.SMTP.Port == 0 {
		cfg.SMTP.Port = 465
		cfg.SMTP.SSL = true
	}
	if cfg.SMTP.From == "" {
		cfg.SMTP.From = cfg.SMTP.User
	}
	if cfg.Admin.Listen != "" {
		if cfg.Admin.Username == "" || cfg.Admin.PasswordHash == "" {
			return fmt.Errorf("[admin] 启用了 listen 但 username/password_hash 未配置")
		}
	}
	return nil
}

// CleanIMAP 清理凭据里的不可见字符:复制粘贴常带首尾空白/换行,
// 会触发 IMAP literal 传输并在部分服务器上引发协议错误。
// Gmail 应用密码展示时带内部空格,一并去掉。
func CleanIMAP(c *IMAPConfig) {
	c.Host = strings.TrimSpace(c.Host)
	c.User = strings.TrimSpace(c.User)
	c.Folder = strings.TrimSpace(c.Folder)
	c.Password = strings.TrimSpace(c.Password)
	if strings.Contains(strings.ToLower(c.Host), "gmail") {
		c.Password = strings.ReplaceAll(c.Password, " ", "")
	}
}

// CheckIMAPHost 防呆:填成 POP3/SMTP 地址或端口时给出人话提示,
// 而不是等连接后抛协议解析错误(如 POP3 的 "+OK" 会报 expected CRLF)。
func CheckIMAPHost(c *IMAPConfig) error {
	h := strings.ToLower(c.Host)
	if strings.HasPrefix(h, "pop.") || strings.HasPrefix(h, "pop3.") || c.Port == 995 || c.Port == 110 {
		return fmt.Errorf("填的是 POP3 服务器(%s:%d),监听需要 IMAP,例如 imap.gmail.com:993", c.Host, c.Port)
	}
	if strings.HasPrefix(h, "smtp.") || c.Port == 465 || c.Port == 587 || c.Port == 25 {
		return fmt.Errorf("填的是 SMTP 发信服务器(%s:%d),收信监听需要 IMAP,例如 imap.gmail.com:993", c.Host, c.Port)
	}
	return nil
}

// Save 回写配置文件(机器管理,注释不保留)。
func Save(path string, cfg *Config) error {
	var buf bytes.Buffer
	buf.WriteString("# mailwatch 配置 — 由 Web 后台管理,手工注释不会保留\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
