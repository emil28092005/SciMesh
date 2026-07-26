package auth

import "testing"

func TestHashAndCompare(t *testing.T) {
	h := NewHasher(0) // default cost

	hash, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("hash must not equal the plaintext")
	}
	if err := h.Compare(hash, "correct horse battery staple"); err != nil {
		t.Errorf("correct password rejected: %v", err)
	}
	if err := h.Compare(hash, "wrong password"); err == nil {
		t.Error("wrong password accepted")
	}
}

func TestHashSaltsEachTime(t *testing.T) {
	h := NewHasher(0)
	a, _ := h.Hash("same")
	b, _ := h.Hash("same")
	if a == b {
		t.Error("two hashes of the same password must differ (random salt)")
	}
}
