package syncv3_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

var (
	proxyBaseURL      = "http://localhost"
	homeserverBaseURL = os.Getenv("SYNCV3_SERVER")
)

func TestMain(m *testing.M) {
	listenAddr := os.Getenv("SYNCV3_BINDADDR")
	if listenAddr == "" {
		fmt.Println("SYNCV3_BINDADDR must be set")
		os.Exit(1)
	}
	segments := strings.Split(listenAddr, ":")
	proxyBaseURL += ":" + segments[1]
	fmt.Println("proxy located at", proxyBaseURL)
	exitCode := m.Run()
	os.Exit(exitCode)
}
