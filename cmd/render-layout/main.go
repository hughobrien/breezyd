// SPDX-License-Identifier: GPL-3.0-or-later

// render-layout is a test helper that renders the templ Layout template to
// stdout. It is called by the Playwright test suite via execSync to obtain
// the Layout HTML shell without needing a running daemon.
//
// Usage: render-layout <styleHash> [htmxVersion]
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: render-layout <styleHash> [htmxVersion]")
		os.Exit(2)
	}
	styleHash := os.Args[1]
	htmxVersion := "2.0.4"
	if len(os.Args) >= 3 {
		htmxVersion = os.Args[2]
	}
	d := templates.LayoutData{StyleHash: styleHash, HTMXVersion: htmxVersion}
	if err := templates.Layout(d).Render(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "render:", err)
		os.Exit(1)
	}
}
