// Package watcher 维持单个邮箱的 IMAP 连接与增量拉取。
package watcher

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"mailwatch/internal/config"
	"mailwatch/internal/events"
	"mailwatch/internal/mail"
	"mailwatch/internal/store"
)

// ClientVersion 发给 IMAP ID 命令的版本号,由 main 注入。
var ClientVersion = "dev"

// Watcher 维持一个邮箱的 IMAP 连接:IDLE 实时感知新邮件(服务器不支持则退回纯轮询),
// 每轮用 UID SEARCH 拉增量,逐封回调 handle。多个邮箱各自持有一个 Watcher。
type Watcher struct {
	mb     *config.MailboxConfig
	state  *store.State
	handle func(m *mail.Mail)
	wake   chan struct{}

	OnConnected func() // 连接就绪回调(可选),供状态上报
}

func New(mb *config.MailboxConfig, state *store.State, handle func(m *mail.Mail)) *Watcher {
	return &Watcher{mb: mb, state: state, handle: handle, wake: make(chan struct{}, 1)}
}

// Run 跑一条连接直到出错或 ctx 取消。调用方负责重连。
func (w *Watcher) Run(ctx context.Context) error {
	mb := w.mb
	opts := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(d *imapclient.UnilateralDataMailbox) {
				if d.NumMessages != nil {
					select {
					case w.wake <- struct{}{}:
					default:
					}
				}
			},
		},
	}
	c, err := imapclient.DialTLS(fmt.Sprintf("%s:%d", mb.Host, mb.Port), opts)
	if err != nil {
		return fmt.Errorf("IMAP 连接: %w", err)
	}
	defer c.Close()

	if err := c.Login(mb.User, mb.Password).Wait(); err != nil {
		return fmt.Errorf("IMAP 登录: %w", err)
	}
	// 网易(163/126)等 Coremail 服务器要求登录后发 ID,否则 SELECT 报 Unsafe Login
	if c.Caps().Has(imap.CapID) {
		_, _ = c.ID(&imap.IDData{Name: "mailwatch", Version: ClientVersion}).Wait()
	}
	sel, err := c.Select(mb.Folder, nil).Wait()
	if err != nil {
		return fmt.Errorf("IMAP select %s: %w", mb.Folder, err)
	}

	if w.state.Get(mb.Name) == 0 {
		// 首次运行:跳过历史邮件,只监听增量
		w.state.Save(mb.Name, uint32(sel.UIDNext)-1)
		log.Printf("[%s] 首次运行,从 UID>%d 开始监听", mb.Name, w.state.Get(mb.Name))
	}

	useIdle := mb.Idle && c.Caps().Has(imap.CapIdle)
	poll := time.Duration(mb.PollIntervalSec) * time.Second
	events.Add("ok", "[%s] IMAP 已连接 %s folder=%s idle=%v poll=%s", mb.Name, mb.Host, mb.Folder, useIdle, poll)
	if w.OnConnected != nil {
		w.OnConnected()
	}

	for {
		if err := w.checkNew(c); err != nil {
			return err
		}
		if useIdle {
			if err := w.idleWait(ctx, c, poll); err != nil {
				return err
			}
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(poll):
			case <-w.wake:
			}
			if err := c.Noop().Wait(); err != nil {
				return fmt.Errorf("NOOP: %w", err)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// idleWait 进入 IDLE,直到有新邮件通知 / 到达兜底轮询周期 / ctx 取消。
func (w *Watcher) idleWait(ctx context.Context, c *imapclient.Client, poll time.Duration) error {
	idle, err := c.Idle()
	if err != nil {
		return fmt.Errorf("IDLE: %w", err)
	}
	select {
	case <-w.wake:
	case <-time.After(poll):
	case <-ctx.Done():
	}
	if err := idle.Close(); err != nil {
		return fmt.Errorf("IDLE 结束: %w", err)
	}
	return idle.Wait()
}

func (w *Watcher) checkNew(c *imapclient.Client) error {
	last := w.state.Get(w.mb.Name)
	var set imap.UIDSet
	set.AddRange(imap.UID(last+1), 0) // 0 = *
	data, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
	if err != nil {
		return fmt.Errorf("UID SEARCH: %w", err)
	}
	var uids []imap.UID
	for _, u := range data.AllUIDs() {
		// n:* 始终包含最后一封已有邮件,过滤掉 <= last 的
		if uint32(u) > last {
			uids = append(uids, u)
		}
	}
	for _, uid := range uids {
		if err := w.fetchOne(c, uid); err != nil {
			return err
		}
		w.state.Save(w.mb.Name, uint32(uid))
	}
	return nil
}

func (w *Watcher) fetchOne(c *imapclient.Client, uid imap.UID) error {
	var set imap.UIDSet
	set.AddNum(uid)
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(set, &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return fmt.Errorf("UID FETCH %d: %w", uid, err)
	}
	if len(msgs) == 0 {
		log.Printf("[%s] UID %d fetch 无结果,跳过", w.mb.Name, uid)
		return nil
	}
	raw := msgs[0].FindBodySection(section)
	if raw == nil {
		log.Printf("[%s] UID %d 无正文数据,跳过", w.mb.Name, uid)
		return nil
	}
	m := mail.Parse(uint32(uid), raw)
	m.Mailbox = w.mb.Name
	w.handle(m)

	if w.mb.MarkSeen {
		flags := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
		if err := c.Store(set, flags, nil).Close(); err != nil {
			log.Printf("[%s] UID %d 标记已读失败: %v", w.mb.Name, uid, err)
		}
	}
	return nil
}
