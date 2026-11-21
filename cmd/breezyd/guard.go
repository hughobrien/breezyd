// SPDX-License-Identifier: GPL-3.0-or-later

// Request guard: one middleware layer around the whole mux that closes
// the "web page in a browser drives the daemon" hole (issue #259).
//
//  1. Host allowlist (opt-in, [daemon].allowed_hosts) against DNS rebinding.
//  2. Cross-origin rejection for every state-changing method, via
//     net/http.CrossOriginProtection (Sec-Fetch-Site, else Origin vs Host).
//  3. A request body size cap, answered with 413.
//
// There is no authentication here; requests carrying none of the browser
// headers (the breezy CLI, curl, Prometheus) pass untouched.
package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxBodyBytes caps request bodies. The largest legitimate body is a
// schedule PUT (a handful of entries, well under 4 KiB).
const maxBodyBytes = 64 << 10

// guard wraps next with the Host allowlist, cross-origin protection and
// body cap. An empty allowedHosts disables the Host check.
func guard(next http.Handler, allowedHosts []string) http.Handler {
	allowed := make(map[string]bool, len(allowedHosts))
	for _, h := range allowedHosts {
		allowed[normHost(h)] = true
	}

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, errEnvelope{
			Error: "cross-origin request rejected", Code: "forbidden"})
	}))
	body := bodyCap(next)
	checked := cop.Handler(body)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowed) > 0 && !hostAllowed(r.Host, allowed) {
			writeJSON(w, http.StatusForbidden, errEnvelope{
				Error: "host not allowed", Code: "forbidden"})
			return
		}
		// Behind the NixOS nginx proxy r.Host may be the upstream address
		// while the browser's Origin is the public vhost; accept an Origin
		// that matches X-Forwarded-Host when the browser sent no
		// Sec-Fetch-Site verdict of its own.
		// The header may appear more than once (module's $http_host plus
		// recommendedProxySettings' $host) and each may be comma-joined,
		// so any value matching is enough.
		if r.Header.Get("Sec-Fetch-Site") == "" {
			if u, err := url.Parse(r.Header.Get("Origin")); err == nil && u.Host != "" {
				for _, v := range r.Header.Values("X-Forwarded-Host") {
					for _, xfh := range strings.Split(v, ",") {
						if strings.EqualFold(strings.TrimSpace(xfh), u.Host) {
							body.ServeHTTP(w, r)
							return
						}
					}
				}
			}
		}
		checked.ServeHTTP(w, r)
	})
}

// bodyCap buffers state-changing request bodies up to maxBodyBytes and
// answers 413 for anything larger, then hands the handler a replayable
// body. Reading ahead (rather than http.MaxBytesReader alone) lets every
// handler stay unaware of the limit yet still yield a uniform 413.
func bodyCap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		tooBig := func() {
			writeJSON(w, http.StatusRequestEntityTooLarge, errEnvelope{
				Error: "request body too large", Code: "payload_too_large"})
		}
		if r.ContentLength > maxBodyBytes {
			tooBig()
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			buf, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
			if err != nil {
				if _, ok := err.(*http.MaxBytesError); ok {
					tooBig()
					return
				}
				writeJSON(w, http.StatusBadRequest, errEnvelope{
					Error: "read body: " + err.Error(), Code: "bad_request"})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(buf))
		}
		next.ServeHTTP(w, r)
	})
}

// normHost lowercases and strips any port and trailing dot.
func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	return h
}

// hostAllowed reports whether a request Host is on the allowlist, an IP
// literal, or localhost. IP literals are safe: rebinding needs a name.
func hostAllowed(hostHeader string, allowed map[string]bool) bool {
	h := normHost(hostHeader)
	return h == "localhost" || net.ParseIP(h) != nil || allowed[h]
}
