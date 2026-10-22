package secret

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// resetForTest 清空包级密钥状态，避免测试间互相影响。
func resetForTest(t *testing.T) {
	t.Helper()
	cleanup := func() {
		inited = false
		aead = nil
		key = nil
	}
	cleanup()
	t.Cleanup(cleanup)
}

// 回归：已有密钥文件长度不对时必须中止，不能静默生成新密钥覆盖——
// 覆盖会让数据库里所有 enc:v1: 密文永久无法解密。
func TestInitRejectsMalformedExistingKey(t *testing.T) {
	resetForTest(t)
	t.Setenv("SK_SECRET_KEY", "")

	path := filepath.Join(t.TempDir(), "secret.key")
	bad := []byte("truncated-key")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Init(path); err == nil {
		t.Fatal("密钥文件长度错误时应返回错误，而不是自动替换")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bad) {
		t.Fatalf("密钥文件被覆盖：got %q, want %q", got, bad)
	}
}

// 首次运行（文件不存在）仍应自动生成；生成后加解密可往返。
func TestInitGeneratesMissingKey(t *testing.T) {
	resetForTest(t)
	t.Setenv("SK_SECRET_KEY", "")

	path := filepath.Join(t.TempDir(), "nested", "secret.key")
	if err := Init(path); err != nil {
		t.Fatalf("首次运行应自动生成密钥: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 32 {
		t.Fatalf("生成的密钥应为 32 字节, got %d", len(b))
	}

	enc, err := Encrypt("hello secret")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(enc)
	if err != nil || plain != "hello secret" {
		t.Fatalf("加解密往返失败: %q / %v", plain, err)
	}
}

// 重启后应装载同一密钥，既有密文仍可解密。
func TestInitLoadsExistingKey(t *testing.T) {
	resetForTest(t)
	t.Setenv("SK_SECRET_KEY", "")

	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x42}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	enc, err := Encrypt("persisted")
	if err != nil {
		t.Fatal(err)
	}

	// 模拟进程重启：清空内存状态后重新装载同一文件
	inited, aead, key = false, nil, nil
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(enc)
	if err != nil || plain != "persisted" {
		t.Fatalf("重启后应能用原密钥解密: %q / %v", plain, err)
	}
}
