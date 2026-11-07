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
// one Choice between "page now" and "informational", an optional Score for
// urgency that is only logged, and an optional Choice on whether the group
// warrants an automatic investigation. Any field left empty uses the default.
type Questions struct {
	// Context describes the environment to Jev, e.g. what matters and what
	// doesn't. It is added to the state as "context".
	Context     string              `yaml:"context"`
	Route       RouteQuestion       `yaml:"route"`
	Urgency     UrgencyQuestion     `yaml:"urgency"`
	Investigate InvestigateQuestion `yaml:"investigate"`
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

// InvestigateQuestion is the Choice that decides whether Claude investigates
// the group. It is only asked when investigations are configured.
type InvestigateQuestion struct {
	// Enabled defaults to true.
	Enabled         *bool  `yaml:"enabled"`
	Instructions    string `yaml:"instructions"`
	Investigate     string `yaml:"investigate"`
	SelfExplanatory string `yaml:"self_explanatory"`
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
		Investigate: InvestigateQuestion{
			Enabled: &enabled,
			Instructions: "Is it clear from this alert group alone what is wrong and what the operator should do " +
				"about it, or would looking at the system's metrics and logs first help them?",
			Investigate: "The cause or the fix is not obvious from the alert: it is vague, could have several " +
				"causes, or the right response depends on what else is happening in the system.",
			SelfExplanatory: "The alert says plainly what is wrong and the fix is obvious, or it is a brief, " +
				"routine or self-healing blip that needs no digging.",
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
	if q.Investigate.Enabled == nil {
		q.Investigate.Enabled = d.Investigate.Enabled
	}
	setDefault(&q.Investigate.Instructions, d.Investigate.Instructions)
	setDefault(&q.Investigate.Investigate, d.Investigate.Investigate)
	setDefault(&q.Investigate.SelfExplanatory, d.Investigate.SelfExplanatory)
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

// InvestigateEnabled reports whether the investigate Choice may be asked.
func (q Questions) InvestigateEnabled() bool {
	return q.Investigate.Enabled == nil || *q.Investigate.Enabled
}

// jevQuestions builds the request's question map. The investigate Choice is
// included only when investigate is true and the questions enable it.
func (q Questions) jevQuestions(investigate bool) map[string]jev.Question {
	out := map[string]jev.Question{
		routeQuestion: jev.Choice(q.Route.Instructions, map[string]string{
			optionPageNow: q.Route.PageNow,
			optionInfo:    q.Route.Informational,
		}),
	}
	if q.UrgencyEnabled() {
		out[urgencyQuestion] = jev.Score(q.Urgency.Instructions, q.Urgency.Levels)
	}
	if investigate && q.InvestigateEnabled() {
		out[investigateQuestion] = jev.Choice(q.Investigate.Instructions, map[string]string{
			optionInvestigate:     q.Investigate.Investigate,
			optionSelfExplanatory: q.Investigate.SelfExplanatory,
		})
	}
	return out
}
