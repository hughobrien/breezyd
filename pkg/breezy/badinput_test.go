// SPDX-License-Identifier: GPL-3.0-or-later

package breezy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// Bad caller input (reserved param IDs, oversize values, over-long
// passwords) must come back as typed errors wrapping ErrInvalidArg, never
// as panics (#260).

func TestClient_BadInput_ReturnsErrors(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)
	ctx := context.Background()
	big := make([]byte, 256)

	cases := []struct {
		name string
		do   func() error
		want error
	}{
		{"ReadParams reserved FC", func() error { _, err := c.ReadParams(ctx, []breezy.ParamID{0x00FC}); return err }, breezy.ErrReservedParamID},
		{"ReadParams reserved FF on page 1", func() error { _, err := c.ReadParams(ctx, []breezy.ParamID{0x0001, 0x01FF}); return err }, breezy.ErrReservedParamID},
		{"ReadParam reserved FE", func() error { _, err := c.ReadParam(ctx, 0x00FE); return err }, breezy.ErrReservedParamID},
		{"WriteParams reserved FD", func() error {
			return c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x00FD, Value: []byte{1}}})
		}, breezy.ErrReservedParamID},
		{"WriteParamsUnsafe reserved FF", func() error {
			return c.WriteParamsUnsafe(ctx, []breezy.ParamWrite{{ID: 0x00FF, Value: []byte{1}}})
		}, breezy.ErrReservedParamID},
		{"WriteParams oversize", func() error {
			return c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0077, Value: big}})
		}, breezy.ErrValueTooLarge},
		{"WriteParamsUnsafe oversize", func() error {
			return c.WriteParamsUnsafe(ctx, []breezy.ParamWrite{{ID: 0x0077, Value: big}})
		}, breezy.ErrValueTooLarge},
		{"WriteParams empty value", func() error {
			return c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0077, Value: nil}})
		}, breezy.ErrInvalidArg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panicked: %v", r)
					}
				}()
				err = tc.do()
			}()
			is.True(errors.Is(err, tc.want))              // typed sentinel
			is.True(errors.Is(err, breezy.ErrInvalidArg)) // maps to bad_request / exit 2
		})
	}
}

func TestClient_MaxSizeValueAccepted(t *testing.T) {
	is := is.New(t)
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)
	// 255 bytes is the largest the FE size byte can carry.
	err := c.WriteParamsUnsafe(context.Background(), []breezy.ParamWrite{{ID: 0x0077, Value: make([]byte, 255)}})
	is.True(!errors.Is(err, breezy.ErrInvalidArg)) // 255 is legal
}

func TestNewClient_LongPassword(t *testing.T) {
	is := is.New(t)
	_, err := breezy.NewClient("127.0.0.1:4000", testDeviceID, "123456789")
	is.True(errors.Is(err, breezy.ErrPasswordTooLong))
	is.True(errors.Is(err, breezy.ErrInvalidArg))
}

// MemClient must reject exactly what the UDP client rejects.
func TestMemClient_BadInput_ReturnsErrors(t *testing.T) {
	c := breezy.NewMemClient(nil)
	ctx := context.Background()
	big := make([]byte, 256)

	_, err := c.ReadParams(ctx, []breezy.ParamID{0x00FF})
	if !errors.Is(err, breezy.ErrReservedParamID) || !errors.Is(err, breezy.ErrInvalidArg) {
		t.Errorf("ReadParams reserved: %v", err)
	}
	err = c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x01FC, Value: []byte{1}}})
	if !errors.Is(err, breezy.ErrReservedParamID) || !errors.Is(err, breezy.ErrInvalidArg) {
		t.Errorf("WriteParams reserved: %v", err)
	}
	err = c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0077, Value: big}})
	if !errors.Is(err, breezy.ErrValueTooLarge) || !errors.Is(err, breezy.ErrInvalidArg) {
		t.Errorf("WriteParams oversize: %v", err)
	}
	err = c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0077, Value: nil}})
	if !errors.Is(err, breezy.ErrInvalidArg) {
		t.Errorf("WriteParams empty: %v", err)
	}
	got, _ := c.ReadParams(ctx, []breezy.ParamID{0x0077})
	if len(got) != 0 {
		t.Errorf("rejected write leaked into store: %v", got)
	}
}

// A password over the protocol's 8 bytes must be an error, not a panic in
// EncodeRequest (it used to crash breezyd at startup).
func TestDiscover_LongPassword_ReturnsError(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	_, err := breezy.DiscoverWithPassword(ctx, "123456789")
	is.True(errors.Is(err, breezy.ErrPasswordTooLong))
	is.True(errors.Is(err, breezy.ErrInvalidArg))
	_, err = breezy.DiscoverAtWithPassword(ctx, []string{"127.0.0.1:1"}, "123456789")
	is.True(errors.Is(err, breezy.ErrPasswordTooLong))
}
