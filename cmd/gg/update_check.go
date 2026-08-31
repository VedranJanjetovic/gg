package main

import (
	"context"
	"os"
	"sync"

	"github.com/VedranJanjetovic/gg/internal/update"
	"github.com/VedranJanjetovic/gg/internal/version"
)

// cachedUpdateChecker answers the TUI footer's "is a newer gg released?" once
// per process. The release API is rate limited, and every attach re-asking it
// would spend that budget without telling the user anything new. It is
// deliberately built without an installer: the footer only advertises the
// update, `gg update` performs it.
type cachedUpdateChecker struct {
	service   *update.Service
	once      sync.Once
	available bool
	err       error
}

func newCachedUpdateChecker() *cachedUpdateChecker {
	return &cachedUpdateChecker{service: update.NewServiceWithDependencies(
		func() string { return version.Current().Version },
		update.NewHTTPReleaseLookup(nil, os.Getenv("GG_RELEASE_SOURCE")),
	)}
}

func (c *cachedUpdateChecker) Available(ctx context.Context) (bool, error) {
	c.once.Do(func() { c.available, c.err = c.service.Available(ctx) })
	return c.available, c.err
}
