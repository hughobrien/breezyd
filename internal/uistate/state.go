// SPDX-License-Identifier: GPL-3.0-or-later

// Package uistate carries dashboard UI state via the breezy-ui cookie.
//
// The browser owns the writes (in layout.templ's inline JS); the server
// reads the cookie on every render and emits the right <details open>
// and <div hidden> markup directly. This keeps server output authoritative
// and avoids the JS-restore flicker pattern from #49.
package uistate

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// CookieName is the name of the cookie carrying UI state.
const CookieName = "breezy-ui"

// maxCookieBytes is the largest cookie value we trust before falling back
// to defaults. Real cookies for the supported device count (~50) sit well
// under 1 KB; anything above 4 KB is either tampered or pathological.
const maxCookieBytes = 4096

// State is the parsed contents of the breezy-ui cookie.
type State struct {
	// Details maps a <details id> (e.g. "info-bedroom", "sensors-bedroom")
	// to its user-toggled open state. Absence means "use the section's
	// default plus force-open rules"; the server applies those.
	Details map[string]bool `json:"details,omitempty"`

	// Preset maps a device name to its preset-editor UI state.
	Preset map[string]PresetState `json:"preset,omitempty"`
}

// PresetState is the per-device preset-editor UI state.
type PresetState struct {
	// Open is which numbered preset's editor panel is visible.
	// 0 means closed; 1, 2, or 3 means that preset's editor is shown.
	Open int `json:"open,omitempty"`

	// Automode is the editor's automode-checkbox state. Default false.
	Automode bool `json:"automode,omitempty"`

	// Match is the editor's match-speeds-checkbox state. Default true,
	// stored explicitly so the cookie survives a flag flip in code.
	Match bool `json:"match,omitempty"`
}

// DefaultsForDevice returns the documented per-device defaults for a
// device with no cookie entry: editor closed, automode off, match-speeds on.
func DefaultsForDevice(name string) PresetState {
	return PresetState{Open: 0, Automode: false, Match: true}
}

// Parse reads the breezy-ui cookie from r and returns its parsed contents.
// On any error (missing, malformed, oversize) returns the zero State —
// callers apply defaults from there. Parse never returns an error: the
// philosophy is that bad UI-state cookies must never produce a 5xx and
// never partially apply.
func Parse(r *http.Request) State {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return State{}
	}
	if len(c.Value) > maxCookieBytes {
		return State{}
	}
	raw, err := url.QueryUnescape(c.Value)
	if err != nil {
		return State{}
	}
	var s State
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return State{}
	}
	return s
}

// Cookie returns an *http.Cookie for s, primarily for tests and any
// future server-initiated write path. The JS in layout.templ owns the
// production writes via document.cookie.
func Cookie(s State) *http.Cookie {
	b, _ := json.Marshal(s) // State is a plain struct; Marshal cannot fail.
	return &http.Cookie{
		Name:     CookieName,
		Value:    url.QueryEscape(string(b)),
		Path:     "/",
		MaxAge:   31536000, // 1 year
		SameSite: http.SameSiteLaxMode,
	}
}
