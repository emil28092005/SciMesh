package token

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const secret = "coordinator-verify-secret-32-bytes!!"

func sign(t *testing.T, method jwt.SigningMethod, key any, sub, role string, exp time.Time) string {
	t.Helper()
	return signVerified(t, method, key, sub, role, false, exp)
}

func signVerified(t *testing.T, method jwt.SigningMethod, key any, sub, role string, verified bool, exp time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims{
		Role:     role,
		Verified: verified,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	})
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

func TestVerifyCarriesVerifiedClaim(t *testing.T) {
	v := NewVerifier(secret)
	raw := signVerified(t, jwt.SigningMethodHS256, []byte(secret), uuid.New().String(), "user", true, time.Now().Add(time.Hour))

	claims, err := v.Verify(raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !claims.Verified {
		t.Error("verified claim not read from token")
	}
}

func TestNewVerifierNilWhenNoSecret(t *testing.T) {
	if NewVerifier("") != nil {
		t.Error("empty secret must yield a nil verifier (auth disabled)")
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	v := NewVerifier(secret)
	id := uuid.New()
	raw := sign(t, jwt.SigningMethodHS256, []byte(secret), id.String(), "admin", time.Now().Add(time.Hour))

	claims, err := v.Verify(raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.UserID != id {
		t.Errorf("UserID = %v, want %v", claims.UserID, id)
	}
	if claims.Role != "admin" {
		t.Errorf("Role = %q, want admin", claims.Role)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	v := NewVerifier(secret)
	raw := sign(t, jwt.SigningMethodHS256, []byte(secret), uuid.New().String(), "user", time.Now().Add(-time.Minute))
	if _, err := v.Verify(raw); err == nil {
		t.Error("expired token accepted")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	raw := sign(t, jwt.SigningMethodHS256, []byte(secret), uuid.New().String(), "user", time.Now().Add(time.Hour))
	if _, err := NewVerifier("another-secret-also-at-least-32-byte").Verify(raw); err == nil {
		t.Error("token verified under the wrong secret")
	}
}

func TestVerifyRejectsNoneAlg(t *testing.T) {
	raw := sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, uuid.New().String(), "admin", time.Now().Add(time.Hour))
	if _, err := NewVerifier(secret).Verify(raw); err == nil {
		t.Error("none-signed token accepted")
	}
}

func TestVerifyRejectsNonUUIDSubject(t *testing.T) {
	raw := sign(t, jwt.SigningMethodHS256, []byte(secret), "not-a-uuid", "user", time.Now().Add(time.Hour))
	if _, err := NewVerifier(secret).Verify(raw); err == nil {
		t.Error("non-uuid subject accepted")
	}
}
