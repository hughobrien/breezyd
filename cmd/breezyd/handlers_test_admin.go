//go:build breezyd_test_admin

// SPDX-License-Identifier: GPL-3.0-or-later

// handlers_test_admin.go mounts a /test/devices/{name}/... HTTP surface that
// mutates the per-device *breezy.MemClient in place. This file is only compiled
// when the breezyd_test_admin build tag is set; the production binary never
// contains these routes.
//
// Endpoints:
//
//	POST /test/devices/{name}/params/{id}   — overwrite one param value (hex body)
//	POST /test/devices/{name}/inject-error  — arm auth/timeout fault injection
//	POST /test/devices/{name}/reset         — restore seed params and clear faults
//
// All endpoints return 400 when the backend is UDP (type assertion to
// *breezy.MemClient fails) and 204 on success.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// mountTestAdmin registers the /test/... routes on mux. Called once from
// Handler.mux() so the routes share the same ServeMux as the production API.
func mountTestAdmin(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /test/devices/{name}/params/{id}", h.testSetParam)
	mux.HandleFunc("POST /test/devices/{name}/inject-error", h.testInjectError)
	mux.HandleFunc("POST /test/devices/{name}/reset", h.testReset)
}

// memClientFor resolves the MemClient for name.
// It dials via the configured ClientFactory (same path production uses),
// immediately releases the per-device lock (MemClient is concurrency-safe
// internally so no serialisation is needed), and type-asserts to *MemClient.
// Returns (nil, false) and writes a 400/500 on any error.
func (h *Handler) memClientFor(w http.ResponseWriter, name string) (*breezy.MemClient, bool) {
	cli, unlock, err := h.dial(name)
	if err != nil {
		http.Error(w, fmt.Sprintf("dial: %v", err), http.StatusInternalServerError)
		return nil, false
	}
	// Release the UDP-serialisation lock immediately — MemClient is safe for
	// concurrent callers and doesn't hold a real connection. The unlock func
	// is a no-op if no Poller is registered for this device.
	unlock()
	// MemClient does not need Close; the call is a no-op but call it for
	// correctness to satisfy the HandlerClient interface contract.
	_ = cli.Close()

	mc, ok := cli.(*breezy.MemClient)
	if !ok {
		http.Error(w, "test admin requires --backend=memory; UDP backend is in use", http.StatusBadRequest)
		return nil, false
	}
	return mc, true
}

// testSetParam handles POST /test/devices/{name}/params/{id}.
// Body: {"value":"FF42"} (hex string, any length).
// Effect: mc.SetParamValue(id, decodedBytes).
func (h *Handler) testSetParam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	idStr := r.PathValue("id")

	id, err := parseParamID(idStr)
	if err != nil {
		http.Error(w, "bad param id: "+err.Error(), http.StatusBadRequest)
		return
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "parse JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	val, err := hex.DecodeString(body.Value)
	if err != nil {
		http.Error(w, "value must be hex: "+err.Error(), http.StatusBadRequest)
		return
	}

	mc, ok := h.memClientFor(w, name)
	if !ok {
		return
	}
	mc.SetParamValue(id, val)
	w.WriteHeader(http.StatusNoContent)
}

// testInjectError handles POST /test/devices/{name}/inject-error.
// Body: {"kind":"auth"} | {"kind":"timeout"} | {"kind":"none"}.
// "auth" arms ErrAuth on next read/write; "timeout" arms ErrTimeout;
// "none" clears both. The modes are mutually exclusive: "auth" clears
// timeout first, and vice-versa.
func (h *Handler) testInjectError(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "parse JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	mc, ok := h.memClientFor(w, name)
	if !ok {
		return
	}

	switch body.Kind {
	case "auth":
		mc.SetTimeoutMode(false)
		mc.SetAuthFailureMode(true)
	case "timeout":
		mc.SetAuthFailureMode(false)
		mc.SetTimeoutMode(true)
	case "none":
		mc.SetAuthFailureMode(false)
		mc.SetTimeoutMode(false)
	default:
		http.Error(w, fmt.Sprintf("unknown kind %q (allowed: auth, timeout, none)", body.Kind), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testReset handles POST /test/devices/{name}/reset.
// Clears all injected fault state and restores the seed params.
func (h *Handler) testReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	mc, ok := h.memClientFor(w, name)
	if !ok {
		return
	}
	mc.Reset()
	w.WriteHeader(http.StatusNoContent)
}
