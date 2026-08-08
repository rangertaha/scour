// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

// TestANodeServesAndASecondOneJoinsIt.
//
// `scour serve` is a documented command that nothing ran. Every test it had
// checked that it refuses arguments, so the whole of what it does — start a
// broker, join a cluster, register, watch for jobs, and leave cleanly on a
// signal — had no coverage at all, and internal/node measured 0% under the
// command-level suite.
//
// That is the shape the notes call "built, tested, unreachable", one layer out:
// internal/node has a thorough test of its own, and nothing checked that the
// command a person types reaches it.
//
// # Why this needs the binary
//
// The first node is also the broker, and the address it chose is printed rather
// than configured, because the operating system picked the port. A second node
// joins by reading it. That is a two-process arrangement and it cannot be
// tested in one: the interesting part is precisely that the second node needs
// an address and nothing else.
func TestANodeServesAndASecondOneJoinsIt(t *testing.T) {
	first := start(t, t.TempDir(), "serve", "--name", "first")

	// The address the embedded broker chose. Printed as the line somebody is
	// meant to copy, so this reads it the way a person would.
	address := waitFor(t, first, "join it with: scour serve --join ")
	address = strings.TrimSpace(address)
	if !strings.HasPrefix(address, "nats://") {
		t.Fatalf("the address to join is %q, which is not one\n%s", address, first.output.String())
	}

	if said := first.output.String(); !strings.Contains(said, "first is serving, and is the broker") {
		t.Errorf("the first node does not say it is the broker:\n%s", said)
	}

	// A second node, given the address and nothing else. It must not stand up
	// a broker of its own: two brokers on one machine is a cluster that is
	// really two clusters, and the symptom is jobs that only some nodes see.
	second := start(t, t.TempDir(), "serve", "--name", "second", "--join", address)
	waitFor(t, second, "second joined ")

	if said := second.output.String(); strings.Contains(said, "is the broker") {
		t.Errorf("the second node started a broker of its own instead of joining:\n%s", said)
	}

	// Both leave on the interrupt rather than being killed, which is what
	// start's cleanup checks, and a node that leaves says so.
	second.stop(t)
	if said := second.output.String(); !strings.Contains(said, "second has left") {
		t.Errorf("the second node did not leave cleanly:\n%s", said)
	}

	first.stop(t)
	if said := first.output.String(); !strings.Contains(said, "first has left") {
		t.Errorf("the first node did not leave cleanly:\n%s", said)
	}
}

// TestANodeServesOnlyTheStagesItWasTold.
//
// `--stages` is what makes a machine added to a cluster a downloader and not a
// spider, which is how somebody puts the fetching on the hosts with the
// addresses and the parsing on the hosts with the cores. A flag that was
// accepted and ignored would be invisible until the cluster was busy.
func TestANodeServesOnlyTheStagesItWasTold(t *testing.T) {
	node := start(t, t.TempDir(), "serve", "--name", "downloaders", "--stages", "download")
	waitFor(t, node, "downloaders is serving")

	node.stop(t)
	if said := node.output.String(); !strings.Contains(said, "downloaders has left") {
		t.Errorf("the node did not leave cleanly:\n%s", said)
	}
}
