package internal

import (
	"fmt"
	"net/http"
	"strings"
)

func ExtractAccessToken(req *http.Request) (accessToken string, err error) {
	ah := req.Header.Get("Authorization")
	if ah == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	accessToken = strings.TrimPrefix(ah, "Bearer ")
	return accessToken, nil
}
