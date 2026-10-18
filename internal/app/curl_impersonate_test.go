package app

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestReadCurlResponseHeaderSkipsProxyConnect(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("HTTP/1.1 200 Connection established\r\nProxy-Agent: test\r\n\r\nHTTP/2 200\r\nContent-Type: application/json\r\n\r\n{\"ok\":true}"))
	status, _, header, err := readCurlResponseHeader(reader)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 || header.Get("Content-Type") != "application/json" || string(body) != `{"ok":true}` {
		t.Fatalf("status=%d headers=%v body=%q", status, header, body)
	}
}
