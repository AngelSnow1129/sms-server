package main

import (
	"context"
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
// 返回 close 是为了让持久化实现在进程退出前关闭连接（SQLite 需要落盘）。
// 内存实现与 MySQL 实现（不拥有连接）返回空函数，调用方无需区分。
func newOTPStore(cfg *config.Config, db *gorm.DB) (store.Store, func(), error) {
	impl, err := cfg.ResolveOTPStore()
	if err != nil {
		return nil, nil, err
	}

	switch impl {
	case config.StoreSQLite:
		s, err := sqlite.New(cfg.SQLitePath, cfg.OTPCacheTTL)
		if err != nil {
			return nil, nil, err
		}
		log.Printf("[验证码存储] 实现=SQLite 路径=%s", cfg.SQLitePath)
		return s, func() {
			if err := s.Close(); err != nil {
				log.Printf("[验证码存储] 关闭失败: %v", err)
			}
		}, nil

	case config.StoreMySQL:
		s, err := mysqlstore.New(db, cfg.OTPCacheTTL)
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
// SQLite 分支返回一个空实现：repository.SMSRepository 依赖 *gorm.DB，而 GORM 的
// 纯 Go SQLite 驱动不在本轮改动范围内，此处刻意不引入，也不连接 MySQL。
// 因此 sqlite 模式下验证码可用，但短信记录不落库——Create 恒成功，
// 由启动日志里的「短信记录不持久化」一次性说明覆盖。
func newSMSRepository(cfg *config.Config, db *gorm.DB) smsRepository {
	switch cfg.DBDriver {
	case config.DriverSQLite:
		log.Printf("[数据库] 后端=sqlite 路径=%s 短信记录不持久化（仓储层目前仅支持 MySQL）", cfg.SQLitePath)
		return noopSMSRepository{}
	default:
		return repository.NewSMSRepository(db)
	}
}

// noopSMSRepository 短信记录的空实现，仅用于 DB_DRIVER=sqlite 分支。
type noopSMSRepository struct{}

// Create 空实现：返回 nil 使短信处理链路继续，而不是每来一条短信都打一条插入失败。
func (noopSMSRepository) Create(*model.SMSRecord) error { return nil }

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

	// 连接数据库：只有 mysql 分支才连。
	// sqlite 分支完全不接触 MySQL —— 既不建连接也不 AutoMigrate，
	// 否则在没有 MySQL 的环境里 DB_DRIVER=sqlite 会因为连接失败而启动不了。
	var db *gorm.DB
	if cfg.DBDriver == config.DriverMySQL {
		var err error
		db, err = gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
		if err != nil {
			log.Fatalf("[数据库] 连接失败: %v", err)
		}

		sqlDB, err := db.DB()
		if err != nil {
			log.Fatalf("[数据库] 获取连接失败: %v", err)
		}
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(time.Hour)

		// 自动建表与建索引。
		// 索引由 model.SMSRecord 上的 gorm index 标签声明，AutoMigrate 会幂等创建，
		// 不再手写 CREATE INDEX（MySQL 不支持 CREATE INDEX IF NOT EXISTS，手写会报 1064）。
		if err := db.AutoMigrate(&model.SMSRecord{}); err != nil {
			log.Fatalf("[数据库] 迁移失败: %v", err)
		}
	}

	// 初始化各模块
	repo := newSMSRepository(cfg, db)
	otpStore, closeStore, err := newOTPStore(cfg, db)
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
