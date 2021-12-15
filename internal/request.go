package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

func DeviceIDFromRequest(req *http.Request) (string, error) {
	// return a hash of the access token
	ah := req.Header.Get("Authorization")
	if ah == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	accessToken := strings.TrimPrefix(ah, "Bearer ")
	// important that this is a cryptographically secure hash function to prevent
	// preimage attacks where Eve can use a fake token to hash to an existing device ID
	// on the server.
	hash := sha256.New()
	hash.Write([]byte(accessToken))
	return hex.EncodeToString(hash.Sum(nil)), nil
}
