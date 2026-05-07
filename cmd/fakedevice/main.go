//go:build fakedevice_admin

// SPDX-License-Identifier: GPL-3.0-or-later

// Test-only fakedevice entry point. Spawns a fake Twinfresh device on UDP
// and an HTTP admin control plane for tests to drive its state. Excluded
// from default builds via the fakedevice_admin tag.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
)

func main() {
	snapshotPath := flag.String("snapshot", "", "path to a recorded snapshot JSON")
	deviceID := flag.String("id", "BREEZY00000000A0", "16-char device ID")
	password := flag.String("password", "1111", "protocol password (up to 8 chars)")
	flag.Parse()

	if *snapshotPath == "" {
		log.Fatalf("--snapshot is required")
	}

	srv, err := fakedevice.NewServer(*snapshotPath, *deviceID, *password)
	if err != nil {
		log.Fatalf("start fakedevice: %v", err)
	}
	defer func() { _ = srv.Close() }()

	admin, err := srv.StartAdmin()
	if err != nil {
		log.Fatalf("start admin: %v", err)
	}
	defer func() { _ = admin.Close() }()

	// Print address pair on stdout so Playwright can parse them.
	// Format: one line of "udp=HOST:PORT admin=HOST:PORT\n"
	fmt.Printf("udp=%s admin=%s\n", srv.Addr(), admin.Addr())

	// Block until SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
}
