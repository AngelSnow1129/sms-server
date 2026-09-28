package cache

import (
	"sync"
	"time"

	"smsserver/model"
	"smsserver/store"
)

// OTPCache 验证码内存缓存（线程安全），是 store.Store 的内存实现
type OTPCache struct {
	mu   sync.RWMutex
	data map[string]model.OTPInfo
	ttl  time.Duration
}

// 编译期断言：OTPCache 必须满足 store.Store，
// 否则上层依赖接口的接线要到运行期才发现缺方法。
var _ store.Store = (*OTPCache)(nil)

// NewOTPCache 创建缓存实例
func NewOTPCache(ttl time.Duration) *OTPCache {
	return &OTPCache{
		data: make(map[string]model.OTPInfo),
		ttl:  ttl,
	}
}

// Set 写入或覆盖验证码
func (c *OTPCache) Set(phone, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[phone] = model.OTPInfo{
		Code:     code,
		ExpireAt: time.Now().Add(c.ttl),
	}
}

// Get 读取验证码（不删除）
func (c *OTPCache) Get(phone string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	info, ok := c.data[phone]
	if !ok || time.Now().After(info.ExpireAt) {
		return "", false
	}
	return info.Code, true
}

// Delete 删除验证码
func (c *OTPCache) Delete(phone string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, phone)
}

// GetAndDelete 获取并删除验证码（阅后即焚）
func (c *OTPCache) GetAndDelete(phone string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	info, ok := c.data[phone]
	if !ok || time.Now().After(info.ExpireAt) {
		return "", false
	}
	delete(c.data, phone)
	return info.Code, true
}

// Cleanup 清理过期数据，返回清理数量
func (c *OTPCache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	count := 0
	for phone, info := range c.data {
		if now.After(info.ExpireAt) {
			delete(c.data, phone)
			count++
		}
	}
	return count
}

// Len 返回缓存中的验证码数量
func (c *OTPCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data)
}
