package identity

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if hash == "" || hash == "correct horse battery staple" {
		t.Fatal("派生值不能为空或等于明文")
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("正确口令校验失败")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("错误口令不应通过")
	}
	if VerifyPassword("not-a-hash", "x") {
		t.Fatal("非法派生值不应通过")
	}
}

func TestHashUsesRandomSalt(t *testing.T) {
	a, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	b, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if a == b {
		t.Fatal("相同口令应因随机盐产生不同派生值")
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	if err := ValidatePasswordPolicy(""); err == nil {
		t.Fatal("空口令应被拒绝")
	}
	if err := ValidatePasswordPolicy("x"); err != nil {
		t.Fatalf("单字符口令不应在形态层被拒绝: %v", err)
	}
}

func TestHashTokenIsStableAndNotPlaintext(t *testing.T) {
	token, err := NewSessionToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	if HashToken(token) == token {
		t.Fatal("库内不应保存明文令牌")
	}
	if HashToken(token) != HashToken(token) {
		t.Fatal("令牌摘要应稳定")
	}
}
