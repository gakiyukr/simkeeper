package notify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 签名算法的参考向量由独立实现（Python hmac 标准库）按官方文档算法计算生成，
// 防止 Go 实现与文档出现一致性的偏差。飞书与钉钉的算法互为镜像
//（密钥与数据互换），两组向量一起能抓住写反的情况。

func TestFeishuSign(t *testing.T) {
	// ts=1700000000, secret="testsecret"
	got := feishuSign("testsecret", 1700000000)
	want := "AOc8oJ7//5OlQlfWC3nRL0R+IkuzcD1FKcAyibRK9Q8="
	if got != want {
		t.Errorf("feishuSign = %q, want %q", got, want)
	}
	// 不同时间戳必须产生不同签名
	if feishuSign("testsecret", 1700000001) == want {
		t.Error("不同时间戳不应产生相同签名")
	}
}

func TestDingTalkSign(t *testing.T) {
	// ts=1700000000000(ms), secret="testsecret"
	got := dingTalkSign("testsecret", 1700000000000)
	want := "d043yCasNZ+KC1N0lrVg+Aan0gEIKPvRfzRqMlUUwzk="
	if got != want {
		t.Errorf("dingTalkSign = %q, want %q", got, want)
	}
	if dingTalkSign("testsecret", 1700000000001) == want {
		t.Error("不同时间戳不应产生相同签名")
	}
}

func TestChannelSenderNonNilClient(t *testing.T) {
	// 未传 HTTPClient 时各发送器自建默认客户端；这里传短超时客户端，
	// 让「连真实域名但不可达」的用例快速失败，而不是等默认 15s 超时。
	fast := &http.Client{Timeout: 500 * time.Millisecond}
	err := SendFeishu(fast, FeishuConfig{Webhook: "http://127.0.0.1:1/nope"}, "x")
	if err == nil {
		t.Error("不可达 webhook 应返回错误")
	}
	err = SendDingTalk(fast, DingTalkConfig{Webhook: "http://127.0.0.1:1/nope"}, "x")
	if err == nil {
		t.Error("不可达 webhook 应返回错误")
	}
	err = SendTelegram(fast, TelegramConfig{BotToken: "1:x", ChatID: "1"}, "x")
	if err == nil {
		t.Error("不可达 API 应返回错误")
	}
	err = SendWxPusher(fast, WxPusherConfig{AppToken: "x", UID: "y"}, "t", "x")
	if err == nil {
		t.Error("不可达 API 应返回错误")
	}
}

// TestDecodeJSONBodyRejectsNon2xx 回归：HTTP 状态码非 2xx 时必须报错，
// 即使响应体看起来能解码——网关错误页缺失业务字段，零值解码会被
// 误判为发送成功（失败告警与重投随之失效）。
func TestDecodeJSONBodyRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"gateway error"}`))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Code int `json:"code"`
	}
	err = decodeJSONBody(resp, &payload, "飞书")
	if err == nil {
		t.Fatal("HTTP 502 应返回错误，即使响应体能解码")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("错误信息应包含状态码, got %v", err)
	}
}
