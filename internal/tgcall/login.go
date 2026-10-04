package tgcall

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// PromptKind 输入提示的类型。
type PromptKind string

const (
	PromptCode     PromptKind = "code"     // Telegram 登录验证码
	PromptPassword PromptKind = "password" // 两步验证密码（未开启则不会被调用）
)

// Prompt 输入回调：CLI 里提示用户输入，测试里可以替换。
type Prompt func(ctx context.Context, kind PromptKind) (string, error)

// interactiveAuth 实现 auth.UserAuthenticator：
// 手机号取配置，验证码/两步验证密码经 Prompt 交互输入。
type interactiveAuth struct {
	phone  string
	prompt Prompt
}

func (a interactiveAuth) Phone(context.Context) (string, error) { return a.phone, nil }

func (a interactiveAuth) Password(ctx context.Context) (string, error) {
	return a.prompt(ctx, PromptPassword)
}

func (a interactiveAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.prompt(ctx, PromptCode)
}

func (a interactiveAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return nil
}

func (a interactiveAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("本程序不支持注册新 Telegram 账号")
}

// RunLogin 交互式登录系统的 TG 用户账号：
// Telegram 把验证码发到该账号（SMS 或已登录的 Telegram 客户端），
// 开了两步验证时再输入密码。成功后会话经 store 持久化
// （写入 notification_configs.tgcall_session），之后呼叫不再需要人工参与。
func RunLogin(ctx context.Context, p Params, store *SessionStore, prompt Prompt) error {
	if p.APIID == 0 || p.APIHash == "" {
		return fmt.Errorf("缺少 api_id/api_hash（到 my.telegram.org 申请）")
	}
	if p.Phone == "" {
		return fmt.Errorf("缺少系统 TG 账号手机号")
	}
	client := telegram.NewClient(p.APIID, p.APIHash, telegram.Options{
		SessionStorage: store,
	})
	return client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(interactiveAuth{phone: p.Phone, prompt: prompt}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("登录失败: %w", err)
		}
		return nil
	})
}
