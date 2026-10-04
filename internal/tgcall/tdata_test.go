package tgcall

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
	"github.com/gotd/td/session/tdesktop"
)

// 真实 tdata 需要真实账号，这里覆盖两段可确定性测试的逻辑：
// 会话格式转换、ZIP 内 tdata 根目录定位。

func TestAccountToSession(t *testing.T) {
	var authKey crypto.Key
	for i := range authKey {
		authKey[i] = byte(i)
	}
	acc := tdesktop.Account{
		IDx: 1,
		Authorization: tdesktop.MTPAuthorization{
			UserID: 42,
			MainDC: 2,
			Keys:   map[int]crypto.Key{2: authKey},
		},
	}
	b64, err := accountToSession(acc)
	if err != nil {
		t.Fatalf("accountToSession: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	var wrapper struct {
		Version int
		Data    session.Data
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if wrapper.Version != 1 {
		t.Errorf("session version = %d, want 1", wrapper.Version)
	}
	if wrapper.Data.DC != 2 {
		t.Errorf("DC = %d, want 2", wrapper.Data.DC)
	}
	if string(wrapper.Data.AuthKey) != string(authKey[:]) {
		t.Error("AuthKey 与源密钥不一致")
	}
	wantID := sha1.Sum(authKey[:])
	if string(wrapper.Data.AuthKeyID) != string(wantID[:12]) {
		t.Error("AuthKeyID 应为 SHA1(auth_key) 前 12 字节")
	}
}

func TestAccountToSessionNoKeys(t *testing.T) {
	acc := tdesktop.Account{Authorization: tdesktop.MTPAuthorization{UserID: 1, MainDC: 2}}
	if _, err := accountToSession(acc); err == nil {
		t.Error("无密钥的账号应返回错误")
	}
}

func mustZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestImportTDataLocatesRoot(t *testing.T) {
	// 带父目录的布局：Telegram Desktop/tdata/key_datas + data
	data := mustZip(t, map[string]string{
		"Telegram Desktop/tdata/key_datas": "\x00garbage",
		"Telegram Desktop/tdata/data":      "\x00garbage",
		"readme.txt":                       "not part of tdata",
	})
	_, _, err := ImportTData(data, "")
	if err == nil {
		t.Fatal("垃圾内容应解密失败")
	}
	// 关键断言：错误是解密/解析层面，而不是「没找到 tdata」
	if strings.Contains(err.Error(), "没找到 tdata") {
		t.Errorf("应能定位到 tdata 根目录，实际错误: %v", err)
	}

	// 平铺布局：key_data 直接在压缩包根
	flat := mustZip(t, map[string]string{
		"key_data": "\x00garbage",
		"data":     "\x00garbage",
	})
	if _, _, err := ImportTData(flat, ""); err == nil || strings.Contains(err.Error(), "没找到 tdata") {
		t.Errorf("平铺布局也应定位成功并进入解密阶段，实际: %v", err)
	}

	// 完全无关的 zip：应明确报「没找到 tdata」
	other := mustZip(t, map[string]string{"foo.txt": "x"})
	if _, _, err := ImportTData(other, ""); err == nil || !strings.Contains(err.Error(), "没找到 tdata") {
		t.Errorf("无关 zip 应报「没找到 tdata」，实际: %v", err)
	}

	// 非 zip 文件
	if _, _, err := ImportTData([]byte("hello"), ""); err == nil || !strings.Contains(err.Error(), "ZIP") {
		t.Errorf("非 zip 应报「不是有效的 ZIP」，实际: %v", err)
	}
	// 空文件
	if _, _, err := ImportTData(nil, ""); err == nil {
		t.Error("空上传应报错")
	}
}
