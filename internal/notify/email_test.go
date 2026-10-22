package notify

import (
	"bufio"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

// 回归：tls 模式遇到不支持 STARTTLS 的服务器必须失败，不能静默降级明文，
// 更不能继续发送 AUTH 凭据。
func TestSendEmailTLSRequiresSTARTTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	authSeen := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(conn)
		bw := bufio.NewWriter(conn)
		write := func(s string) {
			_, _ = bw.WriteString(s)
			_ = bw.Flush()
		}
		write("220 test ESMTP\r\n")
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				// 故意不宣告 STARTTLS，只提供 AUTH LOGIN
				write("250-test\r\n250 AUTH LOGIN\r\n")
			case strings.HasPrefix(cmd, "AUTH"):
				select {
				case authSeen <- struct{}{}:
				default:
				}
				write("235 ok\r\n")
			case strings.HasPrefix(cmd, "QUIT"):
				write("221 bye\r\n")
				return
			default:
				write("250 ok\r\n")
			}
		}
	}()

	err = SendEmail(EmailConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Secure:   "tls",
		Username: "user",
		Password: "secret",
		From:     "from@example.com",
	}, "to@example.com", "subject", "<b>html</b>", "text")

	if err == nil {
		t.Fatal("服务器未提供 STARTTLS 时 tls 模式应报错，不得明文继续")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("错误信息应说明缺少 STARTTLS, got %v", err)
	}
	select {
	case <-authSeen:
		t.Fatal("不应在未加密连接上发送 AUTH 凭据")
	default:
	}

	_ = ln.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("测试 SMTP 服务未在预期时间内结束")
	}
}

// loginAuth 与标准库 PlainAuth 对齐：远程未加密连接拒绝发送凭据；
// TLS 连接或本机中继允许。
func TestLoginAuthRefusesPlaintextRemote(t *testing.T) {
	a := &loginAuth{username: "user", password: "secret"}

	if _, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Error("远程明文连接不应允许 AUTH LOGIN")
	}
	if method, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true}); err != nil || method != "LOGIN" {
		t.Errorf("TLS 连接应返回 LOGIN, got method=%q err=%v", method, err)
	}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "127.0.0.1", TLS: false}); err != nil {
		t.Errorf("本机中继应允许明文 AUTH LOGIN: %v", err)
	}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "localhost", TLS: false}); err != nil {
		t.Errorf("localhost 中继应允许明文 AUTH LOGIN: %v", err)
	}
}
