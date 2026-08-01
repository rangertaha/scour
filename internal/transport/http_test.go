// SPDX-License-Identifier: GPL-3.0-or-later

package transport

import (
	"net/http"
	"testing"
	"time"
)

// A round trip is always bounded.
//
// Zero is not "the default" here, it is "wait forever": a server that accepts
// the connection and then says nothing holds a crawler thread until the process
// ends. A config carrying no timeout, or one where somebody wrote timeout =
// "0s", must still come out bounded.
func TestRoundTripIsAlwaysBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want time.Duration
	}{
		{"nothing set", Config{}, Timeout},
		{"explicitly zero", Config{Timeout: 0}, Timeout},
		{"negative", Config{Timeout: -time.Second}, Timeout},
		{"honoured when given", Config{Timeout: 5 * time.Second}, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := New("http", tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			tr, ok := rt.(*http.Transport)
			if !ok {
				t.Fatalf("got %T, want *http.Transport", rt)
			}
			if tr.ResponseHeaderTimeout != tc.want {
				t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, tc.want)
			}
		})
	}
}
