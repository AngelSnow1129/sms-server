package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"smsserver/cache"
	"smsserver/config"
	"smsserver/handler"
	"smsserver/model"
	"smsserver/repository"
	"smsserver/service"
	"smsserver/store"
	mysqlstore "smsserver/store/mysql"
	"smsserver/store/sqlite"

	gsqlite "github.com/glebarez/sqlite" // 别名 gsqlite：与 smsserver/store/sqlite 包名冲突
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// version 由构建时注入（见 .github/workflows/release.yml 的 -ldflags "-X main.version=..."）。
// 默认 "dev" 使本地 go build 的产物能被明确识别为非正式发布版本。
var version = "dev"

// newOTPStore 按 OTP_STORE 选择验证码存储实现，是接线处唯一的分支点：
// service 只依赖 store.Store，替换实现无需改动业务代码。
//
// OTP_STORE 与 DB_DRIVER 解耦：前者决定验证码存哪里，后者决定短信记录存哪里
// （见 config.Config.ResolveOTPStore）。mysql 分支复用调用方已建立的 *gorm.DB，
// 不自己建连接——短信记录归档用同一条连接，连接池与生命周期由 main 统一收口。
//
// 返回 close 是为了在进程退出前收口底层连接。SQLite 模式下共享池归 main 所有，
// closeStore 负责关闭它；内存实现与 MySQL 实现（不拥有连接）返回空函数。
func newOTPStore(cfg *config.Config, gormDB *gorm.DB, sqliteDB *sql.DB) (store.Store, func(), error) {
	impl, err := cfg.ResolveOTPStore()
	if err != nil {
		return nil, nil, err
	}

	switch impl {
	case config.StoreSQLite:
		// 复用 main 打开的共享池（与短信归档同一个），不再自开连接：
		// 两个模块写同一个库文件，由同一个单连接池串行化全部写事务。
		s, err := sqlite.NewFromDB(sqliteDB, cfg.OTPCacheTTL)
		if err != nil {
			return nil, nil, err
		}
		log.Printf("[验证码存储] 实现=SQLite 路径=%s (与短信归档共享连接池)", cfg.SQLitePath)
		return s, func() { _ = sqliteDB.Close() }, nil

	case config.StoreMySQL:
		s, err := mysqlstore.New(gormDB, cfg.OTPCacheTTL)
		if err != nil {
			return nil, nil, fmt.Errorf("初始化 MySQL 验证码存储失败: %w", err)
		}
		log.Println("[验证码存储] 实现=MySQL 表=otps")
		// MySQL 实现复用 main 建立的 *gorm.DB，不拥有连接，无需关闭
		return s, func() {}, nil

	case config.StoreMemory:
		// 历史默认：验证码存内存，重启即丢失，与接入 SQLite 前完全一致
		log.Println("[验证码存储] 实现=内存 重启后验证码丢失")
		return cache.NewOTPCache(cfg.OTPCacheTTL), func() {}, nil

	default:
		// ResolveOTPStore 已经过滤了全部非法值，这里是不可达分支，
		// 留着是为了让「新增实现忘了接线」在编译/阅读时仍然可见。
		return nil, nil, fmt.Errorf("OTP_STORE=%q 未接线，可选值为 %s|%s|%s",
			impl, config.StoreMemory, config.StoreSQLite, config.StoreMySQL)
	}
}

// smsRepository 与 service 内部的同名接口结构一致（Create(*model.SMSRecord) error），
// 方法集是导出的，因此 main 包可以声明等价接口并传递任意实现。
// 这里重新声明而不是引用 service.smsRepository，是因为后者未导出、外部无法指名。
type smsRepository interface {
	Create(record *model.SMSRecord) error
}

// newSMSRepository 按 DB_DRIVER 选择短信记录的持久化后端。
//
// SQLite 分支：用 glebarez/sqlite（纯 Go 的 GORM SQLite 驱动）把调用方传入的
// 共享连接池（sqliteDB）包成 *gorm.DB，再由 repository.NewSQLiteRepository 用
// SQLite 方言影子模型建 sms_records 表——短信记录与验证码（同一库文件的 otps 表）
// 由此真正落在同一个 SQLite 文件里。为什么不直接 AutoMigrate model.SMSRecord：
// 其标签含 MySQL 专有的 default:CURRENT_TIMESTAMP(3)，SQLite 建表直接报语法
// 错误（见 repository/sqlite_repo.go 影子模型注释）。
//
// MySQL 分支与历史行为完全一致。
func newSMSRepository(cfg *config.Config, gormDB *gorm.DB, sqliteDB *sql.DB) (smsRepository, error) {
	switch cfg.DBDriver {
	case config.DriverSQLite:
		// 复用共享池而不是让驱动自己 Open：验证码存储（store/sqlite）与短信归档
		// 必须是同一个池，写事务才能被串行化到同一个写者上。
		gormSQLite, err := gorm.Open(gsqlite.Dialector{Conn: sqliteDB}, &gorm.Config{})
		if err != nil {
			return nil, fmt.Errorf("包装 SQLite 连接失败: %w", err)
		}
		repo, err := repository.NewSQLiteRepository(gormSQLite)
		if err != nil {
			return nil, err
		}
		log.Printf("[数据库] 后端=sqlite 路径=%s 短信记录落库(表 sms_records)", cfg.SQLitePath)
		return repo, nil
	default:
		return repository.NewSMSRepository(gormDB), nil
	}
}

func main() {
	// 第一行输出实际运行的版本，便于线上排查「部署的到底是哪个版本」
	log.Printf("[服务] 版本=%s", version)

	cfg := config.Load()

	// 校验必填配置
	if cfg.HMACSecret == "" {
		log.Fatal("[配置错误] HMAC_SECRET 不能为空")
	}
	if cfg.WebhookSecret == "" {
		log.Fatal("[配置错误] WEBHOOK_SECRET 不能为空")
	}

	// 连接数据库：按 DB_DRIVER 二选一，非法值启动即退出。
	//   - mysql：GORM 连接 MySQL 并 AutoMigrate（历史行为）；
	//   - sqlite：打开本地共享单连接池，验证码存储与短信归档共用同一个库文件
	//     与连接池，完全不接触 MySQL（这正是 sqlite 模式的价值：无 MySQL 可跑）。
	var (
		gormDB   *gorm.DB // DB_DRIVER=mysql 的 GORM 连接；sqlite 模式下为 nil
		sqliteDB *sql.DB  // DB_DRIVER=sqlite 的共享连接池；mysql 模式下为 nil
	)
	switch cfg.DBDriver {
	case config.DriverMySQL:
		var err error
		gormDB, err = gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
		if err != nil {
			log.Fatalf("[数据库] 连接失败: %v", err)
		}

		sqlDB, err := gormDB.DB()
		if err != nil {
			log.Fatalf("[数据库] 获取连接失败: %v", err)
		}
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(time.Hour)

		// 自动建表与建索引。
		// 索引由 model.SMSRecord 上的 gorm index 标签声明，AutoMigrate 会幂等创建，
		// 不再手写 CREATE INDEX（MySQL 不支持 CREATE INDEX IF NOT EXISTS，手写会报 1064）。
		if err := gormDB.AutoMigrate(&model.SMSRecord{}); err != nil {
			log.Fatalf("[数据库] 迁移失败: %v", err)
		}

	case config.DriverSQLite:
		var err error
		sqliteDB, err = sql.Open("sqlite", sqlite.SQLiteDSN(cfg.SQLitePath))
		if err != nil {
			log.Fatalf("[数据库] 打开 SQLite 失败: %v", err)
		}
		// SQLite 是单写者模型：把池压到 1 个连接，验证码存储与短信归档的所有
		// 写事务都由这个池串行化，规避并发写报 "database is locked"；
		// busy_timeout/WAL 由 SQLiteDSN 统一追加（见 store/sqlite 的 dsnPragmas）。
		sqliteDB.SetMaxOpenConns(1)
		sqliteDB.SetMaxIdleConns(1)

	default:
		log.Fatalf("[配置错误] DB_DRIVER=%q 非法，可选值为 %s|%s",
			cfg.DBDriver, config.DriverMySQL, config.DriverSQLite)
	}

	// 初始化各模块
	repo, err := newSMSRepository(cfg, gormDB, sqliteDB)
	if err != nil {
		log.Fatalf("[数据库] 初始化短信归档失败: %v", err)
	}
	otpStore, closeStore, err := newOTPStore(cfg, gormDB, sqliteDB)
	if err != nil {
		log.Fatalf("[验证码存储] 初始化失败: %v", err)
	}
	defer closeStore()
	svc := service.NewOTPService(repo, otpStore, cfg.HMACSecret, cfg.OTPTemplates)
	h := handler.NewHandler(svc, cfg.WebhookSecret, cfg.SMSForwardChannels)

	// 启动清理协程
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCleanup := svc.StartCleanupWorker(ctx, cfg.CleanupInterval)

	// 注册路由（路由定义集中在 handler.NewRouter，便于测试覆盖）
	mux := handler.NewRouter(h)

	// HTTP服务配置
	server := &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 启动服务
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("[服务] 监听地址: %s", cfg.ServerAddr)
		// 记录生效配置，避免 TTL/清理间隔只能从日志倒推
		log.Printf("[服务] 验证码有效期=%v 清理间隔=%v", cfg.OTPCacheTTL, cfg.CleanupInterval)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("[服务] 启动失败: %v", err)
	case sig := <-quit:
		log.Printf("[服务] 正在关闭... signal=%s", sig)
	}

	// 停止接收新请求，等待在途 HTTP 请求结束
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[服务] 关闭异常: %v", err)
	}

	// 停掉清理协程，并等待在途短信处理完成，避免丢短信
	cancel()
	svc.WaitInFlight()
	waitCleanup()

	log.Println("[服务] 已停止")
}
