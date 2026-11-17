// Package identity 提供本地账号、会话与角色授权（M2）。
//
// 口令只保存不可逆派生值（PBKDF2-HMAC-SHA256）；参数属于实现基线（OQ-040），
// 集中在本文件，禁止散落到调用方。
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	// DefaultIterations 见 OQ-040；调整时必须同步 DOC-13 与旧值兼容策略。
	DefaultIterations = 210000
	SaltBytes         = 16
	KeyBytes          = 32
)

// ErrPasswordMismatch 表示口令校验失败；调用方不得区分「用户不存在」与「口令错误」对外呈现。
var ErrPasswordMismatch = errors.New("口令不匹配")

// HashPassword 生成 `pbkdf2_sha256$<iter>$<salt-b64>$<key-b64>` 形式的派生值。
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("口令不能为空")
	}
	salt := make([]byte, SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	key := pbkdf2.Key([]byte(password), salt, DefaultIterations, KeyBytes, sha256.New)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s",
		DefaultIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword 以常量时间比较派生值，避免通过响应时间区分错误位置。
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got := pbkdf2.Key([]byte(password), salt, iter, len(want), sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// HashToken 保存会话令牌的摘要而不是令牌本身：库泄露不等价于会话被接管。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewSessionToken 生成 256 位随机令牌。
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成会话令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidatePasswordPolicy 只做最低限度的形态校验；复杂度策略未定（OQ-040），不自行加码。
func ValidatePasswordPolicy(password string) error {
	if password == "" {
		return errors.New("口令不能为空")
	}
	if len(password) > 1024 {
		return errors.New("口令过长（上限 1024 字节）")
	}
	return nil
}
