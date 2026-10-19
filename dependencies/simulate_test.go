package dependencies

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A simulated outage refuses calls exactly as an open breaker does, reports
// itself as a test, leaves the real breaker alone, and ends on its own.
func TestASimulatedOutageLooksLikeARealOneAndEnds(t *testing.T) {
	r := New()
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.Register(Spec{Name: "pm", Label: "Reporting database"})

	if err := r.Simulate("pm", 10*time.Minute, "boss"); err != nil {
		t.Fatal(err)
	}
	called := false
	err := r.Do(context.Background(), "pm", func(context.Context) error { called = true; return nil })
	if called || !errors.Is(err, ErrOpen) {
		t.Fatalf("a simulated outage let the call through (called %v, err %v)", called, err)
	}
	if r.Allow("pm") {
		t.Error("Allow says yes during a simulated outage")
	}
	st, _ := r.Status("pm")
	if st.State != Offline || st.Breaker != "simulated" || st.ErrorClass != "simulated" {
		t.Errorf("status during the test = %+v", st)
	}
	if got := r.Simulations(); len(got) != 1 || got[0].By != "boss" {
		t.Errorf("simulations = %+v", got)
	}

	now = now.Add(11 * time.Minute)
	if !r.Allow("pm") {
		t.Error("the simulated outage did not end on its own")
	}
	if err := r.Do(context.Background(), "pm", func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Errorf("after the test the call did not go through: %v", err)
	}
	if st, _ := r.Status("pm"); st.Breaker != Closed || st.State != Live {
		t.Errorf("the real breaker was changed by the test: %+v", st)
	}
}

func TestSimulationLimits(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "pm"})
	for _, d := range []time.Duration{0, -time.Minute, 31 * time.Minute} {
		if err := r.Simulate("pm", d, "x"); err == nil {
			t.Errorf("a %v simulation was accepted", d)
		}
	}
	if err := r.Simulate("nobody", time.Minute, "x"); err == nil {
		t.Error("an unknown dependency was accepted")
	}
	r.Simulate("pm", time.Minute, "x")
	r.EndSimulation("pm")
	if !r.Allow("pm") || len(r.Simulations()) != 0 {
		t.Error("ending a simulation did not end it")
	}
}
