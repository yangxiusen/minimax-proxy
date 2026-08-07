package healthcheck

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestCheckConnectsToTCPListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Check(ctx, listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsUnavailableAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Check(ctx, address); err == nil {
		t.Fatal("Check() error = nil")
	}
}
