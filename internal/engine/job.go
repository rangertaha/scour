// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Job is one crawl a client asked for: what to fetch, and the engine
// configuration to fetch it under.
//
// A job carries its own configuration rather than inheriting the server's, so
// two jobs submitted to the same cluster can cache to different buckets, crawl
// at different rates and hand different stages to different people. See
// [Config].
type Job struct {
	// Name identifies the job. It is the client's, not generated, because a
	// client resubmitting the same crawl means the same job, and a name is how
	// they say so.
	Name string `json:"name"`

	// Targets are the URLs the crawl starts from.
	Targets []string `json:"targets,omitempty"`

	// Config is the engine configuration this job runs under.
	Config Config `json:"config"`
}

// WithDefaults returns the job with its configuration completed.
func (j Job) WithDefaults() Job {
	j.Config = j.Config.WithDefaults()
	return j
}

// Validate reports every problem with a job at once, its own and its
// configuration's.
func (j Job) Validate() error {
	var problems []error

	if strings.TrimSpace(j.Name) == "" {
		problems = append(problems, errors.New("name: a job needs one"))
	}
	if len(j.Targets) == 0 {
		problems = append(problems, fmt.Errorf("job %q: no targets, so there is nowhere to start", j.Name))
	}

	for i, target := range j.Targets {
		u, err := url.Parse(target)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("job %q: target %d %q: %w", j.Name, i, target, err))
		case u.Scheme != "http" && u.Scheme != "https":
			// A crawler that followed file:// would read the disk of whichever
			// machine picked the job up, which is a submitted job reaching
			// somewhere it was never given.
			problems = append(problems, fmt.Errorf("job %q: target %d %q: only http and https are crawled", j.Name, i, target))
		case u.Host == "":
			problems = append(problems, fmt.Errorf("job %q: target %d %q: no host", j.Name, i, target))
		}
	}

	if err := j.Config.Validate(); err != nil {
		problems = append(problems, fmt.Errorf("job %q: %w", j.Name, err))
	}

	return errors.Join(problems...)
}

// Submission is what one HCL document contains: every job in it.
//
// A document rather than a job, because submitting a set together is the
// normal case. A client describing a crawl usually has several related jobs,
// and sending them one at a time means a half-applied submission when the
// third is rejected.
type Submission []Job

// WithDefaults completes every job in the submission.
func (s Submission) WithDefaults() Submission {
	out := make(Submission, len(s))
	for i, j := range s {
		out[i] = j.WithDefaults()
	}
	return out
}

// Validate reports every problem across every job, and rejects duplicate
// names.
//
// All of them at once, for the same reason a single job's errors are joined: a
// client fixing a submission of ten jobs one error at a time is a client we
// have made an enemy of.
func (s Submission) Validate() error {
	var problems []error

	if len(s) == 0 {
		problems = append(problems, errors.New("no jobs"))
	}

	seen := map[string]bool{}
	for _, j := range s {
		if seen[j.Name] {
			// Names are how a resubmission finds its job, so two jobs sharing
			// one would make that lookup ambiguous the moment it mattered.
			problems = append(problems, fmt.Errorf("job %q: submitted twice in the same document", j.Name))
		}
		seen[j.Name] = true

		if err := j.Validate(); err != nil {
			problems = append(problems, err)
		}
	}

	return errors.Join(problems...)
}

// Names lists the jobs in the submission, in the order they were written.
func (s Submission) Names() []string {
	out := make([]string, 0, len(s))
	for _, j := range s {
		out = append(out, j.Name)
	}
	return out
}
