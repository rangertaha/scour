// SPDX-License-Identifier: GPL-3.0-or-later

// Command migcheck opens a database, running whatever migrations it needs, and
// reports how long that took. It exists to try a migration against a real
// database before it reaches one that matters.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/rangertaha/scour/internal/store"
)

func main() {
	start := time.Now()
	s, err := store.Open(os.Args[1])
	if err != nil {
		fmt.Println("MIGRATION FAILED:", err)
		os.Exit(1)
	}
	defer s.Close()
	fmt.Printf("migrated in %s\n", time.Since(start).Round(time.Millisecond))
}
