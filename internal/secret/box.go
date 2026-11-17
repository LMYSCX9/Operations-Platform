// Package secret 提供底座唯一的凭据静态加密实现（ADR-0013）。
//
// 算法固定为 AES-256-GCM；主密钥来自配置项 credential_key（base64 的 32 字节），
// 不落库、不写日志、不出现在任何接口响应中。缺少主密钥时调用方必须显式失败，
// 不允许降级为明文（DOC-09 HOST-A-06）。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// ErrKeyNotConfigured 表示主密钥缺失或长度非法，涉及凭据的读写必须显式失败。
var ErrKeyNotConfigured = errors.New("凭据主密钥未配置")

// ErrDecryptFailed 表示密文被篡改或与当前主密钥不匹配；错误信息不包含明文。
var ErrDecryptFailed = errors.New("凭据解密失败")

// KeySize 是 AES-256 要求的密钥长度（字节）。
const KeySize = 32

// Box 是线程安全的加解密器；cipher.AEAD 允许并发使用。
type Box struct {
	aead cipher.AEAD
}

// New 用给定主密钥构造 Box；长度非 32 字节一律拒绝。
func New(key []byte) (*Box, error) {
	if len(key) == 0 {
		return nil, ErrKeyNotConfigured
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: 主密钥必须为 %d 字节，当前 %d 字节", ErrKeyNotConfigured, KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Encrypt 返回 nonce||ciphertext||tag；每次调用使用独立随机 nonce。
func (b *Box) Encrypt(plaintext []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, ErrKeyNotConfigured
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成随机 nonce 失败: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt 校验并解密 nonce||ciphertext||tag。
func (b *Box) Decrypt(ciphertext []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, ErrKeyNotConfigured
	}
	nonceSize := b.aead.NonceSize()
	if len(ciphertext) < nonceSize+b.aead.Overhead() {
		return nil, ErrDecryptFailed
	}
	nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := b.aead.Open(nil, nonce, body, nil)
	if err != nil {
		// 不包装底层错误，避免把分块信息等内部细节带出去。
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}
