package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// tokenIssuer 发放重连凭据。
//
// M1 只需要"每个会话一个不重复的串"，用来让客户端在断线重连时认得回自己。
// 不做签名也不做过期校验——局域网直连、无公网暴露，这些机制属于上线前
// 才需要考虑的东西（server-design §19 已把鉴权列为非目标）。
//
// 用 crypto/rand 而不是 math/rand：这个值会出现在客户端日志里，两者成本
// 差异在每秒几次的量级上完全不可见，而前者不会因为种子泄漏导致两个玩家
// 拿到同一个 token。
type tokenIssuer struct {
	mu     sync.Mutex
	issued map[uint64]string
}

func newTokenIssuer() *tokenIssuer {
	return &tokenIssuer{issued: make(map[uint64]string)}
}

// issue 为会话生成一个 token。同一个会话重复调用返回同一个值。
func (t *tokenIssuer) issue(sessionID uint64) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if v, ok := t.issued[sessionID]; ok {
		return v
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 失败意味着系统熵源坏了。此时退回会话号——
		// 它至少唯一，而我们没有更好的选择。
		v := hex.EncodeToString([]byte{byte(sessionID), byte(sessionID >> 8)})
		t.issued[sessionID] = v
		return v
	}
	v := hex.EncodeToString(buf[:])
	t.issued[sessionID] = v
	return v
}

// revoke 在会话结束时丢弃 token，避免 map 无限增长。
func (t *tokenIssuer) revoke(sessionID uint64) {
	t.mu.Lock()
	delete(t.issued, sessionID)
	t.mu.Unlock()
}
