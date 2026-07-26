package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

const testSecret = "test-secret-at-least-32-bytes-long!!"

func TestIssueVerifyRoundTrip(t *testing.T) {
	iss := NewIssuer(testSecret, time.Hour, nil)
	id := uuid.New()

	token, err := iss.Issue(id, domain.RoleAdmin)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := iss.Verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != id.String() {
		t.Errorf("sub = %q, want %q", claims.Subject, id.String())
	}
	if claims.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", claims.Role)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	// Negative TTL: the token is already expired when issued.
	iss := NewIssuer(testSecret, -time.Minute, nil)
	token, _ := iss.Issue(uuid.New(), domain.RoleUser)

	if _, err := iss.Verify(token); err == nil {
		t.Error("expired token accepted")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	token, _ := NewIssuer(testSecret, time.Hour, nil).Issue(uuid.New(), domain.RoleUser)

	other := NewIssuer("another-secret-also-32-bytes-long!!!", time.Hour, nil)
	if _, err := other.Verify(token); err == nil {
		t.Error("token verified under the wrong secret")
	}
}

func TestVerifyRejectsNoneAlgorithm(t *testing.T) {
	// Forge a token signed with "none" — the classic algorithm-substitution
	// attack. A verifier that trusts the header's alg would accept it.
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
		Role: domain.RoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.New().String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	raw, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}

	iss := NewIssuer(testSecret, time.Hour, nil)
	if _, err := iss.Verify(raw); err == nil {
		t.Error("none-signed token accepted")
	}
}
