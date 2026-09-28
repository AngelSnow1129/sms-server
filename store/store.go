// Package store 定义验证码存储的抽象层。
//
// 本包只放接口与共享类型，不提供任何具体实现：内存实现 cache.OTPCache 依赖
// store，业务层 service 也依赖 store，二者互不引用，从而避免循环依赖。
// 将来接入 SQLite 或既有的 MySQL 时，只需新增一个实现并在 main.go 换掉接线。
package store

// Store 验证码存储的能力集合：写入/覆盖、原子读取并失效、清理过期项。
//
// 设计取舍：方法签名刻意不带 context.Context。
//
//   - 现有实现（cache.OTPCache）是纯内存操作，没有阻塞 I/O，也没有可取消的对象，
//     传入 ctx 只能被忽略，反而会让调用方误以为存在取消/超时语义；
//   - service.GetOTP / ProcessIncomingSMS 的签名被上层锁死（handler 依赖它们，
//     handler_test.go 中的 fakeService 也按无 ctx 实现），若在此处引入 ctx，
//     service 只能在调用点硬编码 context.Background()，属于典型的反模式；
//   - 引入 ctx 会连带改动 cache 包的全部既有测试（TTL、阅后即焚、并发安全），
//     超出「纯重构」的范围。
//
// 演进路径：接入 SQLite/MySQL 时，持久化实现可以在构造阶段持有带超时的 ctx
// （例如 NewSQLiteStore(ctx, dsn, ttl) 把 ctx 存为字段），不必改本接口即可获得
// 超时与取消能力。若将来确需把错误或 ctx 显式上抛给调用方，应作为一次独立的
// 破坏性重构，让 service 与 handler 一并调整，而不是在本轮纯重构里夹带。
type Store interface {
	// Set 写入或覆盖验证码：同一 token 重复写入时新码覆盖旧码，旧码立即失效。
	// token 是手机号经 HMAC-SHA256 得到的十六进制串，实现方不应假设它是手机号。
	Set(token, code string)

	// Get 读取验证码但不使其失效；token 不存在或已过期时返回 ok=false。
	Get(token string) (code string, ok bool)

	// Delete 删除指定验证码；token 不存在时无副作用。
	Delete(token string)

	// GetAndDelete 原子地读取验证码并使其失效（阅后即焚）。
	// 并发下必须保证同一条验证码只会被一个调用方取走。
	GetAndDelete(token string) (code string, ok bool)

	// Cleanup 清理已过期的验证码，返回本次清理的条数。
	// service.StartCleanupWorker 的「移除=」日志依赖该返回值，实现方不可用空实现糊过去。
	Cleanup() (removed int)

	// Len 返回当前存储中未失效的条目数量，用于清理日志的「剩余=」字段。
	Len() int
}
