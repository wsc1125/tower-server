// Package auth 认证：密码哈希与 JWT 签发校验。
package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ErrPasswordMismatch 密码不匹配。
var ErrPasswordMismatch = errors.New("密码不匹配")

// PasswordHasher bcrypt 密码哈希器。
type PasswordHasher struct {
	cost int
}

// NewPasswordHasher 创建哈希器。cost 建议 12（约 250ms/次）。
func NewPasswordHasher(cost int) *PasswordHasher {
	return &PasswordHasher{cost: cost}
}

// Hash 生成密码哈希。bcrypt 输出固定 60 字符，且自带盐值。
func (h *PasswordHasher) Hash(password string) (string, error) {
	// bcrypt 上限 72 字节，超出部分会被静默截断 —— 这里显式拒绝，避免「密码后半段无效」。
	if len(password) > 72 {
		return "", fmt.Errorf("密码超过 bcrypt 上限 72 字节")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}

	return string(hashed), nil
}

// Verify 校验密码。不匹配时返回 ErrPasswordMismatch。
func (h *PasswordHasher) Verify(hash, password string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ErrPasswordMismatch
		}

		return fmt.Errorf("校验密码失败: %w", err)
	}

	return nil
}

// dummyHash 一个固定的合法 bcrypt 哈希，用于「用户名不存在」时的假比对。
//
// 目的：让「用户不存在」与「密码错误」两条路径耗时相近，
// 避免攻击者通过响应时间枚举出哪些用户名真实存在（时序攻击）。
// 值对应明文 "dummy"，永远不会匹配成功。
const dummyHash = "$2a$12$C6UzMDM.H6dfI/f/IKcEe.4Z6jVvJZ8YQxKZ5Z5Z5Z5Z5Z5Z5Z5Zu"

// VerifyDummy 执行一次假比对，用于抹平时序差异。
// 返回值恒为 ErrPasswordMismatch，调用方无需判断。
func (h *PasswordHasher) VerifyDummy(password string) error {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
	return ErrPasswordMismatch
}
