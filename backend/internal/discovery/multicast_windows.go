//go:build windows

package discovery

import (
	"net"
	"syscall"
)

// Select the outgoing interface explicitly; binding the local source address
// alone does not override every OS's default multicast route. Keep TTL local.
func configureMulticast(conn *net.UDPConn, ip net.IP) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var address [4]byte
	copy(address[:], ip.To4())
	var optionErr error
	err = raw.Control(func(fd uintptr) {
		if optionErr = syscall.SetsockoptInet4Addr(syscall.Handle(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, address); optionErr != nil {
			return
		}
		if optionErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 1); optionErr != nil {
			return
		}
		optionErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1)
	})
	if err != nil {
		return err
	}
	return optionErr
}
