package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Login 侧签发的 token 必须能被 go-zero 的 rest.WithJwt 中间件接受。
// 两者用的 jwt 库版本不同，升级任一侧都可能静默破坏登录链路。
func TestGetJwtTokenValidForGoZero(t *testing.T) {
	const secret = "selftest123456"
	now := time.Now().Unix()
	l := &LoginLogic{}
	token, err := l.getJwtToken(secret, now, 3600)
	if err != nil {
		t.Fatalf("getJwtToken failed: %v", err)
	}

	parsed, err := jwt.Parse(token, func(*jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	if err != nil {
		t.Fatalf("token 无法被解析: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("token 解析后 valid 为 false")
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims 类型异常: %T", parsed.Claims)
	}
	if got := claims["exp"]; got != float64(now+3600) {
		t.Fatalf("exp 期望 %d，实际 %v", now+3600, got)
	}

	// 错误密钥必须验签失败，避免密钥形同虚设。
	if _, err := jwt.Parse(token, func(*jwt.Token) (interface{}, error) {
		return []byte("wrong-secret"), nil
	}); err == nil {
		t.Fatal("错误密钥竟然通过了验签")
	}
}
