// SPDX-License-Identifier: GPL-3.0-or-later

// Command mocksite serves a website that behaves badly on purpose.
//
// It is the same site the tests crawl, from the same package, so what you
// debug against by hand and what the suite runs against cannot drift apart.
//
//	go run ./cmd/mocksite
//	scour scrape --url http://127.0.0.1:842/article/jsonld job.hcl
//
// It prints every path as it is asked for, which is usually the fastest way to
// see what a crawl is really doing: a URL fetched twice, a redirect followed
// somewhere unexpected, or a request for something robots.txt forbids.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rangertaha/scour/internal/mocksite"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "what to listen on; :0 asks the operating system")
	slow := flag.Duration("slow", 2*time.Second, "how long /slow takes to answer")
	robots := flag.String("robots", "", "what /robots.txt serves; empty is the default one")
	quiet := flag.Bool("quiet", false, "do not print each request")
	flag.Parse()

	site := mocksite.New(mocksite.Options{Robots: *robots, Slow: *slow})

	var handler http.Handler = site
	if !*quiet {
		handler = logging(site)
	}

	// Listen before announcing, so the address printed is one that is already
	// accepting. Printing first and binding after is how a script that pipes
	// this into a crawl races the server it just started.
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("mocksite: %v", err)
	}

	fmt.Printf("mocksite listening on http://%s\n", listener.Addr())
	fmt.Printf("try: scour scrape --url http://%s/article/jsonld job.hcl\n", listener.Addr())

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		fmt.Printf("\nmocksite served %d requests\n", site.Total())
		_ = server.Close()
	}()

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("mocksite: %v", err)
	}
}

// logging prints each request, which is what makes this useful to watch.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("  %-6s %s\n", r.Method, r.URL.RequestURI())
		next.ServeHTTP(w, r)
	})
}
