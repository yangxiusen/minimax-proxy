package logsafe

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestErrorKeepsCauseWithoutSensitiveLocations(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "signed media url", err: &url.Error{Op: "Get", URL: "https://media.example/input.png?token=secret", Err: errors.New("i/o timeout")}, want: "i/o timeout"},
		{name: "raw private url", err: errors.New("request http://private.local:8201/path?token=secret failed"), want: "request [redacted-url] failed"},
		{name: "private address", err: errors.New("dial tcp 10.0.0.8:8201: connection refused"), want: "dial tcp [redacted-address] connection refused"},
		{name: "private hostname", err: errors.New("dial tcp node.internal:8201: connection refused"), want: "dial tcp [redacted-address] connection refused"},
		{name: "base64", err: errors.New("invalid data:image/png;base64,cHJpdmF0ZQ== payload"), want: "invalid [redacted-data-uri] payload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Error(test.err)
			if got != test.want {
				t.Fatalf("Error() = %q, want %q", got, test.want)
			}
			for _, forbidden := range []string{"secret", "private.local", "node.internal", "10.0.0.8", "cHJpdmF0ZQ"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("Error() leaked %q: %q", forbidden, got)
				}
			}
		})
	}
}
