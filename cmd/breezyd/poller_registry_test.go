// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
	"github.com/prometheus/client_golang/prometheus"
)

// A poller built with a registry must follow the registry's current
// address: discovery moving the device between ticks redirects the next poll.
func TestPoller_FollowsRegistryIPChange(t *testing.T) {
	is := is.New(t)
	srvA := newFakeServer(t)
	srvB := newFakeServer(t)
	srvA.SetParamValue(0x0044, []byte{10})
	srvB.SetParamValue(0x0044, []byte{20})
	state := newTestState(t, nil)
	reg := NewDeviceRegistry(map[string]DeviceConfig{
		"dev": {ID: pollerTestDeviceID, Password: pollerTestPassword, IP: srvA.Addr()},
	})
	p := &Poller{
		Name: "dev", Devices: reg, Interval: time.Hour, State: state,
		ReadIDs: []breezy.ParamID{0x0044},
	}

	p.tick(context.Background())
	snap, _ := state.Get("dev")
	is.NoErr(snap.LastErr)
	is.Equal(snap.IP, srvA.Addr())
	is.Equal(snap.Values[0x0044], []byte{10})

	_, _ = reg.UpdateIP("dev", srvB.Addr())
	p.tick(context.Background())
	snap, _ = state.Get("dev")
	is.NoErr(snap.LastErr)
	is.Equal(snap.IP, srvB.Addr()) // snapshot reports the new address
	is.Equal(snap.Values[0x0044], []byte{20})
}

// A device with no address yet is recorded as unreachable (a snapshot with an
// error, so the dashboard shows it), then polled normally once discovery
// supplies an address.
func TestPoller_NoIPYet_ThenDiscovered(t *testing.T) {
	is := is.New(t)
	srv := newFakeServer(t)
	state := newTestState(t, nil)
	reg := NewDeviceRegistry(map[string]DeviceConfig{
		"dev": {ID: pollerTestDeviceID, Password: pollerTestPassword},
	})
	p := &Poller{
		Name: "dev", Devices: reg, Interval: time.Hour, State: state,
		ReadIDs: []breezy.ParamID{0x0044},
	}

	p.tick(context.Background())
	snap, ok := state.Get("dev")
	is.True(ok)                  // unreachable device still has a cache entry
	is.True(snap.LastErr != nil) // "no IP yet"
	is.True(snap.LastPoll.IsZero())

	_, _ = reg.UpdateIP("dev", srv.Addr())
	p.tick(context.Background())
	snap, _ = state.Get("dev")
	is.NoErr(snap.LastErr)
	is.Equal(snap.IP, srv.Addr())
	is.True(!snap.LastPoll.IsZero())
}

// startPollers must build a poller and scheduler for a device that has no
// IP yet.
func TestStartPollers_CreatesWorkersForDeviceWithoutIP(t *testing.T) {
	is := is.New(t)
	reg := NewDeviceRegistry(map[string]DeviceConfig{
		"noip": {ID: pollerTestDeviceID, Password: pollerTestPassword},
	})
	h := &Handler{}
	pollers, schedulers, _, _ := startPollers(
		context.Background(), reg, time.Hour, t.TempDir(), newTestState(t, nil),
		NewMetrics(prometheus.NewRegistry()), nil, nil, h.scheduleDial, nil,
	)
	is.True(pollers["noip"] != nil)
	is.True(schedulers["noip"] != nil)
}
