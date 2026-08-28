package main

import "strings"

// Match 判断邮件是否命中规则:来源邮箱在限定列表内(留空=不限),
// 且 发件人包含任一子串(留空=不限),
// 且 主题或正文包含任一关键词(留空=不限)。文本匹配不区分大小写。
func (r *Rule) Match(m *Mail) bool {
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

func containsAny(hay string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(hay, strings.ToLower(n)) {
			return true
		}
	}
	return false
}
