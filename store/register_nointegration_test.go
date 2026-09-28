//go:build !integration

package store_test

// mysqlImplementations 在没有 integration 构建标签时为空。
//
// MySQL 版存储需要一台真实数据库，而 `go test ./...` 必须能在无数据库的 CI 里跑通，
// 因此这里的登记表为空占位：契约用例只覆盖内存与 SQLite 两个实现。
//
// 带 MySQL 环境时这样跑（DSN 指向**专用测试库**，用例会先清表）：
//
//	TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/smsdb_test?parseTime=true&loc=Local' \
//	  go test -tags=integration -race ./store/...
var mysqlImplementations = []storeImpl{}
