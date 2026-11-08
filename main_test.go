package main

import (
	"bytes"
	"io"
	"net"
	"testing"
)

func TestSendClientHelloChunked(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	payload := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	errCh := make(chan error, 1)

	go func() {
		errCh <- sendClientHelloChunked(client, payload)
		client.Close()
	}()

	received := make([]byte, len(payload))
	_, err := io.ReadFull(server, received)
	if err != nil {
		t.Fatalf("Lỗi đọc từ pipe: %v", err)
	}

	if sendErr := <-errCh; sendErr != nil {
		t.Fatalf("sendClientHelloChunked lỗi: %v", sendErr)
	}

	if !bytes.Equal(received, payload) {
		t.Fatalf("Dữ liệu nhận được không khớp: got %s, want %s", received, payload)
	}
}

func TestExtractSNI_InvalidData(t *testing.T) {
	if got := extractSNI([]byte{}); got != "" {
		t.Errorf("Mong đợi rỗng với payload rỗng, nhận: %q", got)
	}
	if got := extractSNI([]byte{0x16, 0x03, 0x01}); got != "" {
		t.Errorf("Mong đợi rỗng với payload thiếu, nhận: %q", got)
	}
}
