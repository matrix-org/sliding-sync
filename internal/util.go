package internal

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// Keys returns a slice containing copies of the keys of the given map, in no particular
// order.
func Keys[K comparable, V any](m map[K]V) []K {
	if m == nil {
		return nil
	}
	output := make([]K, 0, len(m))
	for key := range m {
		output = append(output, key)
	}
	return output
}

func IsUnixSocket(httpOrUnixStr string) bool {
	return strings.HasPrefix(httpOrUnixStr, "/")
}

func UnixTransport(httpOrUnixStr string) *http.Transport {
	return &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", httpOrUnixStr)
		},
	}
}

func GetBaseURL(httpOrUnixStr string) string {
	if IsUnixSocket(httpOrUnixStr) {
		return "http://unix"
	}
	return httpOrUnixStr
}
