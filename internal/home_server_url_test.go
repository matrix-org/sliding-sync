package internal

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestHomeServerUrl_IsUnixSocket_True(t *testing.T) {
	assert.True(t, HomeServerUrl{"/path/to/socket"}.IsUnixSocket())
}

func TestHomeServerUrl_IsUnixSocket_False(t *testing.T) {
	assert.False(t, HomeServerUrl{"localhost:8080"}.IsUnixSocket())
}

func TestHomeServerUrl_GetUnixSocket(t *testing.T) {
	assert.Equal(t, "/path/to/socket", HomeServerUrl{"/path/to/socket"}.GetUnixSocket())
}

func TestHomeServerUrl_GetUnixSocket_Http(t *testing.T) {
	assert.Equal(t, "", HomeServerUrl{"localhost:8080"}.GetUnixSocket())
}

func TestHomeServerUrl_GetBaseUrl_UnixSocket(t *testing.T) {
	assert.Equal(t, "http://unix", HomeServerUrl{"/path/to/socket"}.GetBaseUrl())
}

func TestHomeServerUrl_GetBaseUrl_Http(t *testing.T) {
	assert.Equal(t, "localhost:8080", HomeServerUrl{"localhost:8080"}.GetBaseUrl())
}
