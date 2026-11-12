package monitor

import (
	"arkade/hub/internal/notify"
	"arkade/hub/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gogf/gf/v2/errors/gerror"
	"time"
)

func (s *Service) channel(ctx context.Context, kind string) (notify.ChannelConfig, error) {
	_, data, secrets, err := s.load(ctx)
	if err != nil {
		return notify.ChannelConfig{}, err
	}
	switch kind {
	case "dingtalk":
		return notify.ChannelConfig{Kind: kind, DingTalk: secrets.DingTalk}, nil
	case "email":
		return notify.ChannelConfig{Kind: kind, Email: emailConfig(data.Email, secrets.EmailPassword)}, nil
	}
	return notify.ChannelConfig{}, gerror.New("不支持的通知通道")
}
func (s *Service) Notify(ctx context.Context, event Event) error {
	channel, err := s.channel(ctx, event.Channel)
	if err != nil {
		return err
	}
	label := map[string]string{"firing": "异常告警", "repeat": "异常持续", "resolved": "恢复通知"}[event.Event]
	if label == "" {
		label = "监控通知"
	}
	var detail struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal([]byte(event.Incident.DetailJSON), &detail)
	// 只通知业务摘要，不暴露引擎内部退避/投递状态或通道配置。
	return s.sender.Send(ctx, channel, notify.Message{Title: "Arkade " + label, Body: fmt.Sprintf("%s\n服务器：%s\n容器：%s\n类型：%s\n详情：%s", label, event.Incident.AgentID, event.Incident.ContainerID, event.Incident.Kind, detail.Detail)})
}
func (s *Service) Test(ctx context.Context, kind string) error {
	channel, err := s.channel(ctx, kind)
	if err != nil {
		return err
	}
	if err = notify.ValidateChannel(channel); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sendErr := s.sender.Send(sendCtx, channel, notify.Message{Title: "Arkade 通知测试", Body: "这是管理员从后台主动发送的测试通知，不代表发生了故障。"})
	status, message := "sent", ""
	if sendErr != nil {
		status = "failed"
		message = "测试发送失败，请检查通道地址、凭据、TLS及网络"
	}
	// 浏览器取消不应抹去已经发生的外部发送结果。
	recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer recordCancel()
	recordErr := s.db.AddMonitorDelivery(recordCtx, store.MonitorDelivery{ID: fmt.Sprintf("test-%d", time.Now().UnixNano()), Channel: kind, Event: "test", Status: status, Error: message, CreatedAt: time.Now().Unix()})
	if sendErr != nil {
		return gerror.New(message)
	}
	return recordErr
}
