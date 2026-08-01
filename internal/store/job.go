// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/rangertaha/scour/internal/fuzzy"
)

// JobState is what a job is doing, and what starting it would do.
//
// The states are distinguished by what a script would decide differently on
// seeing them. Budget and Done both end a run with the frontier intact, and
// they are separate because one means there is more to fetch and the other
// means there is not: a caller that cannot tell them apart cannot decide
// whether running again is worth anything.
type JobState string

const (
	// JobReady was created and never run. Starting begins from the seeds.
	JobReady JobState = "ready"
	// JobRunning has a run in flight. Starting reports that run.
	JobRunning JobState = "running"
	// JobPaused was frozen with its frontier kept. Starting resumes it.
	JobPaused JobState = "paused"
	// JobBudget hit --max-pages or --max-time with the frontier kept.
	// Starting resumes on a fresh budget.
	JobBudget JobState = "budget"
	// JobDone exhausted its frontier. Starting begins from the seeds again.
	JobDone JobState = "done"
	// JobStopped had its frontier discarded. Starting begins from the seeds.
	JobStopped JobState = "stopped"
	// JobFailed died in its last run. Starting resumes, frontier permitting.
	JobFailed JobState = "failed"
)

// Valid reports whether a state is one scour writes.
func (s JobState) Valid() bool {
	switch s {
	case JobReady, JobRunning, JobPaused, JobBudget, JobDone, JobStopped, JobFailed:
		return true
	}
	return false
}

// Job is a crawl: one item, a set of targets, and a policy.
//
// It exists because the item was carrying five things at once: a definition, a
// target list, a budget, a frontier and a run state. That left nowhere to put a
// second crawl of the same item over a different set of sites, and nowhere to
// say that one of them is paused while the other is not.
//
// A job is created by the first crawl and named after the item, so nobody has
// to learn the word before crawling anything. It becomes worth naming when one
// item needs two target sets or two policies.
type Job struct {
	ID     uint   `gorm:"primaryKey"`
	Name   string `gorm:"uniqueIndex;not null"`
	ItemID uint   `gorm:"index;not null"`

	// State is what a start would do. See JobState.
	State JobState `gorm:"index;not null;default:ready"`

	// Depth is how many links out from a target to follow. Zero takes the
	// configured default.
	Depth int
	// MaxPages and MaxTime bound one run. Zero is no bound.
	MaxPages int
	MaxTime  int64 // nanoseconds, so a stored job needs no custom type

	CreatedAt time.Time
	UpdatedAt time.Time
	// LastRunAt is when a run last started, for the listing.
	LastRunAt *time.Time

	Targets      []Target      `gorm:"constraint:OnDelete:CASCADE"`
	ContentTypes []ContentType `gorm:"constraint:OnDelete:CASCADE"`
}

// CreateJob creates a job for an item, or returns the existing one.
//
// Idempotent like every other add, so the first crawl can call it without
// checking and a second crawl of the same name is not an error.
func (s *Store) CreateJob(ctx context.Context, name string, itemID uint) (*Job, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("job name must not be empty")
	}
	if itemID == 0 {
		return nil, errors.New("a job needs an item")
	}

	job := Job{Name: name, ItemID: itemID, State: JobReady}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}).
		Create(&job).Error
	if err != nil {
		return nil, fmt.Errorf("create job %q: %w", name, err)
	}
	return s.Job(ctx, name)
}

// Job returns a job by name.
func (s *Store) Job(ctx context.Context, name string) (*Job, error) {
	var j Job
	err := s.db.WithContext(ctx).
		Preload("Targets").Preload("ContentTypes").
		Where("name = ?", name).First(&j).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if near := s.nearestJob(ctx, name); near != "" {
			return nil, fmt.Errorf("job %q: %w (did you mean %q?)", name, ErrNotFound, near)
		}
		return nil, fmt.Errorf("job %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get job %q: %w", name, err)
	}
	return &j, nil
}

// nearestJob names the closest existing job, for a name that was typed.
func (s *Store) nearestJob(ctx context.Context, name string) string {
	var names []string
	if err := s.db.WithContext(ctx).Model(&Job{}).Pluck("name", &names).Error; err != nil {
		return ""
	}
	return fuzzy.Nearest(name, names)
}

// Jobs lists the jobs, or those of one item when itemID is not zero.
func (s *Store) Jobs(ctx context.Context, itemID uint) ([]Job, error) {
	q := s.db.WithContext(ctx).Preload("Targets").Preload("ContentTypes").Order("name")
	if itemID != 0 {
		q = q.Where("item_id = ?", itemID)
	}
	var out []Job
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return out, nil
}

// SetJobState records what a job is doing.
func (s *Store) SetJobState(ctx context.Context, jobID uint, state JobState) error {
	if !state.Valid() {
		return fmt.Errorf("unknown job state %q", state)
	}
	err := s.db.WithContext(ctx).Model(&Job{}).
		Where("id = ?", jobID).
		Updates(map[string]any{"state": state, "updated_at": time.Now().UTC()}).Error
	if err != nil {
		return fmt.Errorf("set job state: %w", err)
	}
	return nil
}

// DeleteJob removes a job and its targets, leaving the item, the cached pages
// and the records alone.
func (s *Store) DeleteJob(ctx context.Context, name string) error {
	job, err := s.Job(ctx, name)
	if err != nil {
		return err
	}
	err = s.db.WithContext(ctx).Where("id = ?", job.ID).Delete(&Job{}).Error
	if err != nil {
		return fmt.Errorf("delete job %q: %w", name, err)
	}
	return nil
}

// JobForItem returns the job an unnamed crawl of an item means.
//
// A job is created by the first crawl and named after the item, so nobody has
// to learn the word before crawling anything. This is what resolves that: given
// an item, the job of the same name, created if it is not there.
//
// It is also what keeps `scour run vehicle` and `scour run vehicle` pointing
// at one frontier across the change, since the migration named every existing
// item's job after the item.
func (s *Store) JobForItem(ctx context.Context, item *Item) (*Job, error) {
	if item == nil {
		return nil, errors.New("no item")
	}
	job, err := s.Job(ctx, item.Name)
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return s.CreateJob(ctx, item.Name, item.ID)
}

// ContentTypesFor returns the content types a job restricts its crawl to.
func (s *Store) ContentTypesFor(ctx context.Context, jobID uint) ([]string, error) {
	var out []string
	err := s.db.WithContext(ctx).Model(&ContentType{}).
		Where("job_id = ?", jobID).Order("type").Pluck("type", &out).Error
	if err != nil {
		return nil, fmt.Errorf("content types for job %d: %w", jobID, err)
	}
	return out, nil
}

// JobPolicy is what bounds a job's runs. A nil field is left as it was, which
// is what lets `scour job set --depth 12` change the depth and nothing else.
type JobPolicy struct {
	Depth    *int
	MaxPages *int
	MaxTime  *time.Duration
}

// SetJobPolicy overwrites the bounds given and leaves the rest.
//
// Overwriting is the point, and is why this is `set` rather than `add`: a depth
// replaces the depth that was there, where a target joins the targets that were
// there. That is the whole distinction between the two verbs.
func (s *Store) SetJobPolicy(ctx context.Context, jobID uint, p JobPolicy) error {
	fields := map[string]any{}
	if p.Depth != nil {
		if *p.Depth < 0 {
			return fmt.Errorf("depth must not be negative, got %d", *p.Depth)
		}
		fields["depth"] = *p.Depth
	}
	if p.MaxPages != nil {
		if *p.MaxPages < 0 {
			return fmt.Errorf("max pages must not be negative, got %d", *p.MaxPages)
		}
		fields["max_pages"] = *p.MaxPages
	}
	if p.MaxTime != nil {
		if *p.MaxTime < 0 {
			return fmt.Errorf("max time must not be negative, got %s", *p.MaxTime)
		}
		fields["max_time"] = int64(*p.MaxTime)
	}
	if len(fields) == 0 {
		return nil
	}
	fields["updated_at"] = time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&Job{}).
		Where("id = ?", jobID).Updates(fields).Error; err != nil {
		return fmt.Errorf("set job policy: %w", err)
	}
	return nil
}

// DeleteContentType stops a job allowing a content type.
func (s *Store) DeleteContentType(ctx context.Context, jobID uint, typ string) error {
	res := s.db.WithContext(ctx).
		Where("job_id = ? AND type = ?", jobID, strings.ToLower(strings.TrimSpace(typ))).
		Delete(&ContentType{})
	if res.Error != nil {
		return fmt.Errorf("remove content type %q: %w", typ, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("content type %q: %w", typ, ErrNotFound)
	}
	return nil
}

// JobByID returns a job by its id, for the paths that hold one rather than a
// name, such as a dispatcher walking the frontier.
func (s *Store) JobByID(ctx context.Context, id uint) (*Job, error) {
	var j Job
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&j).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("job %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", id, err)
	}
	return &j, nil
}
