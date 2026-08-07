package healthcheck

import (
	"context"
	"fmt"
	"net"
)

func Check(ctx context.Context, address string) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("连接服务端口: %w", err)
	}
	return connection.Close()
}
