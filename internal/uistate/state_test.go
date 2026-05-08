// SPDX-License-Identifier: GPL-3.0-or-later

package uistate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse_MissingCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("missing cookie should yield zero State, got %+v", got)
	}
}

func TestParse_RoundTrip(t *testing.T) {
	want := State{
		Details: map[string]bool{
			"info-bedroom":    true,
			"sensors-bedroom": false,
		},
		Preset: map[string]PresetState{
			"bedroom": {Open: 2, Automode: false, Match: true},
		},
	}
	c := Cookie(want)

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	got := Parse(r)

	if got.Details["info-bedroom"] != true {
		t.Errorf("info-bedroom: got %v, want true", got.Details["info-bedroom"])
	}
	if _, ok := got.Details["sensors-bedroom"]; !ok {
		t.Errorf("sensors-bedroom: should be present in map even when false")
	}
	if got.Details["sensors-bedroom"] != false {
		t.Errorf("sensors-bedroom: got %v, want false (explicit)", got.Details["sensors-bedroom"])
	}
	if got.Preset["bedroom"].Open != 2 {
		t.Errorf("preset.bedroom.Open: got %d, want 2", got.Preset["bedroom"].Open)
	}
	if got.Preset["bedroom"].Match != true {
		t.Errorf("preset.bedroom.Match: got %v, want true", got.Preset["bedroom"].Match)
	}

	// Round-trip should also preserve Match=false explicitly.
	want2 := State{
		Preset: map[string]PresetState{
			"office": {Open: 1, Automode: true, Match: false},
		},
	}
	c2 := Cookie(want2)
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(c2)
	got2 := Parse(r2)
	if got2.Preset["office"].Match != false {
		t.Errorf("preset.office.Match: got %v, want false (explicit)", got2.Preset["office"].Match)
	}
	if got2.Preset["office"].Automode != true {
		t.Errorf("preset.office.Automode: got %v, want true", got2.Preset["office"].Automode)
	}
}

func TestParse_MalformedJSON(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "%7Bnot-json"})
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("malformed cookie should yield zero State, got %+v", got)
	}
}

func TestParse_BadURLEncoding(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "%ZZ"})
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("bad URL-encoding should yield zero State, got %+v", got)
	}
}

func TestParse_OversizeCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: strings.Repeat("a", maxCookieBytes+1)})
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("oversize cookie should yield zero State, got %+v", got)
	}
}

func TestDefaultsForDevice(t *testing.T) {
	d := DefaultsForDevice("anything")
	if d.Open != 0 || d.Automode != false || d.Match != true {
		t.Errorf("defaults: got %+v, want {Open:0 Automode:false Match:true}", d)
	}
}
