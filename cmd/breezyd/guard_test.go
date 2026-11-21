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
			rec, reached := doGuard(t, nil, c.method, "/v1/devices/a/params/0087", "", c.hdr)
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

func TestGuard_BodyCap(t *testing.T) {
	big := strings.Repeat("a", maxBodyBytes+1)
	rec, reached := doGuard(t, nil, "POST", "/v1/devices/a/power", big, nil)
	if rec.Code != http.StatusRequestEntityTooLarge || reached {
		t.Fatalf("oversize: status %d reached=%v", rec.Code, reached)
	}
	if !strings.Contains(rec.Body.String(), `"code":"payload_too_large"`) {
		t.Fatalf("not the envelope: %s", rec.Body)
	}
	// Unknown-length (chunked) body over the cap.
	var reached2 bool
	g := guard(guardedOK(&reached2), nil)
	req := httptest.NewRequest("PUT", "/v1/devices/a/schedule", strings.NewReader(big))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || reached2 {
		t.Fatalf("chunked oversize: status %d reached=%v", rec.Code, reached2)
	}
	// Exactly at the cap passes.
	rec, reached = doGuard(t, nil, "POST", "/v1/devices/a/power", strings.Repeat("a", maxBodyBytes), nil)
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
	}), nil)
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
	// Empty list: no Host check.
	rec, _ := doGuard(t, nil, "GET", "/v1/devices", "", map[string]string{"Host": "evil.example"})
	if rec.Code != 200 {
		t.Fatalf("empty allowlist rejected: %d", rec.Code)
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
	g := guard(h, nil)
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
