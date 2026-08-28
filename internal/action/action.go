// Package action 定义规则命中后的动作接口与实现。
// 扩展新动作(webhook、Telegram、执行命令等)时:实现 Action 接口 +
// 在 Build 里根据规则字段追加实例即可,其余代码不用动。
package action

import (
	"fmt"

	"mailwatch/internal/config"
	"mailwatch/internal/mail"
)

// Action 是命中规则后执行的动作。
type Action interface {
	Name() string
	Target() string // 送达目标的展示文本,历史记录用
	Execute(m *mail.Mail) error
}

// Build 根据规则配置组装动作列表。
func Build(cfg *config.Config, r *config.Rule) ([]Action, error) {
	var actions []Action
	if len(r.ForwardTo) > 0 {
		if cfg.SMTP.Host == "" || cfg.SMTP.User == "" {
			return nil, fmt.Errorf("规则[%s]配置了 forward_to 但 [smtp] 未配置", r.Name)
		}
		actions = append(actions, &Forward{
			SMTP: &cfg.SMTP, To: r.ForwardTo, AttachOriginal: r.AttachOriginal,
		})
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("规则[%s]没有配置任何动作(如 forward_to)", r.Name)
	}
	return actions, nil
}
