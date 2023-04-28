package sync2

import (
	"testing"
)

// Sanity check that different tokens have different hashes
func TestHash(t *testing.T) {
	token1 := "ABCD"
	token2 := "EFGH"
	hash1 := hashToken(token1)
	hash2 := hashToken(token2)
	if hash1 == hash2 {
		t.Fatalf("HashedTokenFromRequest: %s and %s have the same hash", token1, token2)
	}
}
