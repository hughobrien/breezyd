// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// postJSON POSTs the given body as application/json and returns the
// response. Mirrors http.PostForm but for the JSON-bodied action
// handlers.
func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// fakePushHub records every Notify call. Action-handler tests inject one
// to verify the post-write fan-out fires without spinning up a real
// PushHub + render closure.
type fakePushHub struct {
	mu       sync.Mutex
	notified []string
}

func (f *fakePushHub) Notify(name string, _ Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified = append(f.notified, name)
}

func (f *fakePushHub) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notified...)
}

func (f *fakePushHub) assertCalledFor(t *testing.T, want string) {
	t.Helper()
	got := f.calls()
	if len(got) != 1 || got[0] != want {
		t.Errorf("PushHub.Notify calls: got %v, want [%s]", got, want)
	}
}

// attachFakePushHub wires a fakePushHub into h and returns it. The
// returned value is shared by reference, so observed calls from inside
// the handler are visible after the request returns.
func attachFakePushHub(h *Handler) *fakePushHub {
	f := &fakePushHub{}
	h.PushHub = f
	return f
}

// assertSSEErrorBody fails the test if body doesn't look like a
// datastar-patch-elements event containing wantSubstr inside its
// elements payload.
func assertSSEErrorBody(t *testing.T, body []byte, wantSubstr string) {
	t.Helper()
	s := string(body)
	if !strings.Contains(s, "event: datastar-patch-elements") {
		t.Errorf("body missing SSE event: %s", s)
	}
	if !strings.Contains(s, "#global-error-banner") {
		t.Errorf("body missing #global-error-banner selector: %s", s)
	}
	if wantSubstr != "" && !strings.Contains(s, wantSubstr) {
		t.Errorf("body missing %q: %s", wantSubstr, s)
	}
}

// newUIWriteTestHandler builds a Handler for write-path UI tests. It seeds a
// Snapshot so viewFor works, and wires a real ClientFactory that dials the
// fakedevice so actual UDP writes succeed.
func newUIWriteTestHandler(t *testing.T) *Handler {
	t.Helper()
	addr := newServerFakeDevice(t)
	h := newTestHandler(t, map[string]DeviceConfig{
		"alpha": {ID: srvDeviceID, Password: srvPassword, IP: addr},
	},
		withState(newTestState(t, map[string]Snapshot{
			"alpha": {
				IP:       addr,
				LastPoll: time.Now(),
				Values: map[breezy.ParamID][]byte{
					0x0001: {0x01}, // power on
					0x0002: {0xFF}, // manual mode
					0x0044: {0x32}, // manual 50%
					0x00B7: {0x01}, // regeneration
					0x0068: {0x00}, // heater off
					0x0088: {0x00}, // filter clean
					0x0083: {0x00}, // fault none
				},
			},
		})),
		withPollers(map[string]*Poller{}),
		withSchedulers(map[string]*Scheduler{}),
	)
	h.ClientFactory = func(name string) (HandlerClient, error) {
		d, ok := h.Devices.Get(name)
		if !ok {
			return nil, fmt.Errorf("unknown device %q", name)
		}
		return breezy.NewClient(d.IP, d.ID, d.Password,
			breezy.WithRetries(0), breezy.WithTimeout(500*time.Millisecond))
	}
	return h
}

func TestUIWritePower_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/power", map[string]any{"on": true})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	notifies.assertCalledFor(t, "alpha")
}

// TestUIWriteAction_NotFound pins that every action endpoint returns 404
// for an unknown device name. Each route's body shape varies (power/heater
// take {"on":bool}, mode/timer take {"mode":...}, speed takes {"manual":N},
// reset-filter/faults take no body), but the unknown-device branch is the
// same in all of them — one table covers the surface.
func TestUIWriteAction_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	cases := []struct {
		name string
		path string
		body any // nil → POST with empty body (reset-filter / reset-faults)
	}{
		{"Power", "power", map[string]any{"on": true}},
		{"Mode", "mode", map[string]any{"mode": "regeneration"}},
		{"Speed", "speed", map[string]any{"manual": 50}},
		{"Heater", "heater", map[string]any{"on": true}},
		{"Timer", "timer", map[string]any{"mode": "night"}},
		{"ResetFilter", "reset-filter", nil},
		{"ResetFaults", "reset-faults", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			var resp *http.Response
			if tc.body == nil {
				r, err := http.Post(srv.URL+"/ui/devices/nope/"+tc.path, "", nil)
				is.NoErr(err)
				resp = r
			} else {
				resp = postJSON(t, srv.URL+"/ui/devices/nope/"+tc.path, tc.body)
			}
			defer func() { _ = resp.Body.Close() }()
			is.Equal(resp.StatusCode, 404)
		})
	}
}

// TestUIBannerMsg pins the user-facing string each error class
// produces. Raw `context deadline exceeded` is meaningless to a
// dashboard user; uiBannerMsg translates timeout-shaped errors
// (ctx deadline, ctx canceled, breezy.ErrTimeout, net.Error.Timeout)
// into "device timeout (no response)" and ErrAuth into the
// authentication string.
func TestUIBannerMsg(t *testing.T) {
	is := is.New(t)

	const wantTimeout = "device timeout (no response)"

	is.Equal(uiBannerMsg(context.DeadlineExceeded), wantTimeout)
	is.Equal(uiBannerMsg(context.Canceled), wantTimeout)
	is.Equal(uiBannerMsg(breezy.ErrTimeout), wantTimeout)

	// Wrapped variants (typical of Go error chains) must still translate.
	is.Equal(uiBannerMsg(fmt.Errorf("dial: %w", breezy.ErrTimeout)), wantTimeout)
	is.Equal(uiBannerMsg(fmt.Errorf("ctx: %w", context.DeadlineExceeded)), wantTimeout)

	// Any net.Error.Timeout() also maps to the timeout string.
	is.Equal(uiBannerMsg(&fakeNetTimeoutErr{msg: "tcp i/o timeout"}), wantTimeout)

	// Unknown / non-timeout errors fall through to err.Error(). uiBannerMsg
	// itself does NOT have a special case for ErrAuth — the action handler
	// (handlers_ui_write.go) bypasses uiBannerMsg for the auth path and
	// emits the hardcoded "device authentication failed" string with a 401
	// status (see TestUIWritePower_AuthError for that integration). So a
	// raw uiBannerMsg(ErrAuth) returns the underlying error message.
	is.Equal(uiBannerMsg(breezy.ErrAuth), "breezy: authentication failed")
	is.Equal(uiBannerMsg(errors.New("bizarre")), "bizarre")
}

// fakeNetTimeoutErr is a minimal net.Error that returns Timeout()=true.
type fakeNetTimeoutErr struct{ msg string }

func (e *fakeNetTimeoutErr) Error() string   { return e.msg }
func (e *fakeNetTimeoutErr) Timeout() bool   { return true }
func (e *fakeNetTimeoutErr) Temporary() bool { return false }

// Compile-time assertion that fakeNetTimeoutErr satisfies net.Error.
var _ net.Error = (*fakeNetTimeoutErr)(nil)

// TestUIWriteAction_BadForm_MissingOn covers /power and /heater: both
// require {"on": bool}, both reject a missing field via errorBannerSSE.
// Pins the universal contract — 200 + Datastar-Status: 422 +
// X-Accel-Buffering: no + "missing" in the SSE banner body.
func TestUIWriteAction_BadForm_MissingOn(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, action := range []string{"power", "heater"} {
		t.Run(action, func(t *testing.T) {
			is := is.New(t)
			resp := postJSON(t, srv.URL+"/ui/devices/alpha/"+action, map[string]any{})
			defer func() { _ = resp.Body.Close() }()
			is.Equal(resp.StatusCode, 200)
			// errorBannerSSE writes X-Accel-Buffering: no inline before its
			// WriteHeader(StatusOK).
			is.Equal(resp.Header.Get("X-Accel-Buffering"), "no")
			// Datastar-Status carries the semantic HTTP code (422 for
			// validation); body returns 200 because datastar drops non-2xx
			// response bodies and would lose the SSE patch.
			is.Equal(resp.Header.Get("Datastar-Status"), "422")
			body, _ := io.ReadAll(resp.Body)
			assertSSEErrorBody(t, body, "missing")
		})
	}
}

func TestUIScheduleNewRow_XAccelBufferingHeader(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule/new-row")
	is.NoErr(err) // GET schedule/new-row
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	is.Equal(resp.Header.Get("X-Accel-Buffering"), "no")
}

func TestUIWritePower_BackendError(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	// 192.0.2.0/24 is the TEST-NET-1 range — guaranteed unreachable.
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: srvPassword,
		IP:       "192.0.2.1:4000",
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/power", map[string]any{"on": true})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)                      // SSE error banner returns 200, semantic error in body
	is.Equal(resp.Header.Get("Datastar-Status"), "502") // backend error semantic code
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "err-banner")
}

func TestUIWritePower_AuthError(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	// Keep the real device address but use the wrong password.
	addr, _ := h.Devices.Get("alpha")
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: "WRONG",
		IP:       addr.IP,
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/power", map[string]any{"on": true})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)                      // SSE error banner returns 200
	is.Equal(resp.Header.Get("Datastar-Status"), "401") // auth error semantic code
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "auth")
}

// ---------- postUIMode tests ----------

func TestUIWriteMode_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	modes := []string{"ventilation", "regeneration", "supply", "extract"}
	for _, mode := range modes {
		resp := postJSON(t, srv.URL+"/ui/devices/alpha/mode", map[string]any{"mode": mode})
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200) // each mode write must succeed
	}
	is.Equal(len(notifies.calls()), len(modes)) // one notify per successful write
}

func TestUIWriteMode_BadForm(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Invalid mode value.
	resp := postJSON(t, srv.URL+"/ui/devices/alpha/mode", map[string]any{"mode": "auto"})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "ventilation/regeneration/supply/extract")
}

// ---------- postUISpeed tests ----------

func TestUIWriteSpeed_HappyManual(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/speed", map[string]any{"manual": 50})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	notifies.assertCalledFor(t, "alpha")
}

func TestUIWriteSpeed_HappyPreset(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, preset := range []int{1, 2, 3} {
		resp := postJSON(t, srv.URL+"/ui/devices/alpha/speed", map[string]any{"preset": preset})
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200) // every preset value must round-trip
	}
}

func TestUIWriteSpeed_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	cases := []struct {
		name        string
		body        map[string]any
		wantBodySub string // optional substring assertion on the SSE banner body
	}{
		{"NeitherField", map[string]any{}, "exactly one"},
		{"BothFields", map[string]any{"manual": 50, "preset": 2}, ""},
		{"InvalidManual", map[string]any{"manual": 5}, ""}, // < 10
		{"InvalidPreset", map[string]any{"preset": 4}, ""}, // > 3
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			resp := postJSON(t, srv.URL+"/ui/devices/alpha/speed", tc.body)
			defer func() { _ = resp.Body.Close() }()
			is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
			if tc.wantBodySub != "" {
				body, _ := io.ReadAll(resp.Body)
				is.True(strings.Contains(string(body), tc.wantBodySub))
			}
		})
	}
}

// ---------- postUIHeater tests ----------

func TestUIWriteHeater_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	values := []bool{true, false}
	for _, on := range values {
		resp := postJSON(t, srv.URL+"/ui/devices/alpha/heater", map[string]any{"on": on})
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200) // each heater toggle must succeed
	}
	is.Equal(len(notifies.calls()), len(values)) // one notify per successful write
}

// ---------- postUIResetFilter tests ----------

func TestUIWriteResetFilter_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/devices/alpha/reset-filter", "", nil)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	notifies.assertCalledFor(t, "alpha")
}

// ---------- postUIResetFaults tests ----------

func TestUIWriteResetFaults_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/devices/alpha/reset-faults", "", nil)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	notifies.assertCalledFor(t, "alpha")
}

// ---------- postUITimer tests ----------

func TestUIWriteTimer_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	modes := []string{"off", "night", "turbo"}
	for _, mode := range modes {
		resp := postJSON(t, srv.URL+"/ui/devices/alpha/timer", map[string]any{"mode": mode})
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200) // every timer mode must succeed
	}
	is.Equal(len(notifies.calls()), len(modes)) // one notify per successful write
}

func TestUIWriteTimer_BadForm(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Missing 'mode' field.
	resp := postJSON(t, srv.URL+"/ui/devices/alpha/timer", map[string]any{})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "mode must be")
}

// ---------- threshold endpoint tests ----------

// putUIThreshold is a helper that issues a PUT to /ui/devices/{name}/threshold
// with form-encoded body.
func putUIThreshold(t *testing.T, base, name string, vals url.Values) *http.Response {
	t.Helper()
	body := strings.NewReader(vals.Encode())
	req, err := http.NewRequest(http.MethodPut, base+"/ui/devices/"+name+"/threshold", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUIThresholdGet_Read(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, kind := range []string{"humidity", "co2", "voc"} {
		resp, err := http.Get(srv.URL + "/ui/devices/alpha/threshold/" + kind)
		is.NoErr(err) // GET threshold for kind
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200) // each kind must render
		body, _ := io.ReadAll(resp.Body)
		is.True(strings.Contains(string(body), "event: datastar-patch-elements")) // body has SSE event
		is.True(strings.Contains(string(body), `data-threshold-cell="`+kind+`"`)) // body has threshold cell marker for kind
		is.True(strings.Contains(string(body), "/threshold/"+kind+"/edit"))       // body has edit link target
	}
}

func TestUIThresholdGet_Edit(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, kind := range []string{"humidity", "co2", "voc"} {
		resp, err := http.Get(srv.URL + "/ui/devices/alpha/threshold/" + kind + "/edit")
		is.NoErr(err) // GET edit form for kind
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200) // each kind's edit form renders
		body, _ := io.ReadAll(resp.Body)
		is.True(strings.Contains(string(body), "event: datastar-patch-elements")) // body has SSE event
		is.True(strings.Contains(string(body), `class="thresh-input"`))           // body has input element
		is.True(strings.Contains(string(body), `<button type="submit"`))          // body has submit button
	}
}

func TestUIThresholdGet_BadKind(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, path := range []string{
		"/ui/devices/alpha/threshold/bad",
		"/ui/devices/alpha/threshold/bad/edit",
	} {
		resp, err := http.Get(srv.URL + path)
		is.NoErr(err) // GET bad kind path
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 404) // unknown kind must 404
	}
}

func TestUIThresholdGet_NotFound(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, path := range []string{
		"/ui/devices/nope/threshold/humidity",
		"/ui/devices/nope/threshold/humidity/edit",
	} {
		resp, err := http.Get(srv.URL + path)
		is.NoErr(err) // GET unknown device threshold
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 404) // unknown device must 404
	}
}

func TestUIThresholdPut_HappyValue(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind":  {"humidity"},
		"value": {"65"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	body, _ := io.ReadAll(resp.Body)
	// Returns the read-variant sensor cell, not the whole card.
	is.True(strings.Contains(string(body), `class="sensor-cell"`)) // body returns sensor-cell read variant
}

func TestUIThresholdPut_HappyEnabled(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind":    {"co2"},
		"enabled": {"false"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
}

func TestUIThresholdPut_HappyBoth(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind":    {"voc"},
		"value":   {"150"},
		"enabled": {"true"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
}

// TestUIThresholdPut_BrowserCheckbox reproduces the actual browser form
// shape: the hidden+checkbox dual-input pattern submits "enabled=false"
// from the hidden field plus "enabled=true" from the checkbox when checked.
// r.FormValue returns the FIRST value, which is always "false" — so the
// handler must use r.Form["enabled"] and read the LAST value to honour
// the checkbox state.
func TestUIThresholdPut_BrowserCheckbox(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Checkbox is checked → browser submits both values.
	v := url.Values{
		"kind": {"humidity"},
	}
	v.Add("enabled", "false") // hidden input
	v.Add("enabled", "true")  // checkbox (checked)

	resp := putUIThreshold(t, srv.URL, "alpha", v)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	// Sanity: confirm the read variant we render afterwards reflects "auto fan ON"
	// by checking that the body contains the read variant of the humidity cell
	// (not an error banner). The render path goes through buildView, which would
	// pick up the WriteThrough'd enabled=true.
	body, _ := io.ReadAll(resp.Body)
	is.True(strings.Contains(string(body), `data-threshold-cell="humidity"`)) // body must render humidity read variant
}

func TestUIThresholdPut_NotFound(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "nope", url.Values{
		"kind":  {"humidity"},
		"value": {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 404)
}

func TestUIThresholdPut_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	cases := []struct {
		name        string
		vals        url.Values
		wantBodySub string // optional substring assertion on the SSE banner body
	}{
		{"MissingKind", url.Values{"value": {"60"}}, ""},
		{"InvalidKind", url.Values{"kind": {"temperature"}, "value": {"60"}}, "invalid"},
		{"NeitherValueNorEnabled", url.Values{"kind": {"humidity"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			resp := putUIThreshold(t, srv.URL, "alpha", tc.vals)
			defer func() { _ = resp.Body.Close() }()
			is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
			if tc.wantBodySub != "" {
				body, _ := io.ReadAll(resp.Body)
				is.True(strings.Contains(string(body), tc.wantBodySub))
			}
		})
	}
}

// ---------- schedule endpoint tests ----------

// newUIScheduleTestHandler extends newUIWriteTestHandler with a per-device
// Scheduler so the schedule UI endpoints have a working Scheduler.Snapshot()
// and Scheduler.Replace().
func newUIScheduleTestHandler(t *testing.T) *Handler {
	t.Helper()
	h := newUIWriteTestHandler(t)
	stateDir := t.TempDir()
	sch := &Scheduler{Device: "alpha", StateDir: stateDir}
	sch.Load()
	h.Schedulers = map[string]*Scheduler{"alpha": sch}
	return h
}

// putUISchedule issues a PUT to /ui/devices/{name}/schedule with form-encoded body.
func putUIScheduleForm(t *testing.T, base, name string, vals url.Values) *http.Response {
	t.Helper()
	body := strings.NewReader(vals.Encode())
	req, err := http.NewRequest(http.MethodPut, base+"/ui/devices/"+name+"/schedule", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUIScheduleGet_Read(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, `class="block schedule"`)) // body must contain schedule block
	is.True(strings.Contains(bs, `no entries`))             // empty scheduler renders 'no entries' text
}

func TestUIScheduleGet_Read_NotFound(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/nope/schedule")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 404)
}

func TestUIScheduleGet_Edit(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	// Pre-load an entry so we can verify it renders in edit form.
	sch := h.Schedulers["alpha"]
	at, _ := ParseScheduleTime("08:00")
	_ = sch.Replace(true, []ScheduleEntry{{At: at, Action: "regeneration", Pct: 60}})

	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule/edit")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, "event: datastar-patch-elements")) // body has SSE event
	is.True(strings.Contains(bs, "/ui/devices/alpha/schedule"))     // body has form @put target
	is.True(strings.Contains(bs, "data-on:submit__prevent="))       // body has data-on-submit attribute
	is.True(strings.Contains(bs, `name="at"`))                      // body has at input
	is.True(strings.Contains(bs, `name="action"`))                  // body has action select
}

// TestUIScheduleGet_Edit_EmptySchedule pins the auto-seed behavior: a
// device with no schedule entries should land in edit mode with one
// seeded row already rendered, so the user can fill it in directly
// rather than having to click "+ add row" before any data entry is
// possible.
func TestUIScheduleGet_Edit_EmptySchedule(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	// Do NOT pre-load any entry — exercise the empty-schedule path.

	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule/edit")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, `name="at"`))     // body has an at input — auto-seed
	is.True(strings.Contains(bs, `name="action"`)) // body has an action select — auto-seed
	is.True(strings.Contains(bs, `name="pct"`))    // body has a pct input — auto-seed
}

func TestUIScheduleGet_Edit_NotFound(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/nope/schedule/edit")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 404)
}

func TestUIScheduleGet_NewRow(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule/new-row")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, `<tr>`))          // body has <tr>
	is.True(strings.Contains(bs, `name="at"`))     // body has at input
	is.True(strings.Contains(bs, `name="action"`)) // body has action select
	is.True(strings.Contains(bs, `name="pct"`))    // body has pct input
}

func TestUIScheduleGet_NewRow_NotFound(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/nope/schedule/new-row")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 404)
}

func TestUISchedulePut_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIScheduleForm(t, srv.URL, "alpha", url.Values{
		"enabled": {"true"},
		"at":      {"08:00", "22:00"},
		"action":  {"regeneration", "off"},
		"pct":     {"60", "10"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Returns read variant — should show schedule block with rows.
	is.True(strings.Contains(bs, `class="block schedule"`)) // body has schedule block
	// Verify the scheduler was actually updated.
	snap := h.Schedulers["alpha"].Snapshot()
	is.True(snap.Enabled)          // scheduler must be enabled after PUT
	is.Equal(len(snap.Entries), 2) // both entries persisted
}

func TestUISchedulePut_Empty(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	// Pre-load entries so we can verify they get cleared.
	sch := h.Schedulers["alpha"]
	at, _ := ParseScheduleTime("08:00")
	_ = sch.Replace(true, []ScheduleEntry{{At: at, Action: "regeneration", Pct: 60}})

	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// PUT with no rows (no at/action/pct fields) → empty schedule.
	resp := putUIScheduleForm(t, srv.URL, "alpha", url.Values{
		"enabled": {"true"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	snap := h.Schedulers["alpha"].Snapshot()
	is.Equal(len(snap.Entries), 0) // PUT with no rows clears the schedule
}

func TestUISchedulePut_NotFound(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIScheduleForm(t, srv.URL, "nope", url.Values{
		"enabled": {"true"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 404)
}

func TestUISchedulePut_BadForm_InvalidTime(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIScheduleForm(t, srv.URL, "alpha", url.Values{
		"at":     {"25:00"},
		"action": {"regeneration"},
		"pct":    {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	// Body must be 200 so datastar processes the SSE patch (datastar's
	// @put discards non-2xx response bodies). Semantic 422 is preserved in
	// the Datastar-Status response header for observability. See #70.
	is.Equal(resp.StatusCode, http.StatusOK)            // 200 so datastar processes the SSE patch
	is.Equal(resp.Header.Get("Datastar-Status"), "422") // semantic status preserved in header
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, "invalid")) // body explains the validation failure
}

func TestUISchedulePut_BadForm_InvalidAction(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIScheduleForm(t, srv.URL, "alpha", url.Values{
		"at":     {"08:00"},
		"action": {"turbo"},
		"pct":    {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, http.StatusOK)
	is.Equal(resp.Header.Get("Datastar-Status"), "422")
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, "invalid action")) // body explains the action failure
}

func TestUISchedulePut_BadForm_DuplicateAt(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Duplicate at-times → Scheduler.Replace returns ErrInvalidArg.
	resp := putUIScheduleForm(t, srv.URL, "alpha", url.Values{
		"at":     {"08:00", "08:00"},
		"action": {"regeneration", "off"},
		"pct":    {"60", "10"},
	})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, http.StatusOK)
	is.Equal(resp.Header.Get("Datastar-Status"), "422")
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Edit variant rendered with error message via SSE.
	is.True(strings.Contains(bs, "data-on:submit__prevent="))   // body has edit form
	is.True(strings.Contains(bs, "/ui/devices/alpha/schedule")) // body has schedule URL
}

// ---------- postUISchedEnabled tests ----------

// TestPostUISchedEnabled_Happy verifies the enabled-toggle endpoint flips
// the scheduler's enabled bit and notifies the PushHub. Pre-loads one
// entry because the forced-off invariant pins enabled=false on a
// schedule with no entries; the test isn't about that invariant.
func TestPostUISchedEnabled_Happy(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	sch := h.Schedulers["alpha"]
	at, _ := ParseScheduleTime("08:00")
	_ = sch.Replace(false, []ScheduleEntry{{At: at, Action: "regeneration", Pct: 60}})
	snap := sch.Snapshot()
	is.Equal(snap.Enabled, false) // scheduler starts disabled

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/schedule/enabled", map[string]any{"enabled": true})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	notifies.assertCalledFor(t, "alpha")

	snap = sch.Snapshot()
	is.Equal(snap.Enabled, true) // scheduler must be enabled after POST
}

// TestPostUISchedEnabled_MissingField pins that a missing 'enabled' field
// returns a 422 SSE error banner.
func TestPostUISchedEnabled_MissingField(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/schedule/enabled", map[string]any{})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
	is.Equal(resp.Header.Get("Datastar-Status"), "422")
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "missing")
}

// TestPostUISchedEnabled_UnknownDevice pins that an unknown device name
// returns 404.
func TestPostUISchedEnabled_UnknownDevice(t *testing.T) {
	is := is.New(t)
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/nope/schedule/enabled", map[string]any{"enabled": true})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 404)
}

// TestPostUISchedEnabled_NoScheduler pins that a device without a wired
// scheduler returns a 422 SSE error banner.
func TestPostUISchedEnabled_NoScheduler(t *testing.T) {
	is := is.New(t)
	// newUIWriteTestHandler has no schedulers wired.
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/schedule/enabled", map[string]any{"enabled": true})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
	is.Equal(resp.Header.Get("Datastar-Status"), "422")
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "schedule not configured")
}

// ---------- postUIPreset tests ----------

func TestPostUIPreset_Success(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/preset", map[string]any{"preset": 2, "supply": 40, "extract": 45})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200)
	notifies.assertCalledFor(t, "alpha")
}

func TestPostUIPreset_NotFound(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/nope/preset", map[string]any{"preset": 1, "supply": 40, "extract": 45})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 404)
}

func TestPostUIPreset_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// supply / extract minimums are 10. preset must be 1..3.
	cases := []struct {
		name string
		body map[string]any
		sub  string
	}{
		{"BadPreset", map[string]any{"preset": 4, "supply": 40, "extract": 45}, "preset must be"},
		{"BadSupply", map[string]any{"preset": 1, "supply": 5, "extract": 45}, "supply percent must be"},
		{"BadExtract", map[string]any{"preset": 1, "supply": 40, "extract": 5}, "extract percent must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			resp := postJSON(t, srv.URL+"/ui/devices/alpha/preset", tc.body)
			defer func() { _ = resp.Body.Close() }()
			is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
			body, _ := io.ReadAll(resp.Body)
			assertSSEErrorBody(t, body, tc.sub)
		})
	}
}

func TestPostUIPreset_MissingFields(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Empty form: all fields missing → first validation fails (preset).
	resp := postJSON(t, srv.URL+"/ui/devices/alpha/preset", map[string]any{})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
}

func TestPostUIPreset_AuthError(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	// Keep the real device address but use the wrong password.
	addr, _ := h.Devices.Get("alpha")
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: "WRONG",
		IP:       addr.IP,
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/preset", map[string]any{"preset": 1, "supply": 40, "extract": 45})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "auth")
}

func TestPostUIPreset_BackendError(t *testing.T) {
	is := is.New(t)
	h := newUIWriteTestHandler(t)
	// 192.0.2.0/24 is TEST-NET-1 — guaranteed unreachable.
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: srvPassword,
		IP:       "192.0.2.1:4000",
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/ui/devices/alpha/preset", map[string]any{"preset": 1, "supply": 40, "extract": 45})
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, 200) // SSE error banner returns 200
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "err-banner")
}
