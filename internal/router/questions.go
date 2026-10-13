package router

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"

	"github.com/starttoaster/reflex/internal/jev"
)

// Questions is the wording of what reflex asks Jev. The structure is fixed:
// one Choice between "page now" and "informational", plus an optional Score
// for urgency that is only logged. Any field left empty uses the default.
type Questions struct {
	// Context describes the environment to Jev, e.g. what matters and what
	// doesn't. It is added to the state as "context".
	Context string          `yaml:"context"`
	Route   RouteQuestion   `yaml:"route"`
	Urgency UrgencyQuestion `yaml:"urgency"`
}

// RouteQuestion is the Choice that decides the channel.
type RouteQuestion struct {
	Instructions  string `yaml:"instructions"`
	PageNow       string `yaml:"page_now"`
	Informational string `yaml:"informational"`
}

// UrgencyQuestion is the logged-only Score.
type UrgencyQuestion struct {
	// Enabled defaults to true.
	Enabled      *bool    `yaml:"enabled"`
	Instructions string   `yaml:"instructions"`
	Levels       []string `yaml:"levels"`
}

// Score limits from the Jev API.
const (
	minScoreLevels = 2
	maxScoreLevels = 10
)

// DefaultQuestions returns the built-in wording.
func DefaultQuestions() Questions {
	enabled := true
	return Questions{
		Route: RouteQuestion{
			Instructions: "This is a group of monitoring alerts about to be sent to the on-call operator. " +
				"Should it page them now, or can it wait until they next check the informational channel?",
			PageNow: "A human needs to act right away: a service people rely on is down or badly " +
				"degraded, data is at risk, storage or capacity is about to run out, or there is a security problem.",
			Informational: "It can wait: a warning or early signal, a brief or self-healing blip, a non-essential " +
				"component, or something worth fixing eventually but not urgently.",
		},
		Urgency: UrgencyQuestion{
			Enabled:      &enabled,
			Instructions: "How urgent is this alert group for the operator?",
			Levels: []string{
				"No action needed",
				"Look at it this week",
				"Look at it today",
				"Look at it within the hour",
				"Drop everything now",
			},
		},
	}
}

// LoadQuestions reads a YAML questions file. Unknown keys are an error so
// typos don't silently fall back to defaults.
func LoadQuestions(path string) (Questions, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Questions{}, err
	}
	var q Questions
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&q); err != nil && !errors.Is(err, io.EOF) {
		return Questions{}, fmt.Errorf("parse %s: %w", path, err)
	}
	q = q.WithDefaults()
	if err := q.Validate(); err != nil {
		return Questions{}, fmt.Errorf("%s: %w", path, err)
	}
	return q, nil
}

// WithDefaults fills every empty field from DefaultQuestions.
func (q Questions) WithDefaults() Questions {
	d := DefaultQuestions()
	setDefault(&q.Route.Instructions, d.Route.Instructions)
	setDefault(&q.Route.PageNow, d.Route.PageNow)
	setDefault(&q.Route.Informational, d.Route.Informational)
	if q.Urgency.Enabled == nil {
		q.Urgency.Enabled = d.Urgency.Enabled
	}
	setDefault(&q.Urgency.Instructions, d.Urgency.Instructions)
	if len(q.Urgency.Levels) == 0 {
		q.Urgency.Levels = d.Urgency.Levels
	}
	return q
}

func setDefault(s *string, def string) {
	if *s == "" {
		*s = def
	}
}

// Validate checks the urgency levels against the API's limits.
func (q Questions) Validate() error {
	if !q.UrgencyEnabled() {
		return nil
	}
	if n := len(q.Urgency.Levels); n < minScoreLevels || n > maxScoreLevels {
		return fmt.Errorf("urgency.levels must have %d to %d entries, got %d", minScoreLevels, maxScoreLevels, n)
	}
	for i, l := range q.Urgency.Levels {
		if l == "" {
			return fmt.Errorf("urgency.levels[%d] is empty", i)
		}
	}
	return nil
}

// UrgencyEnabled reports whether the urgency Score is asked.
func (q Questions) UrgencyEnabled() bool {
	return q.Urgency.Enabled == nil || *q.Urgency.Enabled
}

// jevQuestions builds the request's question map.
func (q Questions) jevQuestions() map[string]jev.Question {
	out := map[string]jev.Question{
		routeQuestion: jev.Choice(q.Route.Instructions, map[string]string{
			optionPageNow: q.Route.PageNow,
			optionInfo:    q.Route.Informational,
		}),
	}
	if q.UrgencyEnabled() {
		out[urgencyQuestion] = jev.Score(q.Urgency.Instructions, q.Urgency.Levels)
	}
	return out
}
