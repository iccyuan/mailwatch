package store

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	historyKeep    = 5000  // 压缩后保留条数
	historyCompact = 10000 // 超过该条数触发压缩
	bodyCap        = 32 << 10
)

// ID64 历史记录 ID(纳秒时间戳)。int64 超出 JS 安全整数范围(2^53),
// 直接以数字进 JSON 会在前端丢精度,所以序列化为字符串;读取兼容旧的数字格式。
type ID64 int64

func (v ID64) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatInt(int64(v), 10) + `"`), nil
}

func (v *ID64) UnmarshalJSON(b []byte) error {
	n, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	*v = ID64(n)
	return err
}

// ActionResult 单个动作的送达情况。
type ActionResult struct {
	Action string    `json:"action"`
	Target string    `json:"target"`
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
	Time   time.Time `json:"time"`
}

// MailRecord 一封已处理邮件的完整记录(含正文,供后台查看)。
type MailRecord struct {
	ID       ID64           `json:"id"`
	Time     time.Time      `json:"time"`
	UID      uint32         `json:"uid"`
	Mailbox  string         `json:"mailbox"` // 来源邮箱
	From     string         `json:"from"`
	FromAddr string         `json:"from_addr"`
	To       string         `json:"to"`
	Subject  string         `json:"subject"`
	Body     string         `json:"body"`
	BodyHTML string         `json:"body_html,omitempty"` // HTML 正文,详情页渲染用
	Rule     string         `json:"rule,omitempty"`      // 命中的规则名,空=未命中
	Results  []ActionResult `json:"results,omitempty"`
}

// History JSONL 追加存储 + 内存索引。单进程写,无并发文件竞争。
type History struct {
	mu   sync.Mutex
	path string
	recs []*MailRecord
}

func LoadHistory(path string) *History {
	h := &History{path: path}
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r MailRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			h.recs = append(h.recs, &r)
		}
	}
	if len(h.recs) > historyKeep {
		h.recs = h.recs[len(h.recs)-historyKeep:]
		h.rewrite()
	}
	return h
}

func (h *History) Add(r *MailRecord) {
	if len(r.Body) > bodyCap {
		r.Body = r.Body[:bodyCap] + "\n...(正文过长已截断)"
	}
	if len(r.BodyHTML) > 4*bodyCap {
		r.BodyHTML = "" // HTML 过大就只留文本
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r)
	line, _ := json.Marshal(r)
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("写历史失败: %v", err)
	} else {
		f.Write(append(line, '\n'))
		f.Close()
	}
	if len(h.recs) > historyCompact {
		h.recs = h.recs[len(h.recs)-historyKeep:]
		h.rewrite()
	}
}

// rewrite 全量重写文件(调用方需持锁或在单线程 Load 阶段)。
func (h *History) rewrite() {
	tmp := h.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		log.Printf("压缩历史失败: %v", err)
		return
	}
	w := bufio.NewWriter(f)
	for _, r := range h.recs {
		line, _ := json.Marshal(r)
		w.Write(append(line, '\n'))
	}
	w.Flush()
	f.Close()
	if err := os.Rename(tmp, h.path); err != nil {
		log.Printf("压缩历史失败: %v", err)
	}
}

// List 倒序分页,q 匹配发件人/主题(不区分大小写),mailbox 非空时只看该邮箱。
// 返回的记录不含正文。
func (h *History) List(offset, limit int, q, mailbox string) (total int, items []*MailRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	q = strings.ToLower(q)
	var filtered []*MailRecord
	for i := len(h.recs) - 1; i >= 0; i-- {
		r := h.recs[i]
		if mailbox != "" && r.Mailbox != mailbox {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.From+" "+r.FromAddr+" "+r.Subject), q) {
			continue
		}
		filtered = append(filtered, r)
	}
	total = len(filtered)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	for _, r := range filtered[offset:end] {
		c := *r
		c.Body = ""
		c.BodyHTML = ""
		items = append(items, &c)
	}
	if items == nil {
		items = []*MailRecord{}
	}
	return total, items
}

func (h *History) Get(id int64) *MailRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.recs) - 1; i >= 0; i-- {
		if int64(h.recs[i].ID) == id {
			return h.recs[i]
		}
	}
	return nil
}

// Stats 统计面板数据:累计/今日计数、近 14 天每日收转、各规则命中数、各邮箱收信量。
func (h *History) Stats() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	type day struct {
		Date      string `json:"date"`
		Received  int    `json:"received"`
		Forwarded int    `json:"forwarded"`
		Failed    int    `json:"failed"`
	}
	now := time.Now()
	days := make([]*day, 14)
	dayIdx := map[string]*day{}
	for i := 0; i < 14; i++ {
		d := &day{Date: now.AddDate(0, 0, i-13).Format("01-02")}
		days[i] = d
		dayIdx[now.AddDate(0, 0, i-13).Format("2006-01-02")] = d
	}
	var total, matched, delivered, failed, todayRecv, todayFwd int
	today := now.Format("2006-01-02")
	ruleCount := map[string]int{}
	mboxCount := map[string]int{}
	for _, r := range h.recs {
		total++
		if r.Mailbox != "" {
			mboxCount[r.Mailbox]++
		}
		date := r.Time.Format("2006-01-02")
		if date == today {
			todayRecv++
		}
		if d := dayIdx[date]; d != nil {
			d.Received++
		}
		if r.Rule == "" {
			continue
		}
		matched++
		ruleCount[r.Rule]++
		ok := len(r.Results) > 0
		for _, res := range r.Results {
			if !res.OK {
				ok = false
			}
		}
		if ok {
			delivered++
			if date == today {
				todayFwd++
			}
			if d := dayIdx[date]; d != nil {
				d.Forwarded++
			}
		} else {
			failed++
			if d := dayIdx[date]; d != nil {
				d.Failed++
			}
		}
	}
	type nameCount struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	var rules []nameCount
	for name, c := range ruleCount {
		rules = append(rules, nameCount{name, c})
	}
	if rules == nil {
		rules = []nameCount{}
	}
	var mailboxes []nameCount
	for name, c := range mboxCount {
		mailboxes = append(mailboxes, nameCount{name, c})
	}
	if mailboxes == nil {
		mailboxes = []nameCount{}
	}
	return map[string]any{
		"total": total, "matched": matched, "delivered": delivered, "failed": failed,
		"today_received": todayRecv, "today_forwarded": todayFwd,
		"days": days, "rules": rules, "mailboxes": mailboxes,
	}
}
