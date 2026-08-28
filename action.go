package main

import "fmt"

// Action 是命中规则后执行的动作。扩展新动作(webhook、Telegram、执行命令等)时:
// 实现该接口 + 在 buildActions 里根据规则字段追加实例即可,其余代码不用动。
type Action interface {
	Name() string
	Target() string // 送达目标的展示文本,历史记录用
	Execute(m *Mail) error
}

// buildActions 根据规则配置组装动作列表。
func buildActions(cfg *Config, r *Rule) ([]Action, error) {
	var actions []Action
	if len(r.ForwardTo) > 0 {
		if cfg.SMTP.Host == "" || cfg.SMTP.User == "" {
			return nil, fmt.Errorf("规则[%s]配置了 forward_to 但 [smtp] 未配置", r.Name)
		}
		actions = append(actions, &ForwardAction{smtp: &cfg.SMTP, to: r.ForwardTo, attachOriginal: r.AttachOriginal})
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("规则[%s]没有配置任何动作(如 forward_to)", r.Name)
	}
	return actions, nil
}
