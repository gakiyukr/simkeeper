package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// FeishuConfig 飞书群自定义机器人渠道。
// webhook 在飞书群「设置 → 群机器人 → 自定义机器人」创建后获取；
// 开启「签名校验」时需同时填 secret。
type FeishuConfig struct {
	Webhook string
	Secret  string
}

// feishuSign 飞书签名：把 timestamp+"\n"+secret 作为 HMAC-SHA256 的密钥、
// 消息体为空，取 base64。（注意与钉钉的算法互为镜像：密钥和数据互换。）
// 算法来自飞书官方文档，签名向量在 notify_test.go 固化。
func feishuSign(secret string, ts int64) string {
	stringToSign := strconv.FormatInt(ts, 10) + "\n" + secret
	h := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// feishuResponse 兼容新（code/msg）旧（StatusCode/StatusMessage）两种响应格式。
type feishuResponse struct {
	Code          int    `json:"code"`
	Msg           string `json:"msg"`
	StatusCode    int    `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

// SendFeishu 发送纯文本消息到飞书群。
func SendFeishu(client *http.Client, cfg FeishuConfig, message string) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Webhook == "" {
		return fmt.Errorf("飞书 Webhook 未配置")
	}
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": message},
	}
	if cfg.Secret != "" {
		ts := time.Now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = feishuSign(cfg.Secret, ts)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := client.Post(cfg.Webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("飞书网络错误: %w", err)
	}
	defer resp.Body.Close()
	var fr feishuResponse
	if err := decodeJSONBody(resp, &fr, "飞书"); err != nil {
		return err
	}
	switch {
	case fr.Code != 0:
		return fmt.Errorf("飞书发送失败: code=%d %s", fr.Code, fr.Msg)
	case fr.StatusCode != 0:
		return fmt.Errorf("飞书发送失败: code=%d %s", fr.StatusCode, fr.StatusMessage)
	}
	return nil
}
