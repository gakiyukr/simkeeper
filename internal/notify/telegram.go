package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// TelegramConfig Telegram 渠道配置。
type TelegramConfig struct {
	BotToken string
	ChatID   string
}

// telegramResponse 是 Bot API sendMessage 的响应骨架。
type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// SendTelegram 发送纯文本消息。
// 不带 parse_mode：号码/运营商里可能出现 < > &，纯文本最稳妥（与 PHP 版决策一致）。
func SendTelegram(client *http.Client, cfg TelegramConfig, message string) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	payload, err := json.Marshal(map[string]string{
		"chat_id": cfg.ChatID,
		"text":    message,
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.BotToken)
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("Telegram 网络错误: %w", err)
	}
	defer resp.Body.Close()
	var tr telegramResponse
	if err := decodeJSONBody(resp, &tr, "Telegram"); err != nil {
		return err
	}
	if !tr.OK {
		return fmt.Errorf("Telegram 发送失败: %s", tr.Description)
	}
	return nil
}
