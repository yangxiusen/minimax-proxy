package artifact

import (
	"net/netip"
	"net/url"
	"strings"

	"minimax-h3-tc/internal/netguard"
)

// LegacyPublicURL accepts only an already-public HTTP(S) URL. It deliberately
// rejects literal private hosts so historical node addresses cannot leak back
// through V2 or Manager responses.
func LegacyPublicURL(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", false
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", false
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if address, err := netip.ParseAddr(hostname); err == nil {
		if netguard.IsProtectedAddress(address.Unmap()) {
			return "", false
		}
		return value, true
	}
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") || !strings.Contains(hostname, ".") || looksLikeIPv4NumberHost(hostname) {
		return "", false
	}
	return value, true
}

func looksLikeIPv4NumberHost(hostname string) bool {
	parts := strings.Split(hostname, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		digits, base := part, byte(10)
		if len(part) > 2 && strings.HasPrefix(part, "0x") {
			digits, base = part[2:], 16
		} else if len(part) > 1 && part[0] == '0' {
			digits, base = part[1:], 8
		}
		if digits == "" || !allDigitsInBase(digits, base) {
			return false
		}
	}
	return true
}

func allDigitsInBase(value string, base byte) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= '0' && character <= '9' && character-'0' < base {
			continue
		}
		if base == 16 && character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}
