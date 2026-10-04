package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WxPusherConfig WxPusher 渠道配置。
type WxPusherConfig struct {
	AppToken string
	UID      string
}

// wxpusherResponse 是 WxPusher 发送接口的响应骨架。
type wxpusherResponse struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
}

// SendWxPusher 发送文本消息；contentType=1 表示纯文本（与 PHP 版一致）。
func SendWxPusher(client *http.Client, cfg WxPusherConfig, subject, message string) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	payload, err := json.Marshal(map[string]any{
		"appToken":    cfg.AppToken,
		"content":     message,
		"summary":     subject,
		"contentType": 1,
		"uids":        []string{cfg.UID},
	})
	if err != nil {
		return err
	}
	resp, err := client.Post("https://wxpusher.zjiecode.com/api/send/message", "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("WxPusher 网络错误: %w", err)
	}
	defer resp.Body.Close()
	var wr wxpusherResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return fmt.Errorf("WxPusher 返回内容无法解析: %w", err)
	}
	if !wr.Success {
		return fmt.Errorf("WxPusher 发送失败: %s", wr.Msg)
	}
	return nil
}
