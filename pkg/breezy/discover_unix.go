// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux || darwin || freebsd || netbsd || openbsd

package breezy

import (
	"net"
	"syscall"
)

// enableBroadcast turns on SO_BROADCAST on uc's underlying file
// descriptor. Required by some kernels before they'll accept sends to
// 255.255.255.255 or subnet-directed broadcasts from an unconnected
// UDP socket. Errors are deliberately swallowed: a kernel that
// permits broadcasts without the option set will still work, and a
// kernel that requires it but rejects the setsockopt will surface the
// failure on the subsequent WriteTo where it's actually meaningful.
func enableBroadcast(uc *net.UDPConn) {
	rc, err := uc.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
}
