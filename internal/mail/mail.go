// Package mail 定义解析后的邮件模型,并从 RFC822 原始字节解析。
package mail

import (
	"bytes"
	"io"
	"log"
	"mime"
	"regexp"
	"strings"

	_ "github.com/emersion/go-message/charset" // 注册 GBK/GB2312 等常见编码
	gomail "github.com/emersion/go-message/mail"
)

// Mail 解析后的一封邮件,交给规则匹配和 Action 使用。
type Mail struct {
	Mailbox  string // 来源邮箱(MailboxConfig.Name)
	UID      uint32
	From     string // 原始 From 头(含显示名)
	FromAddr string // 纯地址
	To       string
	Date     string
	Subject  string
	Body     string // 文本正文(text/plain 优先,否则 html 去标签),用于关键词匹配
	Raw      []byte // 原始 RFC822 字节,转发时作为附件
}

var htmlTagRe = regexp.MustCompile(`(?s)<style.*?</style>|<script.*?</script>|<[^>]+>`)

// Parse 从原始字节解析出头部与文本正文。解析失败不致命,能填多少填多少。
func Parse(uid uint32, raw []byte) *Mail {
	m := &Mail{UID: uid, Raw: raw}
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		log.Printf("UID %d 邮件解析失败: %v", uid, err)
		return m
	}
	h := mr.Header
	m.Subject, _ = h.Subject()
	m.Date = h.Get("Date")
	m.To = decodeHeader(h.Get("To"))
	m.From = decodeHeader(h.Get("From"))
	if addrs, err := h.AddressList("From"); err == nil && len(addrs) > 0 {
		m.FromAddr = addrs[0].Address
	}

	var plain, html string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			log.Printf("UID %d 正文部件解析失败: %v", uid, err)
			break
		}
		if ih, ok := p.Header.(*gomail.InlineHeader); ok {
			ct, _, _ := ih.ContentType()
			b, _ := io.ReadAll(p.Body)
			switch ct {
			case "text/plain":
				if plain == "" {
					plain = string(b)
				}
			case "text/html":
				if html == "" {
					html = string(b)
				}
			}
		}
	}
	if plain != "" {
		m.Body = plain
	} else if html != "" {
		m.Body = strings.TrimSpace(htmlTagRe.ReplaceAllString(html, " "))
	}
	return m
}

func decodeHeader(s string) string {
	dec := new(mime.WordDecoder)
	if d, err := dec.DecodeHeader(s); err == nil {
		return d
	}
	return s
}
