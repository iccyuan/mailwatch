// Package admin 提供 Web 管理后台:登录鉴权、配置管理 API、测试工具与静态页面。
package admin

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"mailwatch/internal/config"
	"mailwatch/internal/events"
	"mailwatch/internal/store"
)

//go:embed web
var webFS embed.FS

const sessionTTL = 7 * 24 * time.Hour

// Backend 是后台依赖的应用能力,由 main.App 实现。
type Backend interface {
	Config() *config.Config
	ApplyConfig(*config.Config) error // 校验 + 落盘 + 热重载
	ConnSnapshot() map[string]bool    // 各邮箱连接状态
	Cursors() map[string]uint32       // 各邮箱 UID 游标
	History() *store.History
	StartAt() time.Time
	Version() string
}

type Server struct {
	app Backend

	mu       sync.Mutex
	sessions map[string]time.Time
	fails    map[string]*loginFails // 按 IP 的登录失败限速
}

type loginFails struct {
	count     int
	lockUntil time.Time
}

// Start 启动后台 HTTP 服务(阻塞),listen 取自当前配置。
func Start(app Backend) {
	s := &Server{
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
	events.Add("info", "Web 后台启动于 %s", listen)
	srv := &http.Server{Addr: listen, Handler: secureHeaders(mux), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		events.Add("error", "Web 后台退出: %v", err)
	}
}

// ---------- 静态资源 ----------

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := webFS.ReadFile("web/index.html")
	// 静态资源 URL 注入版本号做缓存失效:升级后浏览器自动拉新资源
	data = bytes.ReplaceAll(data, []byte("{{v}}"), []byte(s.app.Version()))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// serveStatic 提供 web/ 下的静态资源(样式、脚本、字体)。
func serveStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	data, err := webFS.ReadFile("web/" + name)
	if err != nil || name == "index.html" {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".woff2"):
		w.Header().Set("Content-Type", "font/woff2")
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

// ---------- 安全与鉴权 ----------

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// img-src 放开 http(s):邮件详情的原始样式渲染需要加载邮件里的远程图片;
		// 脚本仍严格限制,且邮件 HTML 只进无脚本的 sandbox iframe
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src data: https: http:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// isHTTPS 直连 TLS 或经反代(Caddy 设 X-Forwarded-Proto)都算。
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// 仅当请求来自本机(反代)时,才信任 X-Forwarded-For 里的真实客户端 IP;
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

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
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

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
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

	adm := s.app.Config().Admin
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(adm.Username)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(adm.PasswordHash), []byte(req.Password)) == nil
	if !userOK || !passOK {
		s.mu.Lock()
		f.count++
		if f.count >= 5 {
			f.lockUntil = time.Now().Add(time.Minute)
			f.count = 0
		}
		s.mu.Unlock()
		events.Add("warn", "后台登录失败 (IP %s)", ip)
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
	events.Add("info", "后台登录成功 (IP %s)", ip)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("mw_session"); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "mw_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
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
	events.Add("ok", "后台密码已修改")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- JSON 工具 ----------

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

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
