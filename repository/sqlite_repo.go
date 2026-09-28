package repository

import (
	"errors"
	"time"

	"smsserver/model"

	"gorm.io/gorm"
)

// sqliteSMSRecord 是 model.SMSRecord 的 SQLite 安全影子模型：列名逐列一致，
// 只把 MySQL 专有的 DDL 标签换成 SQLite 方言。
//
// 为什么需要影子模型而不能直接 AutoMigrate model.SMSRecord：
//   - `default:CURRENT_TIMESTAMP(3)` 是 MySQL 语法，SQLite 不接受带精度参数的
//     CURRENT_TIMESTAMP，建表直接报 `near "(": syntax error`（实测踩过）；
//   - `type:datetime(3)` 在 MySQL 是毫秒精度类型，SQLite 的类型亲和下写 plain
//     datetime 即可，驱动层保存 time.Time 不受影响。
//
// 为什么 DML 仍直接用 model.SMSRecord：GORM 的列名由字段名按 snake_case 推导，
// 与影子模型逐列一致；`type:`/`default:` 标签只参与 DDL 不参与 DML，
// 因此 Create/查询读写同一张 sms_records 表无需任何转换。
type sqliteSMSRecord struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement"`
	Provider             string    `gorm:"size:64;not null;default:''"`
	Sender               string    `gorm:"size:128;not null;default:''"`
	Recipient            string    `gorm:"size:128;not null;default:'';index:idx_recipient_time,priority:1"`
	Body                 string    `gorm:"type:text;not null"`
	ChannelID            string    `gorm:"size:64;not null;default:''"`
	ExtractedCode        *string   `gorm:"size:32"`
	TemplateID           *string   `gorm:"size:64"`
	ExtractionStatus     string    `gorm:"size:64;not null;default:''"`
	ExtractionConfidence string    `gorm:"size:16;not null;default:''"`
	ReceivedAt           time.Time `gorm:"type:datetime;not null"`
	CreatedAt            time.Time `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;index:idx_recipient_time,sort:desc,priority:2"`
}

// TableName 与 MySQL 版同名，方便两种模式间直接迁移库文件
func (sqliteSMSRecord) TableName() string {
	return "sms_records"
}

// SQLiteRepository 短信归档的 SQLite 实现（DB_DRIVER=sqlite 模式）。
// 读写全部走 model.SMSRecord（见 sqliteSMSRecord 注释），对外接口与 MySQL 版一致。
type SQLiteRepository struct {
	db *gorm.DB
}

// NewSQLiteRepository 建表并返回仓储。构造函数返回 error：
// AutoMigrate 需要 DDL 权限，失败必须让启动中止（与 MySQL 分支的 Fatal 语义一致）。
// 连接池归调用方所有，本构造函数不调整任何池参数——
// main 在 sqlite 模式下让本仓储与验证码存储共享同一个单连接池。
func NewSQLiteRepository(db *gorm.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, errors.New("SQLite 仓储需要一个非 nil 的 *gorm.DB")
	}
	if err := db.AutoMigrate(&sqliteSMSRecord{}); err != nil {
		return nil, err
	}
	return &SQLiteRepository{db: db}, nil
}

// Create 插入短信记录
func (r *SQLiteRepository) Create(record *model.SMSRecord) error {
	return r.db.Create(record).Error
}

// FindByRecipient 按手机号查询短信记录（与 MySQL 版同语义：created_at 倒序）
func (r *SQLiteRepository) FindByRecipient(recipient string) ([]model.SMSRecord, error) {
	var records []model.SMSRecord
	err := r.db.Where("recipient = ?", recipient).
		Order("created_at DESC").
		Find(&records).Error
	return records, err
}
