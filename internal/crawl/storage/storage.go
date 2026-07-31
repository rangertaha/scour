// SPDX-License-Identifier: GPL-3.0-or-later

// Package storage implements colly's storage.Storage over scour's database,
// so the visited set and the cookie jar survive a restart and can be shared by
// several crawl processes.
//
// colly's interface has no context and no error on the cookie methods, so this
// is where that boundary is crossed: failures are logged and treated as a
// miss, which degrades a shared jar into a cold one rather than failing the
// crawl.
package storage

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/rangertaha/scour/internal/store"
)

// Storage is a colly storage backed by the database, scoped to one entity.
type Storage struct {
	ctx      context.Context
	store    *store.Store
	entityID uint
}

// New returns a storage for one entity. The context bounds every query it
// makes, so cancelling the crawl cancels the bookkeeping with it.
func New(ctx context.Context, s *store.Store, entityID uint) *Storage {
	return &Storage{ctx: ctx, store: s, entityID: entityID}
}

// Init implements colly's storage.Storage. The schema is created by the
// store's migrations, so there is nothing to do here.
func (s *Storage) Init() error { return nil }

// Visited implements colly's storage.Storage.
func (s *Storage) Visited(requestID uint64) error {
	return s.store.MarkVisited(s.ctx, s.entityID, requestID)
}

// IsVisited implements colly's storage.Storage.
func (s *Storage) IsVisited(requestID uint64) (bool, error) {
	return s.store.IsVisited(s.ctx, s.entityID, requestID)
}

// Cookies implements colly's storage.Storage. A read failure returns no
// cookies, which costs a session rather than the crawl.
func (s *Storage) Cookies(u *url.URL) string {
	cookies, err := s.store.Cookies(s.ctx, s.entityID, u.Host)
	if err != nil {
		slog.Warn("read cookies failed", "host", u.Host, "err", err)
		return ""
	}
	return cookies
}

// SetCookies implements colly's storage.Storage. The interface returns
// nothing, so a write failure can only be logged.
func (s *Storage) SetCookies(u *url.URL, cookies string) {
	if err := s.store.SetCookies(s.ctx, s.entityID, u.Host, cookies); err != nil {
		slog.Warn("store cookies failed", "host", u.Host, "err", err)
	}
}
