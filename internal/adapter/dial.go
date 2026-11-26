package adapter

import (
	"errors"
	"net"
	"time"
)

// Dialer returns a dialer with timeout that refuses link-local and cloud metadata addresses, as NewHTTPClient's
// does, for outbound connections that are not an adapter's (alert channels, mail).
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: refuseLinkLocal}
}

// IsRefusedAddress reports whether err comes from a dial to a link-local or cloud metadata address.
func IsRefusedAddress(err error) bool { return errors.Is(err, errRefusedAddress) }
