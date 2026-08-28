package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/crypto/bcrypt"
)

//go:embed web
var webFS embed.FS

const sessionTTL = 7 * 24 * time.Hour

type AdminServer struct {
	app *App

	mu       sync.Mutex
	sessions map[string]time.Time
	fails    map[string]*loginFails // 按 IP 的登录失败限速
}

type loginFails struct {
	count     int
	lockUntil time.Time
}

func StartAdmin(app *App) {
	s := &AdminServer{
		app:      app,
		sessions: map[string]time.Time{},
		fails:    map[string]*loginFails{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.serveIndex)
	mux.HandleFunc("GET /static/", serveStatic)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.auth(s.handleLogout))
	mux.HandleFunc("GET /api/status", s.auth(s.handleStatus))
	mux.HandleFunc("GET /api/config", s.auth(s.handleGetConfig))
	mux.HandleFunc("PUT /api/config", s.auth(s.handlePutConfig))
	mux.HandleFunc("POST /api/password", s.auth(s.handlePassword))
	mux.HandleFunc("GET /api/history", s.auth(s.handleHistory))
	mux.HandleFunc("GET /api/history/item", s.auth(s.handleHistoryItem))
	mux.HandleFunc("GET /api/stats", s.auth(s.handleStats))
	mux.HandleFunc("POST /api/imap/folders", s.auth(s.handleListFolders))
	mux.HandleFunc("POST /api/test/e2e", s.auth(s.handleTestE2E))
	mux.HandleFunc("POST /api/test/imap", s.auth(s.handleTestIMAP))
	mux.HandleFunc("POST /api/test/smtp", s.auth(s.handleTestSMTP))
	mux.HandleFunc("POST /api/test/rule", s.auth(s.handleTestRule))
	mux.HandleFunc("POST /api/ai/rule", s.auth(s.handleAIRule))

	listen := app.Config().Admin.Listen
	Ev.Add("info", "Web 后台启动于 %s", listen)
	srv := &http.Server{Addr: listen, Handler: secureHeaders(mux), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		Ev.Add("error", "Web 后台退出: %v", err)
	}
}

// ---------- 基础设施 ----------

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// isHTTPS 直连 TLS 或经反代(Caddy 设 X-Forwarded-Proto)都算。
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// 仅当请求来自本机(Caddy 反代)时,才信任 X-Forwarded-For 里的真实客户端 IP;
	// 直连时不看该头,防止伪造绕过登录限速。
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	return host
}

func (s *AdminServer) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// serveStatic 提供 web/ 下的静态资源(如内嵌字体)。
func serveStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	data, err := webFS.ReadFile("web/" + name)
	if err != nil || name == "index.html" {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".woff2") {
		w.Header().Set("Content-Type", "font/woff2")
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (s *AdminServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("mw_session")
		if err == nil {
			s.mu.Lock()
			exp, ok := s.sessions[c.Value]
			if ok && time.Now().After(exp) {
				delete(s.sessions, c.Value)
				ok = false
			}
			s.mu.Unlock()
			if ok {
				next(w, r)
				return
			}
		}
		jsonErr(w, http.StatusUnauthorized, "未登录")
	}
}

// ---------- 登录 ----------

func (s *AdminServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := readJSON(r, &req); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	ip := clientIP(r)
	s.mu.Lock()
	f := s.fails[ip]
	if f == nil {
		f = &loginFails{}
		s.fails[ip] = f
	}
	locked := time.Now().Before(f.lockUntil)
	s.mu.Unlock()
	if locked {
		jsonErr(w, 429, "尝试过于频繁,请 1 分钟后再试")
		return
	}

	admin := s.app.Config().Admin
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(admin.Username)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)) == nil
	ok := userOK && passOK
	if !ok {
		s.mu.Lock()
		f.count++
		if f.count >= 5 {
			f.lockUntil = time.Now().Add(time.Minute)
			f.count = 0
		}
		s.mu.Unlock()
		Ev.Add("warn", "后台登录失败 (IP %s)", ip)
		time.Sleep(time.Second)
		jsonErr(w, 401, "用户名或密码错误")
		return
	}

	buf := make([]byte, 32)
	rand.Read(buf)
	token := hex.EncodeToString(buf)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	delete(s.fails, ip)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "mw_session", Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: isHTTPS(r),
		MaxAge: int(sessionTTL.Seconds()),
	})
	Ev.Add("info", "后台登录成功 (IP %s)", ip)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *AdminServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("mw_session"); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "mw_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *AdminServer) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Old, New string }
	if err := readJSON(r, &req); err != nil || len(req.New) < 6 {
		jsonErr(w, 400, "新密码至少 6 位")
		return
	}
	cur := s.app.Config()
	if bcrypt.CompareHashAndPassword([]byte(cur.Admin.PasswordHash), []byte(req.Old)) != nil {
		jsonErr(w, 403, "原密码错误")
		return
	}
	h, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		jsonErr(w, 500, "%v", err)
		return
	}
	next := *cur
	next.Admin.PasswordHash = string(h)
	if err := s.app.ApplyConfig(&next); err != nil {
		jsonErr(w, 500, "保存失败: %v", err)
		return
	}
	Ev.Add("ok", "后台密码已修改")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- 状态与配置 ----------

func (s *AdminServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()
	conns := s.app.connSnapshot()
	cursors := s.app.state.Snapshot()
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
		"version":    version,
		"connected":  allOK,
		"mailboxes":  mbs,
		"uptime_sec": int(time.Since(s.app.startAt).Seconds()),
		"rules":      len(cfg.Rules),
		"events":     Ev.Recent(100),
	})
}

func (s *AdminServer) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()
	writeJSON(w, 200, map[string]any{
		"mailboxes": cfg.Mailboxes,
		"smtp":      cfg.SMTP,
		"ai":        cfg.AI,
		"rules":     cfg.Rules,
		"admin":     map[string]string{"listen": cfg.Admin.Listen, "username": cfg.Admin.Username},
	})
}

func (s *AdminServer) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mailboxes []MailboxConfig `json:"mailboxes"`
		SMTP      SMTPConfig      `json:"smtp"`
		AI        AIConfig        `json:"ai"`
		Rules     []Rule          `json:"rules"`
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
	Ev.Add("ok", "配置已保存并热生效 (%d 个邮箱, %d 条规则)", len(next.Mailboxes), len(next.Rules))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- 历史与统计 ----------

func (s *AdminServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	total, items := s.app.history.List(offset, limit, q.Get("q"), q.Get("mailbox"))
	writeJSON(w, 200, map[string]any{"total": total, "items": items})
}

func (s *AdminServer) handleHistoryItem(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	rec := s.app.history.Get(id)
	if rec == nil {
		jsonErr(w, 404, "记录不存在")
		return
	}
	writeJSON(w, 200, rec)
}

func (s *AdminServer) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.app.history.Stats())
}

// ---------- 测试 ----------

func (s *AdminServer) handleTestIMAP(w http.ResponseWriter, r *http.Request) {
	var cfg IMAPConfig
	if err := readJSON(r, &cfg); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	cleanIMAP(&cfg)
	if cfg.Port == 0 {
		cfg.Port = 993
	}
	if err := checkIMAPHost(&cfg); err != nil {
		jsonErr(w, 400, "%v", err)
		return
	}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			c, err := imapclient.DialTLS(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), nil)
			if err != nil {
				return fmt.Errorf("连接失败: %w", err)
			}
			defer c.Close()
			if err := c.Login(cfg.User, cfg.Password).Wait(); err != nil {
				return fmt.Errorf("登录失败: %w", err)
			}
			return nil
		}()
	}()
	select {
	case err := <-done:
		if err != nil {
			jsonErr(w, 400, "%v", err)
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "IMAP 连接和登录正常"})
	case <-time.After(15 * time.Second):
		jsonErr(w, 400, "连接超时(15s)")
	}
}

func (s *AdminServer) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SMTPConfig
		To string `json:"to"` // 非空则实际发一封测试邮件
	}
	if err := readJSON(r, &req); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	cfg := req.SMTPConfig
	if cfg.Port == 0 {
		cfg.Port = 465
		cfg.SSL = true
	}
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			if req.To == "" {
				c, err := connectSMTP(&cfg)
				if err != nil {
					return err
				}
				c.Quit()
				c.Close()
				return nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: mailwatch test\r\n", cfg.From, req.To)
			fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\nmailwatch 测试邮件 %s\r\n", time.Now().Format(time.DateTime))
			return sendSMTP(&cfg, []string{req.To}, []byte(b.String()))
		}()
	}()
	select {
	case err := <-done:
		if err != nil {
			jsonErr(w, 400, "%v", err)
			return
		}
		msg := "SMTP 连接和认证正常"
		if req.To != "" {
			msg = "测试邮件已发送到 " + req.To
		}
		writeJSON(w, 200, map[string]string{"ok": msg})
	case <-time.After(30 * time.Second):
		jsonErr(w, 400, "发送超时(30s)")
	}
}

// handleListFolders 连上 IMAP 列出该邮箱的所有文件夹,供 UI 下拉选择。
func (s *AdminServer) handleListFolders(w http.ResponseWriter, r *http.Request) {
	var cfg IMAPConfig
	if err := readJSON(r, &cfg); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	cleanIMAP(&cfg)
	if cfg.Port == 0 {
		cfg.Port = 993
	}
	if err := checkIMAPHost(&cfg); err != nil {
		jsonErr(w, 400, "%v", err)
		return
	}
	type result struct {
		folders []string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		done <- func() result {
			c, err := imapclient.DialTLS(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), nil)
			if err != nil {
				return result{nil, fmt.Errorf("连接失败: %w", err)}
			}
			defer c.Close()
			if err := c.Login(cfg.User, cfg.Password).Wait(); err != nil {
				return result{nil, fmt.Errorf("登录失败: %w", err)}
			}
			boxes, err := c.List("", "*", nil).Collect()
			if err != nil {
				return result{nil, fmt.Errorf("获取文件夹失败: %w", err)}
			}
			var names []string
			for _, b := range boxes {
				names = append(names, b.Mailbox)
			}
			sort.Slice(names, func(i, j int) bool { // INBOX 置顶,其余按名称
				if names[i] == "INBOX" {
					return true
				}
				if names[j] == "INBOX" {
					return false
				}
				return names[i] < names[j]
			})
			return result{names, nil}
		}()
	}()
	select {
	case res := <-done:
		if res.err != nil {
			jsonErr(w, 400, "%v", res.err)
			return
		}
		writeJSON(w, 200, map[string]any{"folders": res.folders})
	case <-time.After(20 * time.Second):
		jsonErr(w, 400, "连接超时(20s)")
	}
}

// handleTestE2E 全链路测试:用已保存的配置构造一封测试邮件,
// 走真实的规则匹配和转发动作(会实际发信),返回每步结果。不写入历史。
func (s *AdminServer) handleTestE2E(w http.ResponseWriter, r *http.Request) {
	var req struct{ Mailbox, From, Subject, Body string }
	if err := readJSON(r, &req); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	cfg := s.app.Config()
	if req.Subject == "" {
		req.Subject = "mailwatch 全链路测试"
	}
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		req.From, cfg.SMTP.From, mime.BEncoding.Encode("UTF-8", req.Subject),
		time.Now().Format(time.RFC1123Z), req.Body)
	m := &Mail{
		Mailbox: req.Mailbox, From: req.From, FromAddr: req.From,
		Subject: "[测试] " + req.Subject, Body: req.Body, Raw: []byte(raw),
		Date: time.Now().Format(time.RFC1123Z),
	}
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]
		if !rule.Match(m) {
			continue
		}
		acts, err := buildActions(cfg, rule)
		if err != nil {
			jsonErr(w, 400, "规则[%s]动作配置错误: %v", rule.Name, err)
			return
		}
		var results []map[string]any
		for _, act := range acts {
			res := map[string]any{"action": act.Name(), "target": act.Target()}
			if err := act.Execute(m); err != nil {
				res["ok"] = false
				res["error"] = err.Error()
			} else {
				res["ok"] = true
			}
			results = append(results, res)
		}
		Ev.Add("info", "全链路测试: 命中规则[%s]", rule.Name)
		writeJSON(w, 200, map[string]any{"matched": true, "name": rule.Name, "results": results})
		return
	}
	writeJSON(w, 200, map[string]any{"matched": false})
}

// handleTestRule 用示例邮件试跑当前(未保存的)规则,返回命中的规则。
func (s *AdminServer) handleTestRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules   []Rule `json:"rules"`
		Mailbox string `json:"mailbox"`
		From    string `json:"from"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	m := &Mail{Mailbox: req.Mailbox, From: req.From, FromAddr: req.From, Subject: req.Subject, Body: req.Body}
	for i := range req.Rules {
		if req.Rules[i].Match(m) {
			writeJSON(w, 200, map[string]any{"matched": true, "index": i, "name": req.Rules[i].Name})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"matched": false})
}

// ---------- AI 生成规则 ----------

const aiRulePrompt = `你是邮件转发规则助手。根据用户的中文描述,输出一个 JSON 对象(不要 markdown 代码块,不要多余文字):
{"name":"规则名(简短中文)","mailboxes":["限定的监听邮箱名"],"from_contains":["发件人地址或名称片段"],"keywords":["关键词"],"forward_to":["目标邮箱"]}
说明:各字段都是数组,匹配语义是"包含任一即命中",大小写不敏感。
mailboxes 是"只对哪些监听邮箱生效",用户没有明确指定就输出 [](表示全部);
用户没提到关键词就输出 "keywords":[](表示全部转发);没提到发件人就输出 "from_contains":[]。
只提取用户明确说的信息,不要编造邮箱地址。`

func (s *AdminServer) handleAIRule(w http.ResponseWriter, r *http.Request) {
	var req struct{ Desc string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Desc) == "" {
		jsonErr(w, 400, "请填写规则描述")
		return
	}
	ai := s.app.Config().AI
	if ai.BaseURL == "" || ai.Model == "" {
		jsonErr(w, 400, "[ai] 未配置 base_url/model")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"model": ai.Model,
		"messages": []map[string]string{
			{"role": "system", "content": aiRulePrompt},
			{"role": "user", "content": req.Desc},
		},
		"temperature": 0,
	})
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST",
		strings.TrimRight(ai.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		jsonErr(w, 500, "%v", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if ai.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+ai.APIKey)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(httpReq)
	if err != nil {
		jsonErr(w, 502, "AI 请求失败: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		jsonErr(w, 502, "AI 返回 %d: %s", resp.StatusCode, truncate(string(body), 300))
		return
	}
	var cc struct {
		Choices []struct {
			Message struct{ Content string }
		}
	}
	if err := json.Unmarshal(body, &cc); err != nil || len(cc.Choices) == 0 {
		jsonErr(w, 502, "AI 响应解析失败")
		return
	}
	content := strings.TrimSpace(cc.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	var rule Rule
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &rule); err != nil {
		jsonErr(w, 502, "AI 输出不是有效规则: %s", truncate(content, 300))
		return
	}
	Ev.Add("info", "AI 生成规则: %s", rule.Name)
	writeJSON(w, 200, rule)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
