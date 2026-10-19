package dependencies

import (
	"context"
	"sync"
	"time"
)

// Cached holds the last good answer from a dependency.
type Cached[T any] struct {
	mu  sync.Mutex
	val T
	at  time.Time
	ok  bool
}

// Put stores v as the last good answer, taken at at.
func (c *Cached[T]) Put(v T, at time.Time) {
	c.mu.Lock()
	c.val, c.at, c.ok = v, at, true
	c.mu.Unlock()
}

// Get is the last good answer and when it was taken; ok is false when there
// has been none.
func (c *Cached[T]) Get() (v T, at time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val, c.at, c.ok
}

// Source says where an answer came from.
type Source string

const (
	FromLive  Source = "live"
	FromCache Source = "cache"
)

// Meta travels with an answer, so a page can say "cached, last refreshed
// 08:42" instead of passing old figures off as new.
type Meta struct {
	Source Source    `json:"source"`
	At     time.Time `json:"at"`
	// Err is why the live answer was not used, when it was not.
	Err error `json:"-"`
}

// Fetch asks the dependency through the registry and keeps the answer; when
// it cannot answer -- breaker open, error, or slower than its MaxWait -- the
// last good answer is returned instead, marked as such. Only when there has
// never been one is the error returned.
func Fetch[T any](ctx context.Context, r *Registry, name string, c *Cached[T],
	get func(context.Context) (T, error)) (T, Meta, error) {
	var v T
	err := r.Do(ctx, name, func(ctx context.Context) error {
		var e error
		v, e = get(ctx)
		return e
	})
	if err == nil {
		now := r.now()
		c.Put(v, now)
		return v, Meta{Source: FromLive, At: now}, nil
	}
	if old, at, ok := c.Get(); ok {
		return old, Meta{Source: FromCache, At: at, Err: err}, nil
	}
	return v, Meta{Err: err}, err
}
