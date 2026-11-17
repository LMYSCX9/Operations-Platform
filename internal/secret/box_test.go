package secret

import (
	"bytes"
	"errors"
	"testing"
)

func testKey(fill byte) []byte {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = fill
	}
	return key
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	box, err := New(testKey(1))
	if err != nil {
		t.Fatalf("构造 Box 失败: %v", err)
	}
	plaintext := []byte(`{"password":"s3cr3t"}`)
	ciphertext, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("密文中出现了明文")
	}
	got, err := box.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("解密结果 = %q，期望 %q", got, plaintext)
	}
}

func TestEncryptUsesDistinctNonce(t *testing.T) {
	box, _ := New(testKey(2))
	first, _ := box.Encrypt([]byte("same"))
	second, _ := box.Encrypt([]byte("same"))
	if bytes.Equal(first, second) {
		t.Fatal("两次加密产生了相同密文，nonce 可能被复用")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	box, _ := New(testKey(3))
	ciphertext, _ := box.Encrypt([]byte("credential"))
	ciphertext[len(ciphertext)-1] ^= 0xff
	if _, err := box.Decrypt(ciphertext); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("篡改后解密错误 = %v，期望 ErrDecryptFailed", err)
	}
}

func TestDecryptRejectsShortCiphertext(t *testing.T) {
	box, _ := New(testKey(4))
	if _, err := box.Decrypt([]byte("short")); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("短密文错误 = %v，期望 ErrDecryptFailed", err)
	}
}

func TestNewRejectsMissingOrWrongKey(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("空密钥错误 = %v，期望 ErrKeyNotConfigured", err)
	}
	if _, err := New(make([]byte, 16)); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("16 字节密钥错误 = %v，期望 ErrKeyNotConfigured", err)
	}
}

func TestNilBoxFailsExplicitly(t *testing.T) {
	var box *Box
	if _, err := box.Encrypt([]byte("x")); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("nil Box 加密错误 = %v", err)
	}
	if _, err := box.Decrypt([]byte("0123456789012345678901234567890123456789")); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("nil Box 解密错误 = %v", err)
	}
}

func TestDecryptWithAnotherKeyFails(t *testing.T) {
	first, _ := New(testKey(5))
	second, _ := New(testKey(6))
	ciphertext, _ := first.Encrypt([]byte("secret"))
	if _, err := second.Decrypt(ciphertext); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("错误密钥解密错误 = %v，期望 ErrDecryptFailed", err)
	}
}
