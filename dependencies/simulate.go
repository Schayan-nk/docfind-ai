package dependencies

import (
	"fmt"
	"time"
)

// Resilience test mode: an administrator can make the application treat one
// dependency as down for a few minutes, to see -- before it happens for real
// -- that the reports fall back to their copy, the assistant to its rules,
// the inbox to its local store, and the banner says so.
//
// A simulated outage is refused exactly the way an open circuit breaker is:
// Do returns an OpenError without calling the dependency, and Allow says no.
// Nothing is really cut and nothing about the real breaker changes, so when
// the simulation ends -- on its own after its time, or when ended -- the
// dependency is exactly as it was.

// MaxSimulation bounds how long one simulated outage can last: long enough
// to walk through the pages, short enough that a forgotten one ends itself.
const MaxSimulation = 30 * time.Minute

// Simulation is one simulated outage in force.
type Simulation struct {
	Name  string    `json:"name"`
	Label string    `json:"label"`
	Until time.Time `json:"until"`
	By    string    `json:"by"`
}

// Simulate makes name look down for d, on behalf of by.
func (r *Registry) Simulate(name string, d time.Duration, by string) error {
	if d <= 0 || d > MaxSimulation {
		return fmt.Errorf("dependencies: a simulated outage lasts 1 to %d minutes", int(MaxSimulation/time.Minute))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deps[name] == nil {
		return fmt.Errorf("dependencies: no dependency named %q", name)
	}
	if r.sims == nil {
		r.sims = map[string]Simulation{}
	}
	r.sims[name] = Simulation{Name: name, Label: r.deps[name].spec.Label, Until: r.now().Add(d), By: by}
	return nil
}

// EndSimulation ends a simulated outage early.
func (r *Registry) EndSimulation(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sims, name)
}

// Simulations is every simulated outage still in force.
func (r *Registry) Simulations() []Simulation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Simulation{}
	for _, n := range r.order {
		if s, ok := r.simulated(n); ok {
			out = append(out, s)
		}
	}
	return out
}

// simulated reports a live simulation for name, dropping an expired one.
// Called with r.mu held.
func (r *Registry) simulated(name string) (Simulation, bool) {
	s, ok := r.sims[name]
	if !ok {
		return Simulation{}, false
	}
	if !r.now().Before(s.Until) {
		delete(r.sims, name)
		return Simulation{}, false
	}
	return s, true
}
