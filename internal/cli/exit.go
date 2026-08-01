// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/store"
)

// Exit codes, so a script can branch without reading the text.
//
// The distinctions are the ones a caller acts on differently. Not found is not
// the same as failed, because a missing item is a thing to create and a failed
// command is a thing to retry. Unreachable is not the same as failed, because
// one is the service and the other is the request. And an empty result is
// normally success, because "no records above 0.9 yet" is an answer.
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitUsage     = 2
	ExitNotFound  = 3
	ExitEmpty     = 4
	ExitUnreached = 5
)

// ErrEmpty is returned by a command that found nothing, when --strict asked it
// to treat that as a failure.
//
// It only ever reaches the caller under --strict, because an empty result is an
// answer rather than a fault: a listing with nothing in it has succeeded at
// telling you there is nothing.
var ErrEmpty = errors.New("no results")

// ErrUnreachable is returned when a service was configured and could not be
// reached, which is a different thing from the request failing.
var ErrUnreachable = errors.New("service unreachable")

// ExitCode maps an error to the code a script reads.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrEmpty):
		return ExitEmpty
	case errors.Is(err, ErrUnreachable):
		return ExitUnreached
	case errors.Is(err, store.ErrNotFound):
		return ExitNotFound
	case isUsage(err):
		return ExitUsage
	default:
		return ExitFailed
	}
}

// isUsage reports whether an error is about how the command was typed rather
// than about what it tried to do.
//
// urfave reports a bad flag as a plain error, so there is nothing to match on
// but its own exit coder, which it sets to 1 for these. The argument checks in
// this package return their own sentinel instead.
func isUsage(err error) bool {
	if errors.Is(err, ErrUsage) {
		return true
	}
	var coder ucli.ExitCoder
	return errors.As(err, &coder) && coder.ExitCode() == ExitUsage
}

// ErrUsage marks an error as being about how the command was typed.
var ErrUsage = errors.New("usage")

// Usagef builds a usage error, which exits 2 rather than 1.
func Usagef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))
}

// Empty reports nothing found, as a failure only when --strict was given.
//
// Callers hand it what they would have printed, so the message is the same
// either way and only the exit code moves.
func (a *App) Empty(format string, args ...any) error {
	a.Printf(format, args...)
	if a.Strict {
		return ErrEmpty
	}
	return nil
}
