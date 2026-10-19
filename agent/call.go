package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CallRead runs one tool for who outside any plan: the voice conversation's
// model asks for a tool by name, mid-sentence, and this is the whole of what
// that request can do. The same rules as a plan step -- registered, reading
// only, every permission held, arguments validated strictly, its own deadline,
// transient failures tried again -- and every attempt reported to audit.
//
// A tool that changes anything cannot be called this way at all: the voice
// conversation has no approval step, so it has no Execute tools.
func (r *Registry) CallRead(ctx context.Context, who Principal, name string, raw map[string]string,
	audit func(ToolEvent)) (*Tool, *Output, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	if t.Class != Read {
		return t, nil, fmt.Errorf("%w: %q", ErrNotPlannable, name)
	}
	report := func(args map[string]string, attempt int, result, msg string, d time.Duration) {
		if audit != nil {
			audit(ToolEvent{User: who.Username(), Tool: t.Name, Audit: t.Audit, Args: redactArgs(t, args),
				Attempt: attempt, Result: result, Error: clip(msg, 200), Duration: d})
		}
	}
	if !t.Allowed(who) {
		report(nil, 0, "denied", "", 0)
		return t, nil, ErrForbidden
	}
	if raw == nil {
		raw = map[string]string{}
	}
	args, err := t.Validate(raw)
	if err != nil {
		report(raw, 0, "error", err.Error(), 0)
		return t, nil, err
	}
	var out *Output
	for attempt := 0; attempt <= t.Retries; attempt++ {
		start := time.Now()
		out, err = callTool(ctx, t, &Call{Tool: t, Args: args, Who: who})
		if err == nil {
			report(args, attempt+1, "ok", "", time.Since(start))
			return t, out, nil
		}
		report(args, attempt+1, "error", err.Error(), time.Since(start))
		if !errors.Is(err, ErrTransient) || ctx.Err() != nil {
			break
		}
	}
	return t, nil, err
}
