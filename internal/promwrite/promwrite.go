// SPDX-License-Identifier: GPL-3.0-or-later

// Package promwrite sends samples with remote write 1.0 to named targets
// (VictoriaMetrics, Prometheus, or anything else that takes it) and asks a
// target for the newest sample of a series, so a poller that pushes past data
// can carry on where it stopped.
//
// The wire format is small enough to write by hand: a protobuf WriteRequest
// (https://prometheus.io/docs/specs/prw/remote_write_spec/) in a snappy block.
// The snappy block is made of literals only, which is valid snappy that any
// decoder reads; the payloads are a few hundred samples, so compression does
// not matter.
//
// This package is the same in emporiad, kasad, comfortd and breezyd; change
// the copies together.
package promwrite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Series is one metric's labels (including __name__) and its samples, oldest
// first.
type Series struct {
	Labels  map[string]string
	Samples []Sample
}

// Sample is one value at one time.
type Sample struct {
	T time.Time
	V float64
}

// Client talks to one receiver.
type Client struct {
	Base string // e.g. http://127.0.0.1:8428
	HTTP *http.Client
}

// RefusedError is a write the receiver answered with a 4xx other than 429:
// sending the same data again will fail the same way.
type RefusedError struct {
	Status int
	Body   string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("remote write refused: HTTP %d: %s", e.Status, e.Body)
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Write sends the series in one request. A *RefusedError means the data will
// never be accepted; any other error may succeed on a retry. Resending samples
// the receiver already has (same time, same value) is harmless.
func (c *Client) Write(ctx context.Context, series []Series) error {
	body := snappyLiterals(Encode(series))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Base, "/")+"/api/v1/write", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusTooManyRequests:
		return &RefusedError{Status: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	default:
		return fmt.Errorf("remote write: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
}

// Encode marshals a WriteRequest. Labels are sorted by name, as the spec
// requires.
func Encode(series []Series) []byte {
	var out []byte
	for _, s := range series {
		var ts []byte
		names := make([]string, 0, len(s.Labels))
		for n := range s.Labels {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			var l []byte
			l = protowire.AppendTag(l, 1, protowire.BytesType)
			l = protowire.AppendString(l, n)
			l = protowire.AppendTag(l, 2, protowire.BytesType)
			l = protowire.AppendString(l, s.Labels[n])
			ts = protowire.AppendTag(ts, 1, protowire.BytesType)
			ts = protowire.AppendBytes(ts, l)
		}
		for _, p := range s.Samples {
			var m []byte
			m = protowire.AppendTag(m, 1, protowire.Fixed64Type)
			m = protowire.AppendFixed64(m, math.Float64bits(p.V))
			m = protowire.AppendTag(m, 2, protowire.VarintType)
			m = protowire.AppendVarint(m, uint64(p.T.UnixMilli()))
			ts = protowire.AppendTag(ts, 2, protowire.BytesType)
			ts = protowire.AppendBytes(ts, m)
		}
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, ts)
	}
	return out
}

// snappyLiterals wraps b in a snappy block made only of literal elements: the
// uncompressed length as a varint, then chunks of at most 64 KiB, each with a
// tag byte of 61<<2 followed by its length minus one in two little-endian
// bytes.
func snappyLiterals(b []byte) []byte {
	out := protowire.AppendVarint(nil, uint64(len(b)))
	for len(b) > 0 {
		n := min(len(b), 1<<16)
		out = append(out, 61<<2, byte(n-1), byte((n-1)>>8))
		out = append(out, b[:n]...)
		b = b[n:]
	}
	return out
}

// Newest returns the newest sample time of the series the selector matches,
// looking back over lookback. ok is false when there is none.
func (c *Client) Newest(ctx context.Context, selector string, lookback time.Duration) (t time.Time, ok bool, err error) {
	// timestamp() of a plain selector is the sample's own time; the subquery
	// steps every minute, inside the 5m lookback, so no sample is skipped.
	q := fmt.Sprintf("max(max_over_time(timestamp(%s)[%ds:1m]))", selector, int(lookback.Seconds()))
	u := strings.TrimRight(c.Base, "/") + "/api/v1/query?query=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return time.Time{}, false, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return time.Time{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return time.Time{}, false, fmt.Errorf("prometheus query: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var r struct {
		Status string
		Data   struct {
			Result []struct {
				Value [2]any
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return time.Time{}, false, err
	}
	if r.Status != "success" {
		return time.Time{}, false, errors.New("prometheus query: status " + r.Status)
	}
	if len(r.Data.Result) == 0 {
		return time.Time{}, false, nil
	}
	s, _ := r.Data.Result[0].Value[1].(string)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("prometheus query: value %q: %w", s, err)
	}
	return time.UnixMilli(int64(math.Round(f * 1000))).UTC(), true, nil
}
