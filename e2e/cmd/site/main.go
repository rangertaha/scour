// SPDX-License-Identifier: GPL-3.0-or-later

// Command site serves the end-to-end fixture site, so it can be opened in a
// browser or crawled by a real scour.
//
// The tests do not need it: they start the same handler on a random port. This
// exists because a fixture you can look at is a fixture you will add to, and
// because pointing the actual binary at it is the shortest way to find out what
// a change did.
//
//	go run ./e2e/cmd/site
//	scour item add gazette --template article
//	scour job add gazette -i gazette -u http://localhost:8099/
//	scour run gazette
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rangertaha/scour/e2e"
)

func main() {
	addr := flag.String("addr", ":8099", "address to listen on")
	flag.Parse()

	shown := *addr
	if strings.HasPrefix(shown, ":") {
		shown = "localhost" + shown
	}
	fmt.Fprintf(os.Stderr, "serving the fixture site on http://%s/\n", shown)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           e2e.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
