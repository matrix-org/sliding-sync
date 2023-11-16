package internal

import "strings"

type HomeServerUrl struct {
	HttpOrUnixStr string
}

func (u HomeServerUrl) IsUnixSocket() bool {
	return strings.HasPrefix(u.HttpOrUnixStr, "/")
}

func (u HomeServerUrl) GetUnixSocket() string {
	if u.IsUnixSocket() {
		return u.HttpOrUnixStr
	}
	return ""
}

func (u HomeServerUrl) GetBaseUrl() string {
	if u.IsUnixSocket() {
		return "http://unix"
	}
	return u.HttpOrUnixStr
}
