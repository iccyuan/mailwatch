// Package rules 实现规则匹配:纯函数,无状态。
package rules

import (
	"strings"

	"mailwatch/internal/config"
	"mailwatch/internal/mail"
)

// Match 判断邮件是否命中规则:来源邮箱在限定列表内(留空=不限),
// 且 发件人包含任一子串(留空=不限),
// 且 主题或正文包含任一关键词(留空=不限)。文本匹配不区分大小写。
// 注意:Match 不看 Disabled,停用过滤在 FirstMatch 做。
func Match(r *config.Rule, m *mail.Mail) bool {
	if len(r.Mailboxes) > 0 {
		found := false
		for _, name := range r.Mailboxes {
			if name == m.Mailbox {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(r.FromContains) > 0 {
		hay := strings.ToLower(m.From + " " + m.FromAddr)
		if !containsAny(hay, r.FromContains) {
			return false
		}
	}
	if len(r.Keywords) > 0 {
		hay := strings.ToLower(m.Subject + "\n" + m.Body)
		if !containsAny(hay, r.Keywords) {
			return false
		}
	}
	return true
}

// FirstMatch 返回第一条命中且未停用的规则下标;都不命中返回 -1。
// 规则顺序即优先级。
func FirstMatch(rs []config.Rule, m *mail.Mail) int {
	for i := range rs {
		if rs[i].Disabled {
			continue
		}
		if Match(&rs[i], m) {
			return i
		}
	}
	return -1
}

func containsAny(hay string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(hay, strings.ToLower(n)) {
			return true
		}
	}
	return false
}
