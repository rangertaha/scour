// SPDX-License-Identifier: GPL-3.0-or-later

// Package cli is what the command line shares: where output goes, what an exit
// code means, and how a job document is read.
//
// Commands live in packages under this one, each returning its own
// [*ucli.Command], and the tree is assembled in cmd/scour. No command reaches
// into another, so a command can be read on its own and moved without
// unpicking anything.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/engine"
)

// Exit codes.
//
// A document that is wrong and a scour that is broken are different things, and
// a script driving this needs to tell them apart: the first means fix your
// file, the second means the tool needs looking at. Conflating them is how a
// broken build gets retried forever.
const (
	// ExitOK is success.
	ExitOK = 0
	// ExitInvalid means the document was read and refused.
	ExitInvalid = 1
	// ExitUsage means the command line itself was wrong.
	ExitUsage = 2
	// ExitFailed means scour could not do what it was asked, for a reason that
	// is not the document's fault: a file it could not read, a disk that is
	// full.
	ExitFailed = 3
)

// App is what every command is given: somewhere to write, and somewhere to
// read from.
//
// Passed in rather than reaching for os.Stdout, so a test can run a command and
// read what it printed. A command that wrote to the process's streams directly
// could only be tested by running the process.
type App struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader
}

// New returns an App over the process's own streams.
func New() *App {
	return &App{Out: os.Stdout, Err: os.Stderr, In: os.Stdin}
}

// Printf writes to the command's output.
func (a *App) Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Out, format, args...)
}

// Println writes a line to the command's output.
func (a *App) Println(args ...any) {
	_, _ = fmt.Fprintln(a.Out, args...)
}

// Warnf writes to the command's error stream, for things that are worth saying
// but are not why the command failed.
func (a *App) Warnf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Err, format, args...)
}

// Error is a failure with an exit code attached.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Usagef reports a command line that does not make sense.
func Usagef(format string, args ...any) error {
	return &Error{Code: ExitUsage, Err: fmt.Errorf(format, args...)}
}

// Invalidf reports a document that was read and refused.
func Invalidf(format string, args ...any) error {
	return &Error{Code: ExitInvalid, Err: fmt.Errorf(format, args...)}
}

// Failedf reports something that went wrong which is not the document's fault.
func Failedf(format string, args ...any) error {
	return &Error{Code: ExitFailed, Err: fmt.Errorf(format, args...)}
}

// CodeOf is the exit code an error should produce.
func CodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}

	// A flag the parser did not recognise is a wrong command line, not a
	// broken scour. urfave/cli returns a plain error for it, so without this
	// `scour job valid --nope job.hcl` exited 3, and a build script that
	// retries on 3 and gives up on 2 would retry a typo forever. That is
	// exactly the conflation the exit codes are documented to prevent.
	if usage(err) {
		return ExitUsage
	}
	return ExitFailed
}

// usage recognises the command line's own complaints.
//
// By message, which is unpleasant and is what the library gives us: the errors
// from parseFlags are plain fmt.Errorf values implementing no interface worth
// matching. Kept narrow, and to phrases the library builds itself, so that a
// command's own error text cannot be mistaken for one.
func usage(err error) bool {
	for _, phrase := range []string{
		"flag provided but not defined",
		"flag needs an argument",
		"invalid value",
		"invalid boolean value",
	} {
		if strings.Contains(err.Error(), phrase) {
			return true
		}
	}
	return false
}

// Load reads and parses a job document, without validating it.
//
// Reading a file and accepting a submission are different decisions, so this
// does only the first. A command that needs the second calls [Accept].
func Load(path string) (*engine.Document, error) {
	src, err := FromFile(path)
	if err != nil {
		return nil, err
	}
	return src.Read()
}

// Accept reads, parses and validates a job document, which is what everything
// that acts on one needs.
func Accept(path string) (*engine.Document, error) {
	src, err := FromFile(path)
	if err != nil {
		return nil, err
	}
	return src.Accept()
}

// Problems renders joined errors one per line, indented.
//
// Validation reports everything wrong with a document at once, so the output
// has to be a list rather than a sentence. errors.Join separates with newlines
// already; this indents them so a reader can see where the list starts and
// ends.
func Problems(err error) string {
	if err == nil {
		return ""
	}

	lines := strings.Split(strings.TrimRight(err.Error(), "\n"), "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  " + line)
	}
	return b.String()
}

// OneJob returns the named job, or the only one if no name was given.
//
// A document usually holds one job, and making every command take a name would
// be ceremony for the common case. A document holding several and a command
// given no name is genuinely ambiguous, so that is refused rather than guessed.
func OneJob(doc *engine.Document, name string) (*engine.Job, error) {
	if name != "" {
		for _, job := range doc.Jobs {
			if job.Name == name {
				return job, nil
			}
		}
		return nil, Usagef("no job named %q in this document. It has %s",
			name, strings.Join(doc.Names(), ", "))
	}

	switch len(doc.Jobs) {
	case 0:
		return nil, Invalidf("the document has no jobs")
	case 1:
		return doc.Jobs[0], nil
	default:
		return nil, Usagef("the document has %d jobs, so one has to be named: %s",
			len(doc.Jobs), strings.Join(doc.Names(), ", "))
	}
}

// Run executes the command tree and returns the process's exit code.
//
// Separate from main so a test can run the whole command line, arguments and
// all, and read what it printed without starting a process.
func Run(ctx context.Context, a *App, root *ucli.Command, args []string) int {
	err := root.Run(ctx, args)
	if err == nil {
		return ExitOK
	}

	// A command that already said what went wrong does not say it twice. The
	// commands print their own detail because they have more of it: the
	// document, the line, the list of problems.
	if msg := err.Error(); msg != "" && msg != "refused" {
		_, _ = fmt.Fprintf(a.Err, "scour: %s\n", msg)
	}
	return CodeOf(err)
}

// Source is a job document and where it came from.
//
// # Why the bytes are not enough
//
// Because a document that reads a list from a file beside it, with
// `domains = lines("domains.txt")`, means something only in the directory it
// came from. Parsing it anywhere else refuses the call by name, which is
// correct for the cluster's stored copy and wrong for a file somebody named on
// the command line.
//
// Carrying the two together is what stops that being a thing to remember. It
// was one, and three commands forgot: `job show --file`, `job spec --file` and
// `job train --file` each read a path with os.ReadFile and parsed the bytes
// alone, so `scour job valid news.hcl` accepted a document that
// `scour job show --file news.hcl` then refused. There is now no way to hold a
// document without also holding where it came from.
type Source struct {
	// Bytes is the document.
	Bytes []byte

	// Name is what a parse error is reported against: a path, or a job's name.
	Name string

	// Dir is the directory to resolve `lines()` against. Empty means the
	// document did not come from a file, which is what the cluster's copy is
	// and why it carries its lists rather than a reference to them.
	Dir string
}

// FromFile reads a document from disk, remembering where it was.
func FromFile(path string) (Source, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return Source{}, Failedf("%v", err)
	}
	return Source{Bytes: src, Name: path, Dir: filepath.Dir(path)}, nil
}

// FromCluster is a document the cluster stored.
//
// It has no directory on purpose: a stored job carries its lists rather than a
// reference to a file no node can see, so a `lines()` call surviving in one is
// a job that was submitted without being expanded and should be refused rather
// than resolved against whatever directory this process happens to be in.
func FromCluster(name string, document []byte) Source {
	return Source{Bytes: document, Name: name}
}

// Read parses a document without validating it.
func (s Source) Read() (*engine.Document, error) {
	doc, err := engine.ParseIn(s.Bytes, s.Name, s.Dir)
	if err != nil {
		return nil, Invalidf("%v", err)
	}
	return doc, nil
}

// Accept parses and validates, which is what everything that acts on a document
// needs.
func (s Source) Accept() (*engine.Document, error) {
	doc, err := s.Read()
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(); err != nil {
		return nil, Invalidf("%s", Problems(err))
	}
	return doc, nil
}

// AcceptBytes parses and validates a document that did not come from a file.
//
// A thin way of saying [FromCluster] followed by [Source.Accept], kept because
// several callers hold bytes that genuinely have no directory.
func AcceptBytes(src []byte, name string) (*engine.Document, error) {
	return FromCluster(name, src).Accept()
}

// oneName adapts an action that takes exactly one job name.
//
// The counterpart of [oneFile], and separate from it because the two halves of
// this command line take different arguments for a reason: a document is a file
// somebody is editing, and a job is a name the cluster already knows. A command
// that took either would have to guess which it had been given.
func oneName(fn func(ctx context.Context, name string) error) func(context.Context, *ucli.Command) error {
	return func(ctx context.Context, cmd *ucli.Command) error {
		switch cmd.Args().Len() {
		case 1:
			return fn(ctx, cmd.Args().First())
		case 0:
			return Usagef("no job named")
		default:
			return Usagef("one job at a time, got %d", cmd.Args().Len())
		}
	}
}
