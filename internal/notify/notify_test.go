package notify

import (
	"testing"
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
	// 未传 HTTPClient 时各发送器应自建默认客户端而不 panic；
	// 用不可达地址快速走一遍错误路径。
	err := SendFeishu(nil, FeishuConfig{Webhook: "http://127.0.0.1:1/nope"}, "x")
	if err == nil {
		t.Error("不可达 webhook 应返回错误")
	}
	err = SendDingTalk(nil, DingTalkConfig{Webhook: "http://127.0.0.1:1/nope"}, "x")
	if err == nil {
		t.Error("不可达 webhook 应返回错误")
	}
	err = SendTelegram(nil, TelegramConfig{BotToken: "1:x", ChatID: "1"}, "x")
	if err == nil {
		t.Error("不可达 API 应返回错误")
	}
	err = SendWxPusher(nil, WxPusherConfig{AppToken: "x", UID: "y"}, "t", "x")
	if err == nil {
		t.Error("不可达 API 应返回错误")
	}
}
