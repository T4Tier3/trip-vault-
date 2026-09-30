package middleware

import (
	"errors"
	"net"
	"syscall"
)

// isBrokenPipe reports whether err is a client disconnect rather than a server
// fault.
//
// Go surfaces a reset connection as EPIPE or ECONNRESET, which on mobile is the
// normal result of a user backgrounding the app or losing signal. Logging those
// at error level would bury real problems.
func isBrokenPipe(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		err = opErr.Err
	}
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		// A write on a connection the peer already closed.
		return netErr.Timeout() && opErr != nil
	}
	return false
}
