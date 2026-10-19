package dependencies

import (
	"context"
	"sync"
	"time"
)

// Start checks every dependency in the background until ctx ends: every
// Interval while it answers, and on the breaker's schedule while it does
// not -- after each cooldown, which doubles up to MaxCooldown. That is the
// exponential backoff: a server that is down is asked less and less often,
// never never, and the first answer after it comes back closes the breaker
// for everyone.
func (r *Registry) Start(ctx context.Context) {
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				r.checkDue(ctx)
			}
		}
	}()
}

// CheckAll checks every dependency now and waits for the answers: what a
// status page's "check again" asks for.
func (r *Registry) CheckAll(ctx context.Context) {
	r.mu.Lock()
	var names []string
	for _, n := range r.order {
		if d := r.deps[n]; d.spec.Check != nil && !d.checking {
			d.checking = true
			names = append(names, n)
		}
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Go(func() { r.check(ctx, n) })
	}
	wg.Wait()
}

// checkDue starts the checks whose time has come, one in flight per
// dependency.
func (r *Registry) checkDue(ctx context.Context) {
	now := r.now()
	r.mu.Lock()
	var due []string
	for _, n := range r.order {
		d := r.deps[n]
		if d.spec.Check == nil || d.checking || now.Before(d.nextCheck) {
			continue
		}
		d.checking = true
		due = append(due, n)
	}
	r.mu.Unlock()
	for _, n := range due {
		go r.check(ctx, n)
	}
}

func (r *Registry) check(ctx context.Context, name string) {
	r.mu.Lock()
	d := r.deps[name]
	check := d.spec.Check
	r.mu.Unlock()
	_ = r.Do(ctx, name, check)
	r.mu.Lock()
	defer r.mu.Unlock()
	d.checking = false
	if d.breaker == Open {
		d.nextCheck = d.openUntil // the half-open probe
	} else {
		d.nextCheck = r.now().Add(d.spec.Interval)
	}
}
