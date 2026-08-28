package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"mailwatch/internal/action"
	"mailwatch/internal/config"
	"mailwatch/internal/events"
	"mailwatch/internal/mail"
	"mailwatch/internal/rules"
)

// handleTestIMAP 测试 IMAP 连接与登录。
func (s *Server) handleTestIMAP(w http.ResponseWriter, r *http.Request) {
	var cfg config.IMAPConfig
	if err := readJSON(r, &cfg); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	config.CleanIMAP(&cfg)
	if cfg.Port == 0 {
		cfg.Port = 993
	}
	if err := config.CheckIMAPHost(&cfg); err != nil {
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

// handleListFolders 连上 IMAP 列出该邮箱的所有文件夹,供 UI 下拉选择。
func (s *Server) handleListFolders(w http.ResponseWriter, r *http.Request) {
	var cfg config.IMAPConfig
	if err := readJSON(r, &cfg); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	config.CleanIMAP(&cfg)
	if cfg.Port == 0 {
		cfg.Port = 993
	}
	if err := config.CheckIMAPHost(&cfg); err != nil {
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

// handleTestSMTP 测试 SMTP 认证;to 非空则实际发一封测试邮件。
func (s *Server) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		config.SMTPConfig
		To string `json:"to"`
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
				c, err := action.ConnectSMTP(&cfg)
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
			return action.SendMail(&cfg, []string{req.To}, []byte(b.String()))
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

// handleTestRule 用示例邮件试跑当前(未保存的)规则,返回命中的规则。
func (s *Server) handleTestRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules   []config.Rule `json:"rules"`
		Mailbox string        `json:"mailbox"`
		From    string        `json:"from"`
		Subject string        `json:"subject"`
		Body    string        `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		jsonErr(w, 400, "请求格式错误")
		return
	}
	m := &mail.Mail{Mailbox: req.Mailbox, From: req.From, FromAddr: req.From, Subject: req.Subject, Body: req.Body}
	if i := rules.FirstMatch(req.Rules, m); i >= 0 {
		writeJSON(w, 200, map[string]any{"matched": true, "index": i, "name": req.Rules[i].Name})
		return
	}
	writeJSON(w, 200, map[string]any{"matched": false})
}

// handleTestE2E 全链路测试:用已保存的配置构造一封测试邮件,
// 走真实的规则匹配和转发动作(会实际发信),返回每步结果。不写入历史。
func (s *Server) handleTestE2E(w http.ResponseWriter, r *http.Request) {
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
	m := &mail.Mail{
		Mailbox: req.Mailbox, From: req.From, FromAddr: req.From,
		Subject: "[测试] " + req.Subject, Body: req.Body, Raw: []byte(raw),
		Date: time.Now().Format(time.RFC1123Z),
	}
	i := rules.FirstMatch(cfg.Rules, m)
	if i < 0 {
		writeJSON(w, 200, map[string]any{"matched": false})
		return
	}
	rule := &cfg.Rules[i]
	acts, err := action.Build(cfg, rule)
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
	events.Add("info", "全链路测试: 命中规则[%s]", rule.Name)
	writeJSON(w, 200, map[string]any{"matched": true, "name": rule.Name, "results": results})
}

// ---------- AI 生成规则 ----------

const aiRulePrompt = `你是邮件转发规则助手。根据用户的中文描述,输出一个 JSON 对象(不要 markdown 代码块,不要多余文字):
{"name":"规则名(简短中文)","mailboxes":["限定的监听邮箱名"],"from_contains":["发件人地址或名称片段"],"keywords":["关键词"],"forward_to":["目标邮箱"]}
说明:各字段都是数组,匹配语义是"包含任一即命中",大小写不敏感。
mailboxes 是"只对哪些监听邮箱生效",用户没有明确指定就输出 [](表示全部);
用户没提到关键词就输出 "keywords":[](表示全部转发);没提到发件人就输出 "from_contains":[]。
只提取用户明确说的信息,不要编造邮箱地址。`

func (s *Server) handleAIRule(w http.ResponseWriter, r *http.Request) {
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
	var rule config.Rule
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &rule); err != nil {
		jsonErr(w, 502, "AI 输出不是有效规则: %s", truncate(content, 300))
		return
	}
	events.Add("info", "AI 生成规则: %s", rule.Name)
	writeJSON(w, 200, rule)
}
