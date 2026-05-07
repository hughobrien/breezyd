//go:build fakedevice_admin

// SPDX-License-Identifier: GPL-3.0-or-later

// Test-only HTTP control plane for the fakedevice. Lets tests drive
// device state, simulate failure modes, and reset between cases.
//
// Excluded from default builds and from release binaries — gated behind
// the fakedevice_admin build tag.
package fakedevice

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// AdminServer is the HTTP control plane for a fakedevice Server.
type AdminServer struct {
	srv    *http.Server
	server *Server
	addr   string
}

// StartAdmin binds an HTTP control plane on a free 127.0.0.1 port and
// returns the AdminServer. Caller should defer AdminServer.Close().
func (s *Server) StartAdmin() (*AdminServer, error) {
	a := &AdminServer{server: s}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /state", a.putState)
	mux.HandleFunc("POST /simulate/auth-failure", a.simulateAuthFailure)
	mux.HandleFunc("POST /simulate/udp-timeout", a.simulateUDPTimeout)
	mux.HandleFunc("POST /simulate/fan-settle", a.simulateFanSettle)
	mux.HandleFunc("POST /reset", a.reset)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("admin listen: %w", err)
	}
	a.addr = ln.Addr().String()
	a.srv = &http.Server{Handler: mux}
	go func() { _ = a.srv.Serve(ln) }()
	return a, nil
}

// Addr returns the bound address as "host:port".
func (a *AdminServer) Addr() string { return a.addr }

// Close shuts down the admin HTTP server.
func (a *AdminServer) Close() error { return a.srv.Close() }

// PUT /state
// Body: JSON {"params": {"0001": "01", "0044": "32", ...}}
// Keys are 4-char hex param IDs; values are hex-encoded bytes.
func (a *AdminServer) putState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Params map[string]string `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for idHex, valueHex := range body.Params {
		idU, err := strconv.ParseUint(idHex, 16, 16)
		if err != nil {
			http.Error(w, "bad param id "+idHex+": "+err.Error(), http.StatusBadRequest)
			return
		}
		b, err := hex.DecodeString(valueHex)
		if err != nil {
			http.Error(w, "bad value hex for "+idHex+": "+err.Error(), http.StatusBadRequest)
			return
		}
		a.server.SetParamValue(breezy.ParamID(idU), b)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /simulate/auth-failure?on=true|false  (default: true)
func (a *AdminServer) simulateAuthFailure(w http.ResponseWriter, r *http.Request) {
	on := r.URL.Query().Get("on") != "false"
	a.server.SetAuthFailureMode(on)
	w.WriteHeader(http.StatusNoContent)
}

// POST /simulate/udp-timeout?on=true|false  (default: true)
func (a *AdminServer) simulateUDPTimeout(w http.ResponseWriter, r *http.Request) {
	on := r.URL.Query().Get("on") != "false"
	a.server.SetSilentMode(on)
	w.WriteHeader(http.StatusNoContent)
}

// POST /simulate/fan-settle?ms=N  — sets reply delay to N milliseconds.
// Pass ms=0 to clear.
func (a *AdminServer) simulateFanSettle(w http.ResponseWriter, r *http.Request) {
	msStr := r.URL.Query().Get("ms")
	ms, err := strconv.Atoi(msStr)
	if err != nil {
		http.Error(w, "bad ms parameter: "+err.Error(), http.StatusBadRequest)
		return
	}
	a.server.SetReplyDelay(time.Duration(ms) * time.Millisecond)
	w.WriteHeader(http.StatusNoContent)
}

// POST /reset — clears all simulation flags and reloads the snapshot.
func (a *AdminServer) reset(w http.ResponseWriter, r *http.Request) {
	if err := a.server.Reset(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
