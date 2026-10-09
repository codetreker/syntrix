package standalone

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitForHealth(t *testing.T, url string) {
	timeout := 10 * time.Second
	deadline := time.Now().Add(timeout)
	client := &http.Client{
		Timeout: 1 * time.Second,
	}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Timeout waiting for service at %s to be healthy", url)
}

func getFreePort(t *testing.T) int {
	listener, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port
}
