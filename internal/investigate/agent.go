// Package investigate runs an automatic Claude investigation of an alert
// group against Grafana, for the groups Jev judged not self-explanatory.
//
// The model never touches the network itself. It names a tool; this package
// checks the name against a read-only allowlist and calls the operator's
// mcp-grafana server. The result is prose for a Discord embed.
package investigate

import (
	"context"
	"fmt"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
)

// Agent investigates one alert group synchronously.
type Agent struct {
	Loop    *Loop
	Grafana *Grafana
	Limits  Limits
	// Context is the operator's description of their environment.
	Context string
	Now     func() time.Time
}

// Investigate opens an mcp-grafana session, offers its allowed tools to the
// model and runs the loop to a conclusion.
func (a *Agent) Investigate(ctx context.Context, w *alertmanager.Webhook) (Run, error) {
	session, err := a.Grafana.Open(ctx)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = session.Close() }()

	tools, err := session.Tools(ctx)
	if err != nil {
		return Run{}, err
	}
	set := NewSet()
	for _, t := range tools {
		if err := set.Add(t); err != nil {
			return Run{}, err
		}
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	run, err := a.Loop.Run(ctx, a.Limits, set, SystemPrompt(a.Context), UserPrompt(w, now()))
	if err != nil {
		return run, fmt.Errorf("investigation: %w", err)
	}
	return run, nil
}
