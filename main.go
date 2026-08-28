package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var version = "0.1.7"

// App 持有当前配置与运行状态,Web 后台改配置后通过 reload 热重启监听。
type App struct {
	cfgPath string
	state   *State
	history *History
	startAt time.Time

	mu  sync.Mutex
	cfg *Config

	reload chan struct{}

	connMu    sync.Mutex
	connected map[string]bool // 各邮箱连接状态
}

func (a *App) setConnected(name string, v bool) {
	a.connMu.Lock()
	a.connected[name] = v
	a.connMu.Unlock()
}

func (a *App) connSnapshot() map[string]bool {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	out := make(map[string]bool, len(a.connected))
	for k, v := range a.connected {
		out[k] = v
	}
	return out
}

func (a *App) Config() *Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// ApplyConfig 校验、落盘并热重载。
func (a *App) ApplyConfig(cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	// 每条规则的动作先组装一遍,配置错误在保存时就暴露
	for i := range cfg.Rules {
		if _, err := buildActions(cfg, &cfg.Rules[i]); err != nil {
			return err
		}
	}
	a.mu.Lock()
	if err := SaveConfig(a.cfgPath, cfg); err != nil {
		a.mu.Unlock()
		return err
	}
	a.cfg = cfg
	a.mu.Unlock()
	select {
	case a.reload <- struct{}{}:
	default:
	}
	return nil
}

// handleMail 按当前配置组装的规则处理一封邮件,并写入历史记录。
func (a *App) handleMail(cfg *Config, ruleActions [][]Action, m *Mail) {
	Ev.Add("info", "新邮件 UID=%d from=%s subject=%s", m.UID, m.FromAddr, m.Subject)
	rec := &MailRecord{
		ID: time.Now().UnixNano(), Time: time.Now(), UID: m.UID, Mailbox: m.Mailbox,
		From: m.From, FromAddr: m.FromAddr, To: m.To, Subject: m.Subject, Body: m.Body,
	}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if !r.Match(m) {
			continue
		}
		rec.Rule = r.Name
		for _, act := range ruleActions[i] {
			res := ActionResult{Action: act.Name(), Target: act.Target(), Time: time.Now()}
			if err := act.Execute(m); err != nil {
				res.Error = err.Error()
				Ev.Add("error", "规则[%s]动作 %s 失败 (UID=%d): %v", r.Name, act.Name(), m.UID, err)
			} else {
				res.OK = true
				Ev.Add("ok", "规则[%s]命中,动作 %s 完成 (UID=%d)", r.Name, act.Name(), m.UID)
			}
			rec.Results = append(rec.Results, res)
		}
		break // 一封邮件只按第一条命中的规则处理
	}
	a.history.Add(rec)
}

// runWatcherLoop 监督循环:每个邮箱一个监听 goroutine;收到 reload 时
// 整组停掉,用新配置重建。
func (a *App) runWatcherLoop(ctx context.Context) {
	for ctx.Err() == nil {
		cfg := a.Config()
		if err := cfg.Validate(); err != nil {
			Ev.Add("error", "配置无效,监听暂停: %v", err)
			select {
			case <-a.reload: // 后台保存修正后立即恢复
				continue
			case <-ctx.Done():
				return
			}
		}
		ruleActions := make([][]Action, len(cfg.Rules))
		for i := range cfg.Rules {
			acts, err := buildActions(cfg, &cfg.Rules[i])
			if err != nil { // ApplyConfig 已挡过,理论上不会到这
				Ev.Add("error", "配置错误: %v", err)
				acts = nil
			}
			ruleActions[i] = acts
		}

		groupCtx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for i := range cfg.Mailboxes {
			mb := cfg.Mailboxes[i] // 值拷贝,组内不可变
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.watchMailbox(groupCtx, cfg, &mb, ruleActions)
			}()
		}

		select {
		case <-a.reload:
			Ev.Add("info", "配置已更新,重启所有监听")
		case <-ctx.Done():
		}
		cancel()
		wg.Wait()
		a.connMu.Lock()
		a.connected = map[string]bool{}
		a.connMu.Unlock()
	}
}

// watchMailbox 单个邮箱的重连循环(指数退避,稳定运行后重置)。
func (a *App) watchMailbox(ctx context.Context, cfg *Config, mb *MailboxConfig, ruleActions [][]Action) {
	backoff := 10 * time.Second
	for ctx.Err() == nil {
		w := NewWatcher(mb, a.state, func(m *Mail) { a.handleMail(cfg, ruleActions, m) })
		w.OnConnected = func() { a.setConnected(mb.Name, true) }
		started := time.Now()
		err := w.Run(ctx)
		a.setConnected(mb.Name, false)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 5*time.Minute {
			backoff = 10 * time.Second
		}
		Ev.Add("warn", "[%s] 连接断开: %v, %s 后重连", mb.Name, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Minute)
	}
}

func main() {
	log.SetFlags(log.LstdFlags)
	cfgPath := flag.String("c", "config.toml", "配置文件路径")
	showVer := flag.Bool("version", false, "打印版本")
	hashPw := flag.String("hash", "", "生成密码的 bcrypt 哈希后退出(供 [admin] password_hash 用)")
	flag.Parse()
	if *showVer {
		fmt.Printf("mailwatch %s\n", version)
		return
	}
	if *hashPw != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(*hashPw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(h))
		return
	}

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		// 解析失败或没有后台可修正时才致命;语义无效则只起后台,监听暂停
		if cfg == nil || cfg.Admin.Listen == "" {
			log.Fatalf("加载配置失败: %v", err)
		}
		Ev.Add("error", "配置无效: %v —— 监听未启动,请在后台修正并保存", err)
	}

	dir := filepath.Dir(*cfgPath)
	Ev.Init(filepath.Join(dir, "events.jsonl"))
	app := &App{
		cfgPath:   *cfgPath,
		cfg:       cfg,
		state:     LoadState(filepath.Join(dir, "state.json")),
		history:   LoadHistory(filepath.Join(dir, "history.jsonl")),
		startAt:   time.Now(),
		reload:    make(chan struct{}, 1),
		connected: map[string]bool{},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	Ev.Add("info", "mailwatch %s 启动, %d 个邮箱, %d 条规则", version, len(cfg.Mailboxes), len(cfg.Rules))
	if cfg.Admin.Listen != "" {
		go StartAdmin(app)
	}
	app.runWatcherLoop(ctx)
	log.Printf("退出")
}
