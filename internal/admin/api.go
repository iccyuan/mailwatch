package admin

import (
	"net/http"
	"strconv"
	"time"

	"mailwatch/internal/config"
	"mailwatch/internal/events"
)

// handleStatus 运行状态:各邮箱连接/游标、最近事件。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()
	conns := s.app.ConnSnapshot()
	cursors := s.app.Cursors()
	allOK := len(cfg.Mailboxes) > 0
	mbs := []map[string]any{}
	for _, mb := range cfg.Mailboxes {
		c := conns[mb.Name]
		if !c {
			allOK = false
		}
		mbs = append(mbs, map[string]any{
			"name": mb.Name, "user": mb.User, "connected": c, "last_uid": cursors[mb.Name],
		})
	}
	writeJSON(w, 200, map[string]any{
		"version":    s.app.Version(),
		"connected":  allOK,
		"mailboxes":  mbs,
		"uptime_sec": int(time.Since(s.app.StartAt()).Seconds()),
		"rules":      len(cfg.Rules),
		"events":     events.Recent(100),
	})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()
	writeJSON(w, 200, map[string]any{
		"mailboxes": cfg.Mailboxes,
		"smtp":      cfg.SMTP,
		"ai":        cfg.AI,
		"rules":     cfg.Rules,
		"admin":     map[string]string{"listen": cfg.Admin.Listen, "username": cfg.Admin.Username},
	})
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mailboxes []config.MailboxConfig `json:"mailboxes"`
		SMTP      config.SMTPConfig      `json:"smtp"`
		AI        config.AIConfig        `json:"ai"`
		Rules     []config.Rule          `json:"rules"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonErr(w, 400, "请求格式错误: %v", err)
		return
	}
	next := *s.app.Config() // Admin 段保持不变
	next.Mailboxes, next.SMTP, next.AI, next.Rules = req.Mailboxes, req.SMTP, req.AI, req.Rules
	if err := s.app.ApplyConfig(&next); err != nil {
		jsonErr(w, 400, "%v", err)
		return
	}
	events.Add("ok", "配置已保存并热生效 (%d 个邮箱, %d 条规则)", len(next.Mailboxes), len(next.Rules))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- 历史与统计 ----------

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	total, items := s.app.History().List(offset, limit, q.Get("q"), q.Get("mailbox"))
	writeJSON(w, 200, map[string]any{"total": total, "items": items})
}

func (s *Server) handleHistoryItem(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	rec := s.app.History().Get(id)
	if rec == nil {
		jsonErr(w, 404, "记录不存在")
		return
	}
	writeJSON(w, 200, rec)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.app.History().Stats())
}
