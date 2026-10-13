// Package router decides which Discord channel an Alertmanager notification
// goes to. It asks Jev first and falls back to label routing whenever Jev
// can't give a confident answer in time.
package router

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/jev"
	"github.com/starttoaster/reflex/internal/scrub"
)

// Channel is a destination for a notification.
type Channel string

// Channels.
const (
	Urgent Channel = "urgent"
	Info   Channel = "info"
)

// Reason explains how a decision was made. Values are used as metric labels.
type Reason string

// Reasons.
const (
	ReasonJev                   Reason = "jev"
	ReasonFallbackTimeout       Reason = "fallback_timeout"
	ReasonFallbackError         Reason = "fallback_error"
	ReasonFallbackLowConfidence Reason = "fallback_low_confidence"
	ReasonFallbackNoKey         Reason = "fallback_no_key"
	ReasonSticky                Reason = "sticky"
	ReasonFallbackUnknownGroup  Reason = "fallback_unknown_group"
)

// Question ids and options sent to Jev.
const (
	routeQuestion   = "route"
	urgencyQuestion = "urgency"
	optionPageNow   = "page now"
	optionInfo      = "informational"
)

// Evaluator is the part of the Jev client the router needs.
type Evaluator interface {
	Evaluate(ctx context.Context, req jev.Request) (*jev.Response, error)
}

// Decision is the outcome of routing one notification.
type Decision struct {
	Channel Channel
	Reason  Reason
	// Severity is the label the fallback rule looked at.
	Severity string
	// Jev fields are set when Jev answered.
	PageProbability float64
	Confidence      float64
	Urgency         *float64
	Model           string
	JevLatency      time.Duration
	// JevCalled is true when a request was sent, whatever its outcome.
	JevCalled bool
	Err       error
	State     *State
	Request   *jev.Request
	Response  *jev.Response
}

// Footer is the short routing explanation shown on the Discord message.
func (d Decision) Footer() string {
	switch d.Reason {
	case ReasonJev:
		return fmt.Sprintf("routed by Jev (p=%.2f)", d.PageProbability)
	case ReasonSticky:
		return "resolved: same channel as firing"
	default:
		return fmt.Sprintf("fallback (%s): severity=%s", fallbackCause(d.Reason), orNone(d.Severity))
	}
}

func fallbackCause(r Reason) string {
	switch r {
	case ReasonFallbackTimeout:
		return "Jev timeout"
	case ReasonFallbackError:
		return "Jev error"
	case ReasonFallbackLowConfidence:
		return "low confidence"
	case ReasonFallbackNoKey:
		return "Jev disabled"
	case ReasonFallbackUnknownGroup:
		return "unknown group"
	default:
		return string(r)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Config holds the routing knobs.
type Config struct {
	// Threshold is the minimum P(page now) for the urgent channel.
	Threshold float64
	// MinConfidence is the minimum Jev confidence to trust its answer.
	MinConfidence float64
	// Timeout bounds the Jev call.
	Timeout time.Duration
	// Model is the Jev model; empty uses the client's default.
	Model string
}

// Router makes routing decisions. A nil Jev routes every notification by labels.
type Router struct {
	Config   Config
	Jev      Evaluator
	Scrubber *scrub.Scrubber
	Sticky   *Sticky
	Now      func() time.Time
}

// Route decides where w goes and remembers firing decisions so the
// matching resolved notification lands in the same channel.
func (r *Router) Route(ctx context.Context, w *alertmanager.Webhook) Decision {
	if w.Status == alertmanager.StatusResolved {
		return r.routeResolved(w)
	}
	d := r.classify(ctx, w)
	if r.Sticky != nil && w.GroupKey != "" {
		r.Sticky.Set(w.GroupKey, d.Channel)
	}
	return d
}

func (r *Router) routeResolved(w *alertmanager.Webhook) Decision {
	if r.Sticky != nil && w.GroupKey != "" {
		if ch, ok := r.Sticky.Get(w.GroupKey); ok {
			r.Sticky.Delete(w.GroupKey)
			return Decision{Channel: ch, Reason: ReasonSticky, Severity: w.Severity()}
		}
	}
	return fallback(w, ReasonFallbackUnknownGroup, nil)
}

// Classify asks Jev about a notification without touching sticky state.
// It is what the classify command uses for dry runs.
func (r *Router) Classify(ctx context.Context, w *alertmanager.Webhook) Decision {
	return r.classify(ctx, w)
}

func (r *Router) classify(ctx context.Context, w *alertmanager.Webhook) Decision {
	if r.Jev == nil {
		return fallback(w, ReasonFallbackNoKey, nil)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	state := BuildState(w, r.Scrubber, now())
	req := jev.Request{
		State:     state,
		Model:     r.Config.Model,
		Questions: questions(),
	}

	ctx, cancel := context.WithTimeout(ctx, r.Config.Timeout)
	defer cancel()
	start := time.Now()
	resp, err := r.Jev.Evaluate(ctx, req)
	latency := time.Since(start)

	var d Decision
	switch {
	case errors.Is(err, jev.ErrNoAPIKey):
		return fallback(w, ReasonFallbackNoKey, err)
	case errors.Is(err, context.DeadlineExceeded):
		d = fallback(w, ReasonFallbackTimeout, err)
	case err != nil:
		d = fallback(w, ReasonFallbackError, err)
	default:
		d = r.decide(w, resp)
	}
	d.JevCalled = true
	d.JevLatency = latency
	d.State = &state
	d.Request = &req
	d.Response = resp
	return d
}

func (r *Router) decide(w *alertmanager.Webhook, resp *jev.Response) Decision {
	ans, ok := resp.Answers[routeQuestion]
	if !ok || ans.Type != "choice" {
		return fallback(w, ReasonFallbackError, fmt.Errorf("response has no %q choice answer", routeQuestion))
	}
	p, ok := ans.Probabilities[optionPageNow]
	if !ok {
		return fallback(w, ReasonFallbackError, fmt.Errorf("answer has no %q probability", optionPageNow))
	}

	var d Decision
	if ans.Confidence < r.Config.MinConfidence {
		d = fallback(w, ReasonFallbackLowConfidence, nil)
	} else {
		d = Decision{Channel: Info, Reason: ReasonJev, Severity: w.Severity()}
		if p >= r.Config.Threshold {
			d.Channel = Urgent
		}
	}
	d.PageProbability = p
	d.Confidence = ans.Confidence
	d.Model = resp.Model
	if u, ok := resp.Answers[urgencyQuestion]; ok && u.Type == "score" {
		d.Urgency = &u.Score
	}
	return d
}

// LabelChannel is the deterministic rule: severity=critical is urgent,
// anything else is info.
func LabelChannel(severity string) Channel {
	if severity == "critical" {
		return Urgent
	}
	return Info
}

func fallback(w *alertmanager.Webhook, reason Reason, err error) Decision {
	sev := w.Severity()
	return Decision{Channel: LabelChannel(sev), Reason: reason, Severity: sev, Err: err}
}

func questions() map[string]jev.Question {
	return map[string]jev.Question{
		routeQuestion: jev.Choice(
			"This is a group of monitoring alerts for a small self-hosted infrastructure, "+
				"about to be sent to the on-call operator. Should it page them now, or can it wait "+
				"until they next check the informational channel?",
			map[string]string{
				optionPageNow: "A human needs to act right away: a service people rely on is down or badly " +
					"degraded, data is at risk, storage or capacity is about to run out, or there is a security problem.",
				optionInfo: "It can wait: a warning or early signal, a brief or self-healing blip, a non-essential " +
					"component, or something worth fixing eventually but not urgently.",
			},
		),
		urgencyQuestion: jev.Score(
			"How urgent is this alert group for the operator?",
			[]string{
				"No action needed",
				"Look at it this week",
				"Look at it today",
				"Look at it within the hour",
				"Drop everything now",
			},
		),
	}
}
