//go:build !breezyd_test_admin

// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "net/http"

// mountTestAdmin is a no-op when the breezyd_test_admin build tag is not set.
// The production binary ships without any /test/... routes.
func mountTestAdmin(mux *http.ServeMux, h *Handler) {}
