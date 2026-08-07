package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"minimax-h3-tc/internal/healthcheck"
)

func main() {
	address := flag.String("address", "127.0.0.1:8080", "服务 TCP 地址")
	timeout := flag.Duration("timeout", 3*time.Second, "连接超时")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := healthcheck.Check(ctx, *address); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
