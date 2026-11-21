// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// guardedOK is a downstream handler that records it was reached.
func guardedOK(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		buf := make([]byte, 1<<20)
		for {
			if _, err := r.Body.Read(buf); err != nil {
				break
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

func doGuard(t *testing.T, allowed []string, method, target, body string, hdr map[string]string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	var reached bool
	g := guard(guardedOK(&reached), allowed)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:9876"
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec, reached
}

func TestGuard_CrossOrigin(t *testing.T) {
	cases := []struct {
		name   string
		method string
		hdr    map[string]string
		want   int
	}{
		{"no headers (CLI/curl)", "POST", nil, 200},
		{"sec-fetch same-origin", "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"sec-fetch none", "POST", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		{"sec-fetch cross-site", "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"sec-fetch same-site", "POST", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"origin matches host", "POST", map[string]string{"Origin": "http://breezy.lan:9876", "Host": "breezy.lan:9876"}, 200},
		{"origin differs", "POST", map[string]string{"Origin": "http://evil.example", "Host": "breezy.lan:9876"}, 403},
		{"origin matches x-forwarded-host", "POST", map[string]string{"Origin": "https://breezy.example.com", "Host": "127.0.0.1:9876", "X-Forwarded-Host": "breezy.example.com"}, 200},
		{"origin differs from x-forwarded-host", "PUT", map[string]string{"Origin": "https://evil.example", "Host": "127.0.0.1:9876", "X-Forwarded-Host": "breezy.example.com"}, 403},
		{"cross-site PUT", "PUT", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site DELETE", "DELETE", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site GET passes", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"cross-origin GET passes", "GET", map[string]string{"Origin": "http://evil.example", "Host": "x:1"}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, reached := doGuard(t, []string{"*"}, c.method, "/v1/devices/a/params/0087", "", c.hdr)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body)
			}
			if c.want == 403 {
				if reached {
					t.Fatal("handler reached despite rejection")
				}
				if !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
					t.Fatalf("not the JSON envelope: %s", rec.Body)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Fatalf("Content-Type = %q", ct)
				}
			}
		})
	}
}

func TestGuard_ForwardedHostMultiValue(t *testing.T) {
	post := func(xfh ...string) int {
		var reached bool
		g := guard(guardedOK(&reached), []string{"*"})
		req := httptest.NewRequest("POST", "/v1/devices/a/power", strings.NewReader("{}"))
		req.Host = "127.0.0.1:9876"
		req.Header.Set("Origin", "http://breezy.home.lan")
		for _, v := range xfh {
			req.Header.Add("X-Forwarded-Host", v)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post("breezy.home.lan"); c != 200 {
		t.Errorf("matching XFH: %d", c)
	}
	if c := post("other.example", "breezy.home.lan"); c != 200 {
		t.Errorf("second header value: %d", c)
	}
	if c := post("other.example, breezy.home.lan"); c != 200 {
		t.Errorf("comma-joined value: %d", c)
	}
	if c := post(); c != 403 {
		t.Errorf("no XFH: %d, want 403", c)
	}
	if c := post("other.example"); c != 403 {
		t.Errorf("non-matching XFH: %d, want 403", c)
	}
}

func TestGuard_BodyCap(t *testing.T) {
	big := strings.Repeat("a", maxBodyBytes+1)
	rec, reached := doGuard(t, []string{"*"}, "POST", "/v1/devices/a/power", big, nil)
	if rec.Code != http.StatusRequestEntityTooLarge || reached {
		t.Fatalf("oversize: status %d reached=%v", rec.Code, reached)
	}
	if !strings.Contains(rec.Body.String(), `"code":"payload_too_large"`) {
		t.Fatalf("not the envelope: %s", rec.Body)
	}
	// Unknown-length (chunked) body over the cap.
	var reached2 bool
	g := guard(guardedOK(&reached2), []string{"*"})
	req := httptest.NewRequest("PUT", "/v1/devices/a/schedule", strings.NewReader(big))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || reached2 {
		t.Fatalf("chunked oversize: status %d reached=%v", rec.Code, reached2)
	}
	// Exactly at the cap passes.
	rec, reached = doGuard(t, []string{"*"}, "POST", "/v1/devices/a/power", strings.Repeat("a", maxBodyBytes), nil)
	if rec.Code != 200 || !reached {
		t.Fatalf("at-cap: status %d reached=%v", rec.Code, reached)
	}
}

func TestGuard_BodyPreserved(t *testing.T) {
	var got string
	g := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		got = string(b[:n])
	}), []string{"*"})
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"on":true}`))
	g.ServeHTTP(httptest.NewRecorder(), req)
	if got != `{"on":true}` {
		t.Fatalf("body = %q", got)
	}
}

func TestGuard_HostAllowlist(t *testing.T) {
	allowed := []string{"breezy.example.com"}
	cases := []struct {
		host string
		want int
	}{
		{"breezy.example.com", 200},
		{"breezy.example.com:9876", 200},
		{"BREEZY.example.com", 200},
		{"127.0.0.1:9876", 200},
		{"[::1]:9876", 200},
		{"192.168.1.5", 200},
		{"localhost:9876", 200},
		{"evil.example", 403},
		{"evil.example:9876", 403},
		{"breezy.example.com.evil.example", 403},
	}
	for _, c := range cases {
		for _, method := range []string{"GET", "POST"} {
			rec, _ := doGuard(t, allowed, method, "/v1/devices", "", map[string]string{"Host": c.host})
			if rec.Code != c.want {
				t.Errorf("%s Host %q: status %d, want %d", method, c.host, rec.Code, c.want)
			}
		}
	}
	// Empty list: still checked (default-on); IP literals pass.
	rec, _ := doGuard(t, nil, "GET", "/v1/devices", "", map[string]string{"Host": "evil.example"})
	if rec.Code != 403 {
		t.Fatalf("rebinding name accepted with empty allowlist: %d", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "evil.example") || !strings.Contains(b, "allowed_hosts") {
		t.Fatalf("message lacks host/hint: %s", b)
	}
	rec, _ = doGuard(t, nil, "GET", "/v1/devices", "", map[string]string{"Host": "127.0.0.1:9876"})
	if rec.Code != 200 {
		t.Fatalf("IP literal rejected: %d", rec.Code)
	}
	// Trailing dot tolerated.
	rec, _ = doGuard(t, allowed, "GET", "/v1/devices", "", map[string]string{"Host": "breezy.example.com.:9876"})
	if rec.Code != 200 {
		t.Fatalf("trailing dot rejected: %d", rec.Code)
	}
}

func TestGuard_Wildcard(t *testing.T) {
	rec, _ := doGuard(t, []string{"*"}, "GET", "/v1/devices", "", map[string]string{"Host": "evil.example"})
	if rec.Code != 200 {
		t.Fatalf("wildcard rejected: %d", rec.Code)
	}
}

// X-Forwarded-Host must never widen the allowlist, from loopback or not:
// a rebinding page's JS can set it itself.
func TestGuard_ForwardedHostNotTrusted(t *testing.T) {
	for _, remote := range []string{"127.0.0.1:5555", "192.168.1.9:5555"} {
		var reached bool
		g := guard(guardedOK(&reached), nil)
		req := httptest.NewRequest("GET", "/v1/devices", nil)
		req.Host = "evil.example"
		req.RemoteAddr = remote
		req.Header.Set("X-Forwarded-Host", "evil.example")
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != 403 || reached {
			t.Errorf("remote %s: status %d reached=%v, want 403", remote, rec.Code, reached)
		}
	}
}

func TestImplicitHosts(t *testing.T) {
	cases := []struct {
		name     string
		cfg      []string
		listen   string
		hostname string
		allow    []string
		deny     []string
	}{
		{"fqdn hostname", nil, "127.0.0.1:9876", "BigThink.Home.Lan",
			[]string{"bigthink.home.lan", "bigthink", "BIGTHINK.local:9876", "bigthink.home.lan."}, []string{"evil.example", "home.lan"}},
		{"short hostname", nil, "127.0.0.1:9876", "bigthink",
			[]string{"bigthink", "bigthink.local"}, []string{"evil.example"}},
		{"listen name", nil, "breezy.lan:9876", "box",
			[]string{"breezy.lan", "box"}, []string{"evil.example"}},
		{"listen ip", nil, "0.0.0.0:9876", "box",
			[]string{"box", "192.168.1.5"}, []string{"evil.example"}},
		{"configured", []string{"Breezy.Example.com"}, "127.0.0.1:9876", "box",
			[]string{"breezy.example.com:443"}, []string{"evil.example"}},
		{"empty hostname", nil, "127.0.0.1:9876", "",
			[]string{"localhost", "127.0.0.1", "[::1]:9876"}, []string{"evil.example", ".local", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allowed := map[string]bool{}
			for _, h := range implicitHosts(c.cfg, c.listen, c.hostname) {
				allowed[normHost(h)] = true
			}
			for _, h := range c.allow {
				if !hostAllowed(h, allowed) {
					t.Errorf("%q rejected", h)
				}
			}
			for _, h := range c.deny {
				if hostAllowed(h, allowed) {
					t.Errorf("%q accepted", h)
				}
			}
		})
	}
}

// TestGuard_RealMux checks the guard in front of the production Handler:
// a cross-site raw param write never reaches the device client.
func TestGuard_RealMux(t *testing.T) {
	called := false
	h := newTestHandler(t, map[string]DeviceConfig{"a": {}},
		withClientFactory(func(string) (HandlerClient, error) {
			called = true
			return nil, http.ErrNotSupported
		}))
	g := guard(h, []string{"*"})
	req := httptest.NewRequest("POST", "/v1/devices/a/params/0087", strings.NewReader("01"))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("status %d, factory called %v", rec.Code, called)
	}
	req = httptest.NewRequest("GET", "/healthz", nil)
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status %d", rec.Code)
	}
}
