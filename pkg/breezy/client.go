// UDP client for the FDFD/02 protocol. Wraps the codec in a goroutine-safe
// Client that owns a single UDP socket and serialises request/response
// cycles via a mutex. Per-request timeout, exponential-backoff retries, and
// context cancellation are handled here so callers can keep their code
// straight-line.
package breezy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ErrUnsupported is returned by ReadParam when the device replied with an
// FD <id> "not supported" marker for the requested parameter. ReadParams
// signals the same condition by omitting the ID from its result map.
var ErrUnsupported = errors.New("breezy: parameter unsupported by device")

// Defaults applied by NewClient when no matching Option is supplied.
const (
	defaultPort    = 4000
	defaultTimeout = 1500 * time.Millisecond
	defaultRetries = 2
	defaultBackoff = 200 * time.Millisecond

	// readBufSize is generous: a full FDFD/02 packet is bounded by the
	// 1-byte SIZE_PWD field and the protocol's data block, but rather
	// than tighten this we just match the fakedevice's buffer.
	readBufSize = 2048
)

// Option configures a Client at construction time.
type Option func(*Client)

// WithTimeout sets the per-request UDP read/write deadline. The effective
// deadline is min(timeout, ctx.Deadline()) per attempt.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithRetries sets the maximum number of retries after the first attempt.
// A value of 0 disables retries (one attempt total).
func WithRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.retries = n
	}
}

// WithBackoff sets the initial backoff between retry attempts. Subsequent
// retries double this delay (200ms -> 400ms -> 800ms ...).
func WithBackoff(d time.Duration) Option {
	return func(c *Client) { c.backoff = d }
}

// Client speaks FDFD/02 over UDP to a single device. It is safe for use
// from multiple goroutines: a mutex serialises the request/response cycle
// so concurrent callers don't pick up each other's reply frames.
type Client struct {
	addr     string
	deviceID string
	password string
	timeout  time.Duration
	retries  int
	backoff  time.Duration

	mu   sync.Mutex // serialises exchange()
	conn *net.UDPConn
}

// NewClient dials a UDP "connection" (in the connect(2) sense — UDP is
// connectionless, but Dial fixes the peer for the socket). addr may be
// "host" (port defaults to 4000) or "host:port".
func NewClient(addr, deviceID, password string, opts ...Option) (*Client, error) {
	if len(deviceID) != 16 {
		return nil, fmt.Errorf("breezy: deviceID must be 16 bytes, got %d", len(deviceID))
	}
	if len(password) > 8 {
		return nil, fmt.Errorf("breezy: password must be <= 8 bytes, got %d", len(password))
	}

	full := addr
	if !strings.Contains(addr, ":") || strings.HasSuffix(addr, "]") {
		// "host" or bare "[v6addr]" without a port -> append default.
		full = fmt.Sprintf("%s:%d", addr, defaultPort)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", full)
	if err != nil {
		return nil, fmt.Errorf("breezy: resolve %q: %w", full, err)
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, fmt.Errorf("breezy: dial %q: %w", full, err)
	}

	c := &Client{
		addr:     full,
		deviceID: deviceID,
		password: password,
		timeout:  defaultTimeout,
		retries:  defaultRetries,
		backoff:  defaultBackoff,
		conn:     conn,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Close closes the underlying socket. After Close, all subsequent calls
// return an error. Close is idempotent.
func (c *Client) Close() error {
	return c.conn.Close()
}

// ReadParam reads a single parameter and returns the device's value bytes
// (little-endian). Returns ErrUnsupported if the device responded with an
// FD <id> marker.
func (c *Client) ReadParam(ctx context.Context, id ParamID) ([]byte, error) {
	out, err := c.ReadParams(ctx, []ParamID{id})
	if err != nil {
		return nil, err
	}
	v, ok := out[id]
	if !ok {
		return nil, ErrUnsupported
	}
	return v, nil
}

// ReadParams batches many reads into one packet and returns a map keyed by
// the IDs the device acknowledged. Parameters reported as unsupported are
// omitted from the map — the caller can detect them by looking for missing
// keys. On packet-level error (timeout, checksum, auth, ctx cancel), the
// returned map is nil.
func (c *Client) ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
	if len(ids) == 0 {
		return map[ParamID][]byte{}, nil
	}
	body, err := c.exchange(ctx, FuncRead, BuildReadDataBlock(ids))
	if err != nil {
		return nil, err
	}
	parsed, err := ParseDataBlock(body)
	if err != nil {
		return nil, err
	}
	out := make(map[ParamID][]byte, len(parsed))
	for _, p := range parsed {
		if p.Unsupported {
			continue
		}
		out[p.ID] = p.Value
	}
	return out, nil
}

// WriteParam writes a single parameter. 1-byte values use the implicit
// 1-byte form; longer values are framed with FE <size>.
func (c *Client) WriteParam(ctx context.Context, id ParamID, value []byte) error {
	return c.WriteParams(ctx, []ParamWrite{{ID: id, Value: value}})
}

// WriteParams batches multiple writes into one packet. Uses FUNC=0x03
// (write-with-response) so we can detect transport-level failures: if no
// reply arrives, the retry/timeout machinery kicks in.
func (c *Client) WriteParams(ctx context.Context, writes []ParamWrite) error {
	if len(writes) == 0 {
		return nil
	}
	_, err := c.exchange(ctx, FuncWriteWithReply, BuildWriteDataBlock(writes))
	return err
}

// exchange runs one request/response cycle with retries and timeouts. It
// holds c.mu for the entire call so concurrent goroutines don't read each
// other's response frames off the shared socket.
//
// The request is regenerated on each attempt — real devices occasionally
// drop the first packet, and rebuilding is cheap and keeps all attempts
// independent of any state we might add later (sequence numbers etc.).
func (c *Client) exchange(ctx context.Context, fn byte, dataBlock []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	backoff := c.backoff
	var lastErr error

	for attempt := 0; attempt <= c.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		body, err := c.attempt(ctx, fn, dataBlock)
		if err == nil {
			return body, nil
		}

		// Auth failures are deterministic — retrying won't help and we'd
		// just hide the real error. Bail out immediately.
		if errors.Is(err, ErrAuth) {
			return nil, err
		}
		// Context cancellation/deadline propagates up unchanged so callers
		// can use errors.Is(err, context.DeadlineExceeded) etc.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		lastErr = err

		if attempt == c.retries {
			break
		}

		// Sleep before the next attempt, but wake up early if ctx is done.
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}

	if lastErr == nil {
		// Defensive — exchange should always have set lastErr if we fell
		// through the loop without returning success.
		lastErr = errors.New("breezy: exchange exhausted retries")
	}
	return nil, lastErr
}

// attempt performs one send + recv with the appropriate deadline. It does
// not retry; that's exchange's job.
func (c *Client) attempt(ctx context.Context, fn byte, dataBlock []byte) ([]byte, error) {
	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("breezy: set deadline: %w", err)
	}

	req := EncodeRequest(c.deviceID, c.password, fn, dataBlock)
	if _, err := c.conn.Write(req); err != nil {
		return nil, mapNetErr(err, ctx)
	}

	buf := make([]byte, readBufSize)
	n, err := c.conn.Read(buf)
	if err != nil {
		return nil, mapNetErr(err, ctx)
	}

	_, body, derr := DecodeResponse(buf[:n], c.deviceID, c.password)
	if derr != nil {
		return nil, derr
	}

	// Copy out — buf is reused on the next attempt.
	out := make([]byte, len(body))
	copy(out, body)
	return out, nil
}

// mapNetErr converts net-level errors into the typed errors callers expect.
// In particular, a deadline-exceeded error on the conn becomes
// context.DeadlineExceeded only when the *context* deadline is what fired;
// a per-request timeout that the context outlives still surfaces as the
// underlying net.OpError, which exchange treats as retryable.
func mapNetErr(err error, ctx context.Context) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
