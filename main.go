// SimKeeper Go 版入口。
//
// 单个静态二进制，内嵌模板与 schema，无需外部依赖：
//
//	./simkeeper                                    # SQLite：data/simkeeper.db，监听 127.0.0.1:8080
//	./simkeeper -driver mysql  -dsn 'user:pass@tcp(127.0.0.1:3306)/simkeeper?charset=utf8mb4'
//	./simkeeper -driver postgres -dsn 'postgres://user:pass@127.0.0.1:5432/simkeeper?TimeZone=Asia/Shanghai'
//
// 对应环境变量：SK_DRIVER / SK_DB / SK_DSN / SK_ADDR / SK_TRUST_PROXY / SK_CRON_INTERVAL。
// 首次启动访问 /setup 创建管理员（创建后入口永久关闭）。
package main

import (
	"bufio"
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"simkeeper/internal/auth"
	"simkeeper/internal/cronjob"
	"simkeeper/internal/db"
	"simkeeper/internal/secret"
	"simkeeper/internal/store"
	"simkeeper/internal/tgcall"
	"simkeeper/internal/web"
)

// version 由 release 工作流通过 -ldflags "-X main.version=..." 注入；
// 本地 go build 为 dev。
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags)

	// tg-login 子命令：为某个用户的 TG 电话渠道交互式登录 MTProto 账号。
	// 用法：simkeeper tg-login -username <用户名> [-driver ... -db/-dsn ...]
	if len(os.Args) > 1 && os.Args[1] == "tg-login" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		tgLogin(os.Args)
		return
	}

	// reset-password 子命令：重置账号密码（邮件找回渠道不可用时的兜底）。
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		resetPassword(os.Args)
		return
	}

	var (
		driver = flag.String("driver", envOr("SK_DRIVER", db.DialectSQLite),
			"数据库驱动：sqlite（默认）/ mysql / postgres")
		dbPath = flag.String("db", envOr("SK_DB", "data/simkeeper.db"),
			"SQLite 数据库文件路径（仅 driver=sqlite 时使用）")
		dsn = flag.String("dsn", envOr("SK_DSN", ""),
			"MySQL/PostgreSQL 连接串，例如 "+
				`mysql: 'user:pass@tcp(127.0.0.1:3306)/simkeeper?charset=utf8mb4'；`+
				`postgres: 'postgres://user:pass@127.0.0.1:5432/simkeeper?TimeZone=Asia/Shanghai'`)
		addr = flag.String("addr", envOr("SK_ADDR", "127.0.0.1:8080"),
			"HTTP 监听地址")
		trustProxy = flag.Bool("trust-proxy", envOr("SK_TRUST_PROXY", "") == "1",
			"信任 X-Forwarded-For 头（部署在可信反向代理之后时开启，用于登录限流的 IP 归属；取链最右值，客户端伪造的前缀无效）")
		secureCookies = flag.Bool("secure-cookies", envOr("SK_SECURE_COOKIES", "") == "1",
			"会话 Cookie 强制加 Secure 标记（TLS 由反向代理终结时必须开启）")
		secretKeyFile = flag.String("secret-key-file", envOr("SK_SECRET_KEY_FILE", ""),
			"凭据加密密钥文件（默认 data/secret.key；设置环境变量 SK_SECRET_KEY 时优先）")
		cronInterval = flag.Duration("cron-interval", durationOr("SK_CRON_INTERVAL", time.Hour),
			"定时任务执行间隔（如 30m、1h）；0 表示禁用进程内调度，仅保留管理后台手动触发")
		showVersion = flag.Bool("version", false, "打印版本号后退出")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("simkeeper " + version)
		return
	}

	if err := run(*driver, *dbPath, *dsn, *addr, *trustProxy, *secureCookies, *secretKeyFile, *cronInterval); err != nil {
		log.Fatalf("[main] %v", err)
	}
}

// secretKeyPath 计算凭据加密密钥文件路径：SQLite 默认与数据库同目录，
// 其余驱动默认工作目录下的 data/secret.key。显式 flag/环境变量优先。
func secretKeyPath(driver, dbPath, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if driver == db.DialectSQLite {
		return filepath.Join(filepath.Dir(dbPath), "secret.key")
	}
	return "data/secret.key"
}

func run(driver, dbPath, dsn, addr string, trustProxy, secureCookies bool, secretKeyFile string, cronInterval time.Duration) error {
	if err := secret.Init(secretKeyPath(driver, dbPath, secretKeyFile)); err != nil {
		return err
	}
	if driver == db.DialectSQLite {
		// SQLite 的「连接串」就是文件路径，目录按需创建
		if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("创建数据目录失败: %w", err)
			}
		}
		dsn = dbPath
	}
	database, err := db.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		return err
	}

	app, err := web.New(database)
	if err != nil {
		return fmt.Errorf("初始化应用失败: %w", err)
	}
	app.TrustProxy = trustProxy
	app.SecureCookies = secureCookies
	app.Version = version
	if err := app.Settings.SeedDefaults(); err != nil {
		return fmt.Errorf("写入默认设置失败: %w", err)
	}

	// 时区自检：提醒的「当天」判断依赖本地时区，部署时区错了提醒会错位
	zoneName, offset := time.Now().Zone()
	log.Printf("[main] 本地时区: %s (UTC%+03d:%02d) — 与预期不符请设置 TZ 环境变量",
		zoneName, offset/3600, (offset%3600)/60)

	// 历史明文凭据惰性迁移为密文（幂等，无明文时为空操作）
	if n, err := app.Notify.EncryptPlainConfigs(); err != nil {
		log.Printf("[main] 历史明文凭据迁移失败: %v", err)
	} else if n > 0 {
		log.Printf("[main] 已加密 %d 行渠道凭据", n)
	}

	var userCount int
	if err := app.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&userCount); err == nil && userCount == 0 {
		log.Printf("[main] 系统还没有账号：请访问 http://%s/setup 创建账号（创建后入口自动关闭）", addr)
	}

	// 定时任务与 HTTP 服务共用生命周期
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cronjob.New(database).Start(ctx, cronInterval)

	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("[main] SimKeeper %s 已启动（驱动 %s）: http://%s", version, driver, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[main] HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("[main] 收到退出信号，正在关闭…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("关闭 HTTP 服务失败: %w", err)
	}
	log.Printf("[main] 已退出")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// tgLogin 为 TG 电话渠道做一次交互式登录：
// 从库里读指定用户的 api_id/api_hash/手机号，提示输入 Telegram 验证码
// （开了两步验证再输入密码），成功后把会话写回 notification_configs。
func tgLogin(args []string) {
	fs := flag.NewFlagSet("tg-login", flag.ExitOnError)
	var (
		driver = fs.String("driver", envOr("SK_DRIVER", db.DialectSQLite), "数据库驱动")
		dbPath = fs.String("db", envOr("SK_DB", "data/simkeeper.db"), "SQLite 数据库路径")
		dsn    = fs.String("dsn", envOr("SK_DSN", ""), "MySQL/PostgreSQL 连接串")
		user   = fs.String("username", "", "要登录哪个用户的 TG 电话渠道（登录页用的用户名）")
		keyFlg = fs.String("secret-key-file", envOr("SK_SECRET_KEY_FILE", ""), "凭据加密密钥文件")
	)
	// args[0] 是程序路径（非 flag），必须跳过，否则解析在首个元素就停止、
	// 全部 flag 落回默认值
	_ = fs.Parse(args[1:])
	if *user == "" {
		fmt.Fprintln(os.Stderr, "用法: simkeeper tg-login -username <用户名>")
		os.Exit(2)
	}
	if err := secret.Init(secretKeyPath(*driver, *dbPath, *keyFlg)); err != nil {
		log.Fatalf("[tg-login] %v", err)
	}

	database, err := db.Open(*driver, pickDSN(*driver, *dbPath, *dsn))
	if err != nil {
		log.Fatalf("[tg-login] %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		log.Fatalf("[tg-login] %v", err)
	}

	repos := struct {
		users  *store.UserRepo
		notify *store.NotifyRepo
	}{
		users:  &store.UserRepo{DB: database},
		notify: &store.NotifyRepo{DB: database},
	}
	target, err := repos.users.ByUsername(*user)
	if err != nil {
		log.Fatalf("[tg-login] 找不到用户 %q", *user)
	}
	cfg, err := repos.notify.ConfigForUser(target.ID)
	if err != nil {
		log.Fatalf("[tg-login] 读取通知配置失败: %v", err)
	}
	if cfg.IsZero() || cfg.TGCallAPIID == 0 || cfg.TGCallAPIHash == "" {
		log.Fatalf("[tg-login] 该用户尚未在「通知配置」页填写 TG 电话的 api_id/api_hash，请先保存配置")
	}

	fmt.Printf("将为账号 %q 的 TG 电话渠道登录 Telegram（手机号 %s）。\n", target.Username, cfg.TGCallPhone)
	fmt.Println("验证码将发送到该 TG 账号（短信或已登录的 Telegram 客户端）。")
	store := &tgcall.SessionStore{
		Load: func() ([]byte, error) { return base64.StdEncoding.DecodeString(cfg.TGCallSession) },
		Save: func(b []byte) error {
			return repos.notify.SaveTGCallSession(target.ID, base64.StdEncoding.EncodeToString(b))
		},
	}
	reader := bufio.NewReader(os.Stdin)
	ask := func(label string) (string, error) {
		fmt.Print(label)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line), nil
	}
	prompt := func(ctx context.Context, kind tgcall.PromptKind) (string, error) {
		if kind == tgcall.PromptPassword {
			return ask("该账号开启了两步验证，请输入密码: ")
		}
		return ask("请输入验证码: ")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := tgcall.RunLogin(ctx, tgcall.Params{
		APIID:   cfg.TGCallAPIID,
		APIHash: cfg.TGCallAPIHash,
		Phone:   cfg.TGCallPhone,
		Session: cfg.TGCallSession,
	}, store, prompt); err != nil {
		log.Fatalf("[tg-login] %v", err)
	}
	fmt.Println("登录成功，会话已保存到数据库。TG 电话渠道现在可以使用了。")
}

// pickDSN 根据驱动决定连接串（与 run 的规则一致）。
func pickDSN(driver, dbPath, dsn string) string {
	if driver == db.DialectSQLite {
		return dbPath
	}
	return dsn
}

func durationOr(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return fallback
}

// resetPassword 重置账号密码：生成一次性临时密码并打印（仅此一次），
// 重置后该账号的全部登录会话立即失效。单账号自救路径——邮件找回渠道
// 不可用时的兜底。用法：
//
//	simkeeper reset-password [-clear-totp] [-username 用户名] [-driver ... -db/-dsn ...]
//
// -clear-totp 同时清除两步验证绑定（验证器丢失时使用，之后在个人设置重新绑定）。
// 单账号系统省略 -username 即自动定位唯一账号（历史多账号库需指定）。
func resetPassword(args []string) {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	var (
		driver    = fs.String("driver", envOr("SK_DRIVER", db.DialectSQLite), "数据库驱动")
		dbPath    = fs.String("db", envOr("SK_DB", "data/simkeeper.db"), "SQLite 数据库路径")
		dsn       = fs.String("dsn", envOr("SK_DSN", ""), "MySQL/PostgreSQL 连接串")
		user      = fs.String("username", "", "要重置的账号用户名（单账号系统可省略）")
		clearTOTP = fs.Bool("clear-totp", false, "同时关闭该账号的两步验证（验证器丢失时使用）")
	)
	// args[0] 是程序路径（非 flag），必须跳过，否则解析在首个元素就停止、
	// 全部 flag 落回默认值
	_ = fs.Parse(args[1:])

	if *driver == db.DialectSQLite {
		if dir := filepath.Dir(*dbPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				log.Fatalf("[reset-password] 创建数据目录失败: %v", err)
			}
		}
	}

	database, err := db.Open(*driver, pickDSN(*driver, *dbPath, *dsn))
	if err != nil {
		log.Fatalf("[reset-password] %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		log.Fatalf("[reset-password] %v", err)
	}

	users := &store.UserRepo{DB: database}
	var target *store.User
	if *user != "" {
		target, err = users.ByUsername(*user)
		if err != nil {
			log.Fatalf("[reset-password] 找不到用户 %q", *user)
		}
	} else {
		// 未指定用户名：系统应只有唯一账号（历史多账号库要求显式指定）
		var candidates []store.User
		rows, qerr := database.Query(`SELECT id, username FROM users ORDER BY id`)
		if qerr != nil {
			log.Fatalf("[reset-password] %v", qerr)
		}
		for rows.Next() {
			var u store.User
			if err := rows.Scan(&u.ID, &u.Username); err != nil {
				log.Fatalf("[reset-password] %v", err)
			}
			candidates = append(candidates, u)
		}
		_ = rows.Close()
		switch {
		case len(candidates) == 0:
			log.Fatalf("[reset-password] 系统还没有任何账号，请先访问 /setup 创建")
		case len(candidates) > 1:
			log.Fatalf("[reset-password] 系统中有 %d 个账号（历史数据），请用 -username 指定要重置的账号", len(candidates))
		}
		target = &candidates[0]
	}

	fmt.Printf("将重置账号 %q 的登录密码，该账号的所有登录会话将立即失效", target.Username)
	if *clearTOTP {
		fmt.Print("，并关闭其两步验证（验证器需重新绑定）")
	}
	fmt.Print("。继续？(y/N) ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		fmt.Println("已取消")
		return
	}

	// 一次性临时密码：去掉易混淆字符（0/O、1/l/I），仅本次打印
	const charset = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"
	raw := make([]byte, 12)
	if _, err := crand.Read(raw); err != nil {
		log.Fatalf("[reset-password] 生成临时密码失败: %v", err)
	}
	for i := range raw {
		raw[i] = charset[int(raw[i])%len(charset)]
	}
	hash, err := auth.HashPassword(string(raw))
	if err != nil {
		log.Fatalf("[reset-password] %v", err)
	}
	if err := users.UpdatePassword(target.ID, hash); err != nil {
		log.Fatalf("[reset-password] 重置失败: %v", err)
	}
	if *clearTOTP {
		if err := users.SetTOTP(target.ID, "", false); err != nil {
			log.Fatalf("[reset-password] 清除两步验证失败: %v", err)
		}
	}
	if err := (&auth.SessionStore{DB: database}).DestroyForUser(target.ID); err != nil {
		log.Printf("[reset-password] 撤销会话失败（密码仍已重置）: %v", err)
	}
	fmt.Printf("已重置 %q 的密码，临时密码：%s\n", target.Username, string(raw))
	if *clearTOTP {
		fmt.Println("已同时关闭两步验证，登录后可在「个人设置」重新绑定。")
	}
	fmt.Println("请立即用临时密码登录，并在「个人设置」里改成自己的密码（此密码不会再次显示）。")
}
