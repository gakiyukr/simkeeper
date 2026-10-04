// Package tgcall 通过 MTProto 用户账号实现「TG 未接来电闹铃」：
// 程序持有一个 Telegram 用户账号（需要到 my.telegram.org 申请 api_id/api_hash，
// 首次用 tg-login 子命令交互式登录一次），提醒触发时向指定账号发起 VoIP 呼叫，
// 主叫端随即断开——对方手机铃声会响起，最终显示为未接来电。
//
// 说明：Telegram Bot API 永远不能发起通话，这是用户账号（MTProto）独有的能力；
// 本包基于 gotd/td（纯 Go MTProto 客户端）。呼叫为「响铃即走」实现，
// 未实现完整 E2E 媒体协商（那是接通后才需要的），未在真实账号上联调过，
// 协议层参数如遇 Telegram 客户端更新可能需要调整 max_layer。
package tgcall

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// Params 呼叫所需配置，取自用户的通知渠道配置。
type Params struct {
	APIID   int
	APIHash string
	Phone   string // 系统 TG 账号手机号（登录时用；呼叫不需要）
	Session string // base64 的 MTProto 会话；空表示未登录
	Target  string // 被叫账号：@用户名 或 手机号（E.164）
}

// SessionStore 会话持久化：读/写 MTProto 会话字节串（实现方存进数据库）。
// 实现 session.Storage 接口（LoadSession/StoreSession）。
type SessionStore struct {
	Load func() ([]byte, error)
	Save func([]byte) error
}

// LoadSession 实现 session.Storage。
func (s *SessionStore) LoadSession(ctx context.Context) ([]byte, error) {
	if s.Load == nil {
		return nil, nil
	}
	return s.Load()
}

// StoreSession 实现 session.Storage。
func (s *SessionStore) StoreSession(ctx context.Context, data []byte) error {
	if s.Save == nil {
		return nil
	}
	return s.Save(data)
}

// PlaceMissedCall 向 Target 发起一次呼叫并随即断开（未接来电闹铃）。
// 调用方应传带超时的 ctx；返回的 session 若非空是 gotd 更新后的会话，
// 需要由调用方持久化。
func PlaceMissedCall(ctx context.Context, p Params, store *SessionStore) (newSession string, err error) {
	if p.APIID == 0 || p.APIHash == "" {
		return "", fmt.Errorf("TG 电话未配置 api_id/api_hash")
	}
	if p.Session == "" {
		return "", fmt.Errorf("TG 账号尚未登录，请先运行: baohaotong tg-login")
	}
	if p.Target == "" {
		return "", fmt.Errorf("TG 电话未配置被叫账号")
	}

	client := telegram.NewClient(p.APIID, p.APIHash, telegram.Options{
		SessionStorage: store,
	})
	runErr := client.Run(ctx, func(ctx context.Context) error {
		user, err := resolveUser(ctx, client.API(), p.Target)
		if err != nil {
			return fmt.Errorf("解析被叫账号 %s 失败: %w", p.Target, err)
		}
		// 呼叫为端到端加密，完整握手只在对方接听后才需要；
		// 闹铃场景对方不接，g_a 只需是「256 字节随机数的哈希」占位即可。
		gA := make([]byte, 256)
		if _, err := rand.Read(gA); err != nil {
			return fmt.Errorf("生成呼叫密钥失败: %w", err)
		}
		gAHash := sha256.Sum256(gA)
		if _, err := client.API().PhoneRequestCall(ctx, &tg.PhoneRequestCallRequest{
			UserID:   &tg.InputUser{UserID: user.ID, AccessHash: user.AccessHash},
			RandomID: int(time.Now().UnixNano() & 0x7fffffff),
			GAHash:   gAHash[:],
			Protocol: tg.PhoneCallProtocol{
				UDPP2P:          true,
				UDPReflector:    true,
				MinLayer:        92,
				MaxLayer:        158,
				LibraryVersions: []string{"gotd"},
			},
		}); err != nil {
			return fmt.Errorf("发起呼叫失败: %w", err)
		}
		// 让呼叫保持几秒再断开：铃声已触发，对方最终看到未接来电
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
		return nil
	})
	if runErr != nil {
		return "", runErr
	}
	// 会话可能被 gotd 更新（auth key 轮换等），交调用方回存
	if s, err := store.LoadSession(ctx); err == nil && len(s) > 0 {
		return string(s), nil
	}
	return "", nil
}

// resolveUser 把 "@用户名" 或手机号解析成被叫的 tg.User。
func resolveUser(ctx context.Context, api *tg.Client, target string) (*tg.User, error) {
	// 用户名：@前缀或含字母
	if len(target) > 0 && (target[0] == '@' || hasAlpha(target)) {
		name := target
		if name[0] == '@' {
			name = name[1:]
		}
		res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
			Username: name,
		})
		if err != nil {
			return nil, err
		}
		return firstUser(res.Users)
	}
	// 手机号：先导入联系人拿到 peer（不会产生好友邀请）
	phone := target
	if len(phone) > 0 && phone[0] == '+' {
		phone = phone[1:]
	}
	imported, err := api.ContactsImportContacts(ctx, []tg.InputPhoneContact{{
		ClientID:  1,
		Phone:     phone,
		FirstName: "SimKeeper",
	}})
	if err != nil {
		return nil, err
	}
	u, err := firstUser(imported.Users)
	if err != nil {
		return nil, fmt.Errorf("手机号未找到对应的 Telegram 账号")
	}
	return u, nil
}

func firstUser(users []tg.UserClass) (*tg.User, error) {
	for _, u := range users {
		if user, ok := u.(*tg.User); ok {
			return user, nil
		}
	}
	return nil, fmt.Errorf("未找到对应的 Telegram 账号")
}

func hasAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}
