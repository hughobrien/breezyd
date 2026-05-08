// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

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

	h := newUITestHandler(t, "alpha")
	// Replace the device config with one pointing at the real fakedevice.
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: srvPassword,
		IP:       addr,
	})
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
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	notifies.assertCalledFor(t, "alpha")
}

func TestUIWritePower_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestUIWritePower_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Missing 'on' field — form value is absent, so onStr == "".
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "missing or invalid")
}

func TestUIWritePower_BackendError(t *testing.T) {
	h := newUIWriteTestHandler(t)
	// 192.0.2.0/24 is the TEST-NET-1 range — guaranteed unreachable.
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: srvPassword,
		IP:       "192.0.2.1:4000",
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 502 {
		t.Fatalf("status: %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "err-banner")
}

func TestUIWritePower_AuthError(t *testing.T) {
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

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "auth")
}

// ---------- postUIMode tests ----------

func TestUIWriteMode_Happy(t *testing.T) {
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	modes := []string{"ventilation", "regeneration", "supply", "extract"}
	for _, mode := range modes {
		resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/mode", url.Values{"mode": {mode}})
		if err != nil {
			t.Fatalf("mode=%s: %v", mode, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("mode=%s: status=%d, want 200", mode, resp.StatusCode)
		}
	}
	if got := len(notifies.calls()); got != len(modes) {
		t.Errorf("PushHub.Notify count: got %d, want %d", got, len(modes))
	}
}

func TestUIWriteMode_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/mode", url.Values{"mode": {"regeneration"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIWriteMode_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Invalid mode value.
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/mode", url.Values{"mode": {"auto"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "ventilation/regeneration/supply/extract")
}

// ---------- postUISpeed tests ----------

func TestUIWriteSpeed_HappyManual(t *testing.T) {
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"manual": {"50"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	notifies.assertCalledFor(t, "alpha")
}

func TestUIWriteSpeed_HappyPreset(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, preset := range []string{"1", "2", "3"} {
		resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"preset": {preset}})
		if err != nil {
			t.Fatalf("preset=%s: %v", preset, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("preset=%s: status=%d, want 200", preset, resp.StatusCode)
		}
	}
}

func TestUIWriteSpeed_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/speed", url.Values{"manual": {"50"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIWriteSpeed_BadForm_NeitherField(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "exactly one") {
		t.Errorf("body missing error message: %s", string(body))
	}
}

func TestUIWriteSpeed_BadForm_BothFields(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"manual": {"50"}, "preset": {"2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}

func TestUIWriteSpeed_BadForm_InvalidManual(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Out of range (5 < 10).
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"manual": {"5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}

func TestUIWriteSpeed_BadForm_InvalidPreset(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Out of range (4 > 3).
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"preset": {"4"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}

// ---------- postUIHeater tests ----------

func TestUIWriteHeater_Happy(t *testing.T) {
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	values := []string{"true", "false"}
	for _, on := range values {
		resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/heater", url.Values{"on": {on}})
		if err != nil {
			t.Fatalf("on=%s: %v", on, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("on=%s: status=%d, want 200", on, resp.StatusCode)
		}
	}
	if got := len(notifies.calls()); got != len(values) {
		t.Errorf("PushHub.Notify count: got %d, want %d", got, len(values))
	}
}

func TestUIWriteHeater_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/heater", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIWriteHeater_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Missing 'on' field.
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/heater", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "missing or invalid")
}

// ---------- postUIResetFilter tests ----------

func TestUIWriteResetFilter_Happy(t *testing.T) {
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/devices/alpha/reset-filter", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	notifies.assertCalledFor(t, "alpha")
}

func TestUIWriteResetFilter_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/devices/nope/reset-filter", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

// ---------- postUIResetFaults tests ----------

func TestUIWriteResetFaults_Happy(t *testing.T) {
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/devices/alpha/reset-faults", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	notifies.assertCalledFor(t, "alpha")
}

func TestUIWriteResetFaults_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/ui/devices/nope/reset-faults", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

// ---------- postUITimer tests ----------

func TestUIWriteTimer_Happy(t *testing.T) {
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	modes := []string{"off", "night", "turbo"}
	for _, mode := range modes {
		resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/timer", url.Values{"mode": {mode}})
		if err != nil {
			t.Fatalf("mode=%s: %v", mode, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("mode=%s: status=%d, want 200", mode, resp.StatusCode)
		}
	}
	if got := len(notifies.calls()); got != len(modes) {
		t.Errorf("PushHub.Notify count: got %d, want %d", got, len(modes))
	}
}

func TestUIWriteTimer_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/timer", url.Values{"mode": {"night"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIWriteTimer_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Missing 'mode' field.
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/timer", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
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
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, kind := range []string{"humidity", "co2", "voc"} {
		resp, err := http.Get(srv.URL + "/ui/devices/alpha/threshold/" + kind)
		if err != nil {
			t.Fatalf("kind=%s: %v", kind, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("kind=%s: status=%d, want 200", kind, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `class="sensor-cell"`) {
			t.Errorf("kind=%s: body missing sensor-cell: %s", kind, string(body))
		}
		if !strings.Contains(string(body), `data-action="edit-threshold"`) {
			t.Errorf("kind=%s: body missing edit-threshold action: %s", kind, string(body))
		}
	}
}

func TestUIThresholdGet_Edit(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, kind := range []string{"humidity", "co2", "voc"} {
		resp, err := http.Get(srv.URL + "/ui/devices/alpha/threshold/" + kind + "/edit")
		if err != nil {
			t.Fatalf("kind=%s: %v", kind, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("kind=%s: status=%d, want 200", kind, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `class="thresh-input"`) {
			t.Errorf("kind=%s: body missing thresh-input: %s", kind, string(body))
		}
		if !strings.Contains(string(body), `data-action="threshold-save"`) {
			t.Errorf("kind=%s: body missing threshold-save button: %s", kind, string(body))
		}
	}
}

func TestUIThresholdGet_BadKind(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, path := range []string{
		"/ui/devices/alpha/threshold/bad",
		"/ui/devices/alpha/threshold/bad/edit",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 404 {
			t.Fatalf("%s: status=%d, want 404", path, resp.StatusCode)
		}
	}
}

func TestUIThresholdGet_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, path := range []string{
		"/ui/devices/nope/threshold/humidity",
		"/ui/devices/nope/threshold/humidity/edit",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 404 {
			t.Fatalf("%s: status=%d, want 404", path, resp.StatusCode)
		}
	}
}

func TestUIThresholdPut_HappyValue(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind":  {"humidity"},
		"value": {"65"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// Returns the read-variant sensor cell, not the whole card.
	if !strings.Contains(string(body), `class="sensor-cell"`) {
		t.Errorf("body missing sensor-cell: %s", string(body))
	}
}

func TestUIThresholdPut_HappyEnabled(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind":    {"co2"},
		"enabled": {"false"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
}

func TestUIThresholdPut_HappyBoth(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind":    {"voc"},
		"value":   {"150"},
		"enabled": {"true"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
}

// TestUIThresholdPut_BrowserCheckbox reproduces the actual browser form
// shape: the hidden+checkbox dual-input pattern submits "enabled=false"
// from the hidden field plus "enabled=true" from the checkbox when checked.
// r.FormValue returns the FIRST value, which is always "false" — so the
// handler must use r.Form["enabled"] and read the LAST value to honour
// the checkbox state.
func TestUIThresholdPut_BrowserCheckbox(t *testing.T) {
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
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	// Sanity: confirm the read variant we render afterwards reflects "auto fan ON"
	// by checking that the body contains the read variant of the humidity cell
	// (not an error banner). The render path goes through buildView, which would
	// pick up the WriteThrough'd enabled=true.
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-kind="humidity"`) {
		t.Errorf("body missing humidity threshold render; got: %s", body)
	}
}

func TestUIThresholdPut_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "nope", url.Values{
		"kind":  {"humidity"},
		"value": {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIThresholdPut_BadForm_MissingKind(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"value": {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}

func TestUIThresholdPut_BadForm_InvalidKind(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind":  {"temperature"},
		"value": {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid") {
		t.Errorf("body missing error message: %s", string(body))
	}
}

func TestUIThresholdPut_BadForm_NeitherValueNorEnabled(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIThreshold(t, srv.URL, "alpha", url.Values{
		"kind": {"humidity"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
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
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, `class="block schedule"`) {
		t.Errorf("body missing schedule block: %s", bs)
	}
	if !strings.Contains(bs, `no entries`) {
		t.Errorf("body missing 'no entries' text: %s", bs)
	}
}

func TestUIScheduleGet_Read_NotFound(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/nope/schedule")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIScheduleGet_Edit(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	// Pre-load an entry so we can verify it renders in edit form.
	sch := h.Schedulers["alpha"]
	at, _ := ParseScheduleTime("08:00")
	_ = sch.Replace(true, []ScheduleEntry{{At: at, Action: "regeneration", Pct: 60}})

	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule/edit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, `hx-put="/ui/devices/alpha/schedule"`) {
		t.Errorf("body missing form hx-put: %s", bs)
	}
	if !strings.Contains(bs, `name="at"`) {
		t.Errorf("body missing at input: %s", bs)
	}
	if !strings.Contains(bs, `name="action"`) {
		t.Errorf("body missing action select: %s", bs)
	}
}

func TestUIScheduleGet_Edit_NotFound(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/nope/schedule/edit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIScheduleGet_NewRow(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/schedule/new-row")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, `<tr>`) {
		t.Errorf("body missing <tr>: %s", bs)
	}
	if !strings.Contains(bs, `name="at"`) {
		t.Errorf("body missing at input: %s", bs)
	}
	if !strings.Contains(bs, `name="action"`) {
		t.Errorf("body missing action select: %s", bs)
	}
	if !strings.Contains(bs, `name="pct"`) {
		t.Errorf("body missing pct input: %s", bs)
	}
}

func TestUIScheduleGet_NewRow_NotFound(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/nope/schedule/new-row")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUISchedulePut_Happy(t *testing.T) {
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
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Returns read variant — should show schedule block with rows.
	if !strings.Contains(bs, `class="block schedule"`) {
		t.Errorf("body missing schedule block: %s", bs)
	}
	// Verify the scheduler was actually updated.
	snap := h.Schedulers["alpha"].Snapshot()
	if !snap.Enabled {
		t.Errorf("scheduler not enabled after PUT")
	}
	if len(snap.Entries) != 2 {
		t.Errorf("entry count: got %d, want 2", len(snap.Entries))
	}
}

func TestUISchedulePut_Empty(t *testing.T) {
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
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, body: %s", resp.StatusCode, body)
	}
	snap := h.Schedulers["alpha"].Snapshot()
	if len(snap.Entries) != 0 {
		t.Errorf("entry count after empty PUT: got %d, want 0", len(snap.Entries))
	}
}

func TestUISchedulePut_NotFound(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIScheduleForm(t, srv.URL, "nope", url.Values{
		"enabled": {"true"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUISchedulePut_BadForm_InvalidTime(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIScheduleForm(t, srv.URL, "alpha", url.Values{
		"at":     {"25:00"},
		"action": {"regeneration"},
		"pct":    {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	// Returns edit variant with error message at 422 status.
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, want 422, body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, "invalid") {
		t.Errorf("body missing error message: %s", bs)
	}
}

func TestUISchedulePut_BadForm_InvalidAction(t *testing.T) {
	h := newUIScheduleTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp := putUIScheduleForm(t, srv.URL, "alpha", url.Values{
		"at":     {"08:00"},
		"action": {"turbo"},
		"pct":    {"60"},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, want 422, body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, "invalid action") {
		t.Errorf("body missing invalid action error: %s", bs)
	}
}

func TestUISchedulePut_BadForm_DuplicateAt(t *testing.T) {
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
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, want 422, body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Edit variant rendered with error message.
	if !strings.Contains(bs, `hx-put="/ui/devices/alpha/schedule"`) {
		t.Errorf("body missing edit form: %s", bs)
	}
}

// ---------- postUIPreset tests ----------

func TestPostUIPreset_Success(t *testing.T) {
	h := newUIWriteTestHandler(t)
	notifies := attachFakePushHub(h)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/preset", url.Values{
		"preset":  {"2"},
		"supply":  {"40"},
		"extract": {"45"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	notifies.assertCalledFor(t, "alpha")
}

func TestPostUIPreset_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/preset", url.Values{
		"preset":  {"1"},
		"supply":  {"40"},
		"extract": {"45"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestPostUIPreset_BadPreset(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/preset", url.Values{
		"preset":  {"4"},
		"supply":  {"40"},
		"extract": {"45"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "preset must be")
}

func TestPostUIPreset_BadSupply(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// supply=5 is below the minimum of 10.
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/preset", url.Values{
		"preset":  {"1"},
		"supply":  {"5"},
		"extract": {"45"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "supply must be")
}

func TestPostUIPreset_BadExtract(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// extract=5 is below the minimum of 10.
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/preset", url.Values{
		"preset":  {"1"},
		"supply":  {"40"},
		"extract": {"5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "extract must be")
}

func TestPostUIPreset_MissingFields(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Empty form: all fields missing → first validation fails (preset).
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/preset", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}

func TestPostUIPreset_AuthError(t *testing.T) {
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

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/preset", url.Values{
		"preset":  {"1"},
		"supply":  {"40"},
		"extract": {"45"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "auth")
}

func TestPostUIPreset_BackendError(t *testing.T) {
	h := newUIWriteTestHandler(t)
	// 192.0.2.0/24 is TEST-NET-1 — guaranteed unreachable.
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: srvPassword,
		IP:       "192.0.2.1:4000",
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/preset", url.Values{
		"preset":  {"1"},
		"supply":  {"40"},
		"extract": {"45"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 502 {
		t.Fatalf("status: %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	assertSSEErrorBody(t, body, "err-banner")
}
