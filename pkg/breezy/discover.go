// SPDX-License-Identifier: GPL-3.0-or-later

// Local-network discovery for Vents Twinfresh Breezy ERVs. Per the vendor
// manual, sending any FDFD/02 request whose deviceID field is the literal
// "DEFAULT_DEVICEID" causes the device to respond regardless of the password
// in the request. The response packet's deviceID field is also
// "DEFAULT_DEVICEID"; the *real* 16-byte device ID is returned inside the
// DATA block as the value of parameter 0x007C, and the unit type lives at
// 0x00B9. We use that to enumerate devices on the LAN with a UDP broadcast.
package breezy

import (
	"context"
	"encoding/binary"
	"net"
	"time"
)

// DefaultDeviceID is the wildcard device ID used for discovery. Sending a
// request with this ID causes any Breezy device on the network to reply
// with its real ID and unit type, regardless of the request's password.
const DefaultDeviceID = "DEFAULT_DEVICEID"

// DefaultDiscoveryPassword is sent in discovery requests. Per the manual,
// any password works for the wildcard ID; we use "1111" because that's the
// device's factory default and matches the value the official app uses.
const DefaultDiscoveryPassword = "1111"

// DefaultBroadcasts is the list of UDP destination addresses Discover sends
// the wildcard request to. We try the limited broadcast (255.255.255.255)
// plus the two most common consumer-router subnets (192.168.0.0/24 and
// 192.168.1.0/24); subnet-directed broadcasts reach devices the kernel may
// drop the limited broadcast for.
var DefaultBroadcasts = []string{
	"255.255.255.255:4000",
	"192.168.0.255:4000",
	"192.168.1.255:4000",
}

// discoverDeadline is the default total time Discover/DiscoverAt will wait
// for replies after sending the wildcard request, when the caller's context
// has no earlier deadline.
const discoverDeadline = 2 * time.Second

// Found describes one device that answered a discovery probe.
type Found struct {
	// IP is the source address the response came from (no port). For IPv6
	// callers this is the textual address; for IPv4 it's the dotted quad.
	IP string

	// DeviceID is the device's real 16-character identifier, extracted
	// from parameter 0x007C of the response data block.
	DeviceID string

	// UnitType is the model code reported via parameter 0x00B9 (e.g. 17 =
	// Breezy 160). Zero if the device didn't include the parameter.
	UnitType uint16
}

// localBroadcasts returns the directed-broadcast address for every
// up, non-loopback IPv4 interface on the host, formatted as "a.b.c.d:4000".
// These are appended to DefaultBroadcasts by Discover so that a host on
// any subnet (not just 192.168.0/1.0/24) can reach its own LAN devices.
// Errors enumerating interfaces are silently ignored — the caller always
// has DefaultBroadcasts as a fallback.
func localBroadcasts() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			// Compute directed broadcast: host bits all 1.
			mask := ipNet.Mask
			bcast := make(net.IP, 4)
			for i := range 4 {
				bcast[i] = ip4[i] | ^mask[i]
			}
			out = append(out, bcast.String()+":4000")
		}
	}
	return out
}

// Discover broadcasts a wildcard read for parameters 0x007C (device ID)
// and 0x00B9 (unit type) on UDP/4000 to DefaultBroadcasts plus the
// directed-broadcast address of every local IPv4 interface, and listens
// for replies until ctx is done or ~2s elapses, whichever comes first.
// Each distinct (IP, DeviceID) is returned once.
//
// The returned slice is empty (not nil-on-error) when no devices answered.
// Errors only surface for socket-level failures (e.g. the local UDP listen
// failed); per-target send/recv errors are non-fatal.
func Discover(ctx context.Context) ([]Found, error) {
	targets := append([]string(nil), DefaultBroadcasts...)
	seen := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		seen[t] = struct{}{}
	}
	for _, t := range localBroadcasts() {
		if _, dup := seen[t]; !dup {
			targets = append(targets, t)
			seen[t] = struct{}{}
		}
	}
	return discoverInternal(ctx, targets, true)
}

// DiscoverAt is the test- and unicast-friendly variant of Discover: instead
// of broadcasting it sends the wildcard request to each of targets (which
// may be unicast addresses such as "127.0.0.1:54321"). The reply parsing
// and dedupe behavior matches Discover.
func DiscoverAt(ctx context.Context, targets []string) ([]Found, error) {
	return discoverInternal(ctx, targets, false)
}

func discoverInternal(ctx context.Context, targets []string, broadcast bool) ([]Found, error) {
	// Bind to an ephemeral local port. ListenPacket on "udp" gives us an
	// unconnected socket — required because we want to receive replies
	// from arbitrary peers, not just one we Dial'd.
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer pc.Close()

	if broadcast {
		// Some kernels (Linux included, depending on configuration) refuse
		// sends to 255.255.255.255 / 192.168.x.255 from an unconnected UDP
		// socket unless SO_BROADCAST is set on the underlying fd. Older
		// versions of this code relied on the loose default, which works
		// on most Linux boxes but isn't portable. enableBroadcast wraps
		// the syscall in a build-tag-conditional helper so unix builds get
		// the explicit setsockopt while non-unix targets fall through to
		// whatever net.ListenPacket left us with (Windows discovery is a
		// "v1.x feature" — see CHANGELOG).
		if uc, ok := pc.(*net.UDPConn); ok {
			_ = uc.SetReadBuffer(64 * 1024)
			enableBroadcast(uc)
		}
	}

	req := EncodeRequest(DefaultDeviceID, DefaultDiscoveryPassword, FuncRead,
		BuildReadDataBlock([]ParamID{0x007C, 0x00B9}))

	for _, t := range targets {
		addr, err := net.ResolveUDPAddr("udp4", t)
		if err != nil {
			continue
		}
		// Best-effort: a per-target write failure (e.g. "no route to
		// host" for a subnet that isn't local) shouldn't abort the
		// rest of the sweep.
		_, _ = pc.WriteTo(req, addr)
	}

	// Compute the listen deadline. Use the earliest of (now+2s, ctx
	// deadline) so callers can shorten the wait via context, but a
	// no-deadline context still bounds us at 2s.
	deadline := time.Now().Add(discoverDeadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	// If the context is canceled mid-listen, unblock ReadFrom by closing
	// the socket. The defer'd Close above is for the success path; this
	// goroutine handles the cancel path. We use a stop channel so the
	// goroutine exits cleanly when the listen loop finishes naturally.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = pc.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()
	defer close(stop)

	if err := pc.SetReadDeadline(deadline); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var out []Found
	buf := make([]byte, 2048)
	for {
		n, peer, err := pc.ReadFrom(buf)
		if err != nil {
			// Read deadline fired or socket closed — listen loop is done.
			break
		}
		f, ok := parseDiscoveryResponse(buf[:n], peer)
		if !ok {
			continue
		}
		key := f.IP + "|" + f.DeviceID
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, f)
	}

	// If the context was canceled, surface that to the caller — but only
	// if we have nothing useful to return. A cancel that happened *after*
	// we collected real results is still a partial success, and callers
	// who care can re-check ctx.Err() themselves.
	if err := ctx.Err(); err != nil && len(out) == 0 {
		return nil, err
	}
	return out, nil
}

// parseDiscoveryResponse decodes one UDP reply to a wildcard probe. It
// returns ok=false (and skips the response) when the frame is malformed,
// authentication-tagged, or doesn't carry a real device ID.
func parseDiscoveryResponse(raw []byte, peer net.Addr) (Found, bool) {
	// The response echoes our outgoing wildcard ID and password — that's
	// what we used to encode the request, so that's what DecodeResponse
	// must compare against.
	_, body, err := DecodeResponse(raw, DefaultDeviceID, DefaultDiscoveryPassword)
	if err != nil {
		return Found{}, false
	}

	parsed, err := ParseDataBlock(body)
	if err != nil {
		return Found{}, false
	}

	var realID string
	var unitType uint16
	for _, p := range parsed {
		switch p.ID {
		case 0x007C:
			if !p.Unsupported {
				realID = string(p.Value)
			}
		case 0x00B9:
			if !p.Unsupported && len(p.Value) >= 2 {
				unitType = binary.LittleEndian.Uint16(p.Value[:2])
			}
		}
	}
	if realID == "" {
		// Without a real ID we can't dedupe or address the device, so
		// drop the response.
		return Found{}, false
	}

	ip, _, err := net.SplitHostPort(peer.String())
	if err != nil {
		// Defensive: net.UDPAddr.String always includes a port, but if
		// some odd Addr surfaces a hostless string, fall back to using
		// the whole thing as the IP.
		ip = peer.String()
	}
	return Found{IP: ip, DeviceID: realID, UnitType: unitType}, true
}
