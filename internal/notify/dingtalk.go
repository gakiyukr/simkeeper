package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DingTalkConfig 钉钉群自定义机器人渠道。
// webhook 在钉钉群「群设置 → 机器人 → 自定义机器人」创建后获取。
// 安全设置三选一：自定义关键词（消息需含关键词，secret 留空）、
// 加签（填 secret）、IP 白名单。推荐加签或关键词。
type DingTalkConfig struct {
	Webhook string
	Secret  string
}

// dingTalkSign 钉钉签名：以 secret 为 HMAC-SHA256 密钥、数据为
// 毫秒时间戳+"\n"+secret，取 base64 后 URL 编码拼进 webhook 地址。
// 算法来自钉钉官方文档，签名向量在 notify_test.go 固化。
func dingTalkSign(secret string, tsMillis int64) string {
	stringToSign := strconv.FormatInt(tsMillis, 10) + "\n" + secret
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// dingTalkResponse 是 robot/send 的响应骨架。
type dingTalkResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// SendDingTalk 发送纯文本消息到钉钉群。
func SendDingTalk(client *http.Client, cfg DingTalkConfig, message string) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Webhook == "" {
		return fmt.Errorf("钉钉 Webhook 未配置")
	}
	webhook := cfg.Webhook
	if cfg.Secret != "" {
		ts := time.Now().UnixMilli()
		q := url.Values{}
		q.Set("timestamp", strconv.FormatInt(ts, 10))
		q.Set("sign", dingTalkSign(cfg.Secret, ts))
		sep := "&"
		if !strings.Contains(webhook, "?") {
			sep = "?"
		}
		webhook += sep + q.Encode()
	}
	body, err := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": message},
	})
	if err != nil {
		return err
	}
	resp, err := client.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("钉钉网络错误: %w", err)
	}
	defer resp.Body.Close()
	var dr dingTalkResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return fmt.Errorf("钉钉返回内容无法解析: %w", err)
	}
	if dr.ErrCode != 0 {
		return fmt.Errorf("钉钉发送失败: errcode=%d %s", dr.ErrCode, dr.ErrMsg)
	}
	return nil
}
