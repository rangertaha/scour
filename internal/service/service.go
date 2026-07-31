// SPDX-License-Identifier: GPL-3.0-or-later

// Package service runs scour's components.
//
// One binary, many roles: a laptop runs them all in one process, a deployment
// spreads them across machines. The components are identical either way, which
// is the property the topology-equivalence tests exist to protect.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// Role names a component.
type Role string

// The roles a process can take.
const (
	// RoleStore writes what the pipeline produces to the database. It is the
	// only component that touches it.
	RoleStore Role = "store"
	// RoleCrawl fetches pages. It is the only component that touches the
	// network.
	RoleCrawl Role = "crawl"
)

// AllRoles is every role, which is what a bare `scour run` starts.
var AllRoles = []Role{RoleStore, RoleCrawl}

// ParseRoles turns a comma-separated list into roles, rejecting unknown names
// rather than silently starting fewer components than asked for.
func ParseRoles(spec string) ([]Role, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "all" {
		return AllRoles, nil
	}

	valid := make(map[Role]bool, len(AllRoles))
	for _, r := range AllRoles {
		valid[r] = true
	}

	var out []Role
	seen := map[Role]bool{}
	for _, name := range strings.Split(spec, ",") {
		r := Role(strings.ToLower(strings.TrimSpace(name)))
		if r == "" {
			continue
		}
		if !valid[r] {
			return nil, fmt.Errorf("unknown role %q, have %s", r, strings.Join(names(AllRoles), ", "))
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no roles given")
	}
	return out, nil
}

func names(roles []Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return out
}

// Service is one component.
type Service interface {
	// Role is which component this is.
	Role() Role
	// Start runs until ctx is cancelled. It must return when it does.
	Start(ctx context.Context) error
}

// Supervisor runs a set of services and stops them together.
type Supervisor struct {
	services []Service
}

// New returns a supervisor over the given services.
func New(services ...Service) *Supervisor {
	return &Supervisor{services: services}
}

// Run starts every service and blocks until ctx is cancelled or one fails.
//
// The first error wins and cancels the rest: a pipeline missing a stage is not
// a pipeline, so limping on with one component down would produce results that
// look complete and are not.
func (s *Supervisor) Run(ctx context.Context) error {
	if len(s.services) == 0 {
		return errors.New("no services to run")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)

	for _, svc := range s.services {
		wg.Add(1)
		go func(svc Service) {
			defer wg.Done()
			slog.Info("service starting", "role", svc.Role())

			if err := svc.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				once.Do(func() { first = fmt.Errorf("%s: %w", svc.Role(), err) })
				cancel()
			}
			slog.Info("service stopped", "role", svc.Role())
		}(svc)
	}

	wg.Wait()
	return first
}
