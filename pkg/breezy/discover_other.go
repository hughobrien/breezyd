// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package breezy

import "net"

// enableBroadcast is a no-op on platforms where we don't currently set
// SO_BROADCAST. Discovery on Windows etc. falls through to whatever
// net.ListenPacket configures by default; making it a first-class
// platform is a v1.x item.
func enableBroadcast(_ *net.UDPConn) {}
