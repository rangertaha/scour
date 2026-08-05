// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/secret"
)

// TestSecretKeyPrintsOneAndOnlyOne.
func TestSecretKeyPrintsOneAndOnlyOne(t *testing.T) {
	first, _, code := run(t, "secret", "key")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	second, _, _ := run(t, "secret", "key")

	if strings.TrimSpace(first) == "" {
		t.Fatal("no key was printed")
	}
	if first == second {
		t.Error("two runs printed the same key")
	}
}

// TestTheClientNeverStandsUpItsOwnCluster.
//
// An earlier version connected with an embedded broker when nothing said where
// the cluster was. `secret ls` then answered with nothing, which looks exactly
// like a cluster whose secrets have been lost.
func TestTheClientNeverStandsUpItsOwnCluster(t *testing.T) {
	key, _, _ := run(t, "secret", "key")
	t.Setenv(secret.KeyVar, strings.TrimSpace(key))
	t.Setenv("SCOUR_SERVER", "nats://127.0.0.1:1")

	out, errOut, code := run(t, "secret", "ls")
	if code == 0 {
		t.Fatalf("listing against a cluster that is not there succeeded:\n%s", out)
	}
	if !strings.Contains(out+errOut, "127.0.0.1:1") {
		t.Errorf("the failure does not say where it looked:\n%s%s", out, errOut)
	}
}

// TestSecretNeedsAKeyAndSaysWhere.
func TestSecretNeedsAKeyAndSaysWhere(t *testing.T) {
	t.Setenv(secret.KeyVar, "")

	out, errOut, code := run(t, "secret", "ls")
	if code == 0 {
		t.Fatal("listed secrets with no sealing key")
	}
	if !strings.Contains(out+errOut, secret.KeyVar) {
		t.Errorf("the failure does not say what to set:\n%s%s", out, errOut)
	}
}

func TestSecretUsage(t *testing.T) {
	key, _, _ := run(t, "secret", "key")
	t.Setenv(secret.KeyVar, strings.TrimSpace(key))
	t.Setenv("SCOUR_SERVER", "nats://127.0.0.1:1")

	for name, args := range map[string][]string{
		"set with no name": {"secret", "set"},
		"set with two":     {"secret", "set", "a", "b"},
		"rm with no name":  {"secret", "rm"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, code := run(t, args...); code != 2 {
				t.Errorf("exit %d, want a usage error", code)
			}
		})
	}
}

func TestServeTakesNoArguments(t *testing.T) {
	if _, _, code := run(t, "serve", "somefile.hcl"); code != 2 {
		t.Errorf("exit %d, want a usage error", code)
	}
}
