// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"

	"github.com/urfave/cli/v3"
)

// ErrNothingToSet is returned when a set command is given no value to write.
var ErrNothingToSet = errors.New("nothing to set: name a bound, such as --depth")

func Need(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) != n {
		return nil, wanted(cmd, got, what)
	}
	return got, nil
}

// AtLeast checks for a minimum number of positional arguments.
func AtLeast(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) < n {
		return nil, wanted(cmd, got, what)
	}
	return got, nil
}

// wanted reports a wrong number of arguments, and shows the command's own help
// when there were none at all.
//
// Those are different mistakes. Somebody who typed the wrong number of names
// knows what the command is for and wants the count; somebody who typed the
// command bare is asking what it does, and answering that with one line of
// error makes them run it again with --help to get the answer they were
// already owed.
func wanted(cmd *cli.Command, got []string, what string) error {
	if len(got) > 0 {
		return Usagef("%s takes %s, got %d", cmd.Name, what, len(got))
	}
	if err := cli.ShowSubcommandHelp(cmd); err != nil {
		return err
	}
	// The help has just been printed; repeating the complaint under it would
	// be the third time the same thing is said on one screen.
	fmt.Fprintf(cmd.Root().ErrWriter, "\n%s takes %s\n", cmd.Name, what)
	return ErrSilent
}

// AtMost checks for a maximum number of positional arguments.
func AtMost(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) > n {
		return nil, Usagef("%s takes %s, got %d", cmd.Name, what, len(got))
	}
	return got, nil
}
