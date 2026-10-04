// 保号通 Go 版入口。
//
// 单个静态二进制，内嵌模板与 schema，无需外部依赖：
//
//	./baohaotong                                    # SQLite：data/baohaotong.db，监听 127.0.0.1:8080
//	./baohaotong -driver mysql  -dsn 'user:pass@tcp(127.0.0.1:3306)/baohaotong?charset=utf8mb4'
//	./baohaotong -driver postgres -dsn 'postgres://user:pass@127.0.0.1:5432/baohaotong?TimeZone=Asia/Shanghai'
//
// 对应环境变量：BHT_DRIVER / BHT_DB / BHT_DSN / BHT_ADDR / BHT_TRUST_PROXY / BHT_CRON_INTERVAL。
// 首次启动访问 /setup 创建管理员（创建后入口永久关闭）。
package main

import (
	"bufio"
	"context"
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

	"baohaotong/internal/cronjob"
	"baohaotong/internal/db"
	"baohaotong/internal/store"
	"baohaotong/internal/tgcall"
	"baohaotong/internal/web"
)

func main() {
	log.SetFlags(log.LstdFlags)

	// tg-login 子命令：为某个用户的 TG 电话渠道交互式登录 MTProto 账号。
	// 用法：baohaotong tg-login -username <用户名> [-driver ... -db/-dsn ...]
	if len(os.Args) > 1 && os.Args[1] == "tg-login" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		tgLogin(os.Args)
		return
	}

	var (
		driver = flag.String("driver", envOr("BHT_DRIVER", db.DialectSQLite),
			"数据库驱动：sqlite（默认）/ mysql / postgres")
		dbPath = flag.String("db", envOr("BHT_DB", "data/baohaotong.db"),
			"SQLite 数据库文件路径（仅 driver=sqlite 时使用）")
		dsn = flag.String("dsn", envOr("BHT_DSN", ""),
			"MySQL/PostgreSQL 连接串，例如 "+
				`mysql: 'user:pass@tcp(127.0.0.1:3306)/baohaotong?charset=utf8mb4'；`+
				`postgres: 'postgres://user:pass@127.0.0.1:5432/baohaotong?TimeZone=Asia/Shanghai'`)
		addr = flag.String("addr", envOr("BHT_ADDR", "127.0.0.1:8080"),
			"HTTP 监听地址")
		trustProxy = flag.Bool("trust-proxy", envOr("BHT_TRUST_PROXY", "") == "1",
			"信任 X-Forwarded-For 头（部署在可信反向代理之后时开启，用于登录限流的 IP 归属）")
		cronInterval = flag.Duration("cron-interval", durationOr("BHT_CRON_INTERVAL", time.Hour),
			"定时任务执行间隔（如 30m、1h）；0 表示禁用进程内调度，仅保留管理后台手动触发")
	)
	flag.Parse()

	if err := run(*driver, *dbPath, *dsn, *addr, *trustProxy, *cronInterval); err != nil {
		log.Fatalf("[main] %v", err)
	}
}

func run(driver, dbPath, dsn, addr string, trustProxy bool, cronInterval time.Duration) error {
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
	if err := app.Settings.SeedDefaults(); err != nil {
		return fmt.Errorf("写入默认设置失败: %w", err)
	}

	if _, total, err := app.Users.List(1, 1, ""); err == nil && total == 0 {
		log.Printf("[main] 系统还没有账号：请访问 http://%s/setup 创建管理员（创建后入口自动关闭）", addr)
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
		log.Printf("[main] 保号通已启动（驱动 %s）: http://%s", driver, addr)
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
		driver = fs.String("driver", envOr("BHT_DRIVER", db.DialectSQLite), "数据库驱动")
		dbPath = fs.String("db", envOr("BHT_DB", "data/baohaotong.db"), "SQLite 数据库路径")
		dsn    = fs.String("dsn", envOr("BHT_DSN", ""), "MySQL/PostgreSQL 连接串")
		user   = fs.String("username", "", "要登录哪个用户的 TG 电话渠道（登录页用的用户名）")
	)
	_ = fs.Parse(args)
	if *user == "" {
		fmt.Fprintln(os.Stderr, "用法: baohaotong tg-login -username <用户名>")
		os.Exit(2)
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
