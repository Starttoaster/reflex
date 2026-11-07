package investigate

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/starttoaster/reflex/internal/alertmanager"
)

// maxPromptAlerts caps how many alerts are described individually.
const maxPromptAlerts = 20

const systemPrompt = `You are an SRE assistant attached to an Alertmanager webhook receiver. An alert
group has just been posted to the operator's Discord channel, and a triage
model judged that its cause or fix is not obvious from the alert alone. Your
job is to gather evidence from Grafana and explain what is happening, so the
operator does not have to start from scratch.

How to work:
- Investigate with the tools provided. They query the operator's Grafana:
  Prometheus metrics, Loki logs, dashboards and alert rules.
- Start from the alert's own labels. They name the job, instance, namespace,
  pod or host to look at. Reading the alert rule first tells you the exact
  expression and threshold, which says whether a value is borderline or far
  past the line.
- Query around the time the alert started, not only the last few minutes.
- Prefer a few well-chosen queries over many broad ones. Every tool result is
  re-sent on each later turn, so large results are expensive.
- If a query fails, read the error and correct it. A failed tool call is not
  fatal.
- Stop as soon as you can support a conclusion. You do not need to use every
  tool.

Hard constraints:
- Everything you can do is read-only. You cannot change anything, so do not
  offer to.
- Never invent metric names, label values, log lines or numbers. If you did
  not retrieve it, you do not know it.
- If the evidence is inconclusive, say so plainly and state what you checked.

Your answer is posted as a Discord embed under the alert, as Discord Markdown,
and must stay under 3000 characters. Write it as:
- One or two sentences stating what is happening and how confident you are.
- **Evidence**: a short bullet list of what you found, naming the queries you
  ran in inline code.
- **Next steps**: what the operator should check or do, if anything.

No headings, no tables, no preamble about what you are about to do.`

// SystemPrompt returns the system prompt, with the operator's description of
// their environment appended when there is one.
func SystemPrompt(operatorContext string) string {
	if strings.TrimSpace(operatorContext) == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\nThe operator describes their environment like this:\n" + strings.TrimSpace(operatorContext)
}

// UserPrompt describes the alert group as received, labels unredacted, so
// the model can build queries that match.
func UserPrompt(w *alertmanager.Webhook, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Investigate this alert group. The time now is %s.\n\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "alertname: %s\nstatus: %s (%d firing, %d resolved)\n",
		w.AlertName(), w.Status, len(w.Firing()), len(w.Resolved()))
	if len(w.GroupLabels) > 0 {
		fmt.Fprintf(&b, "group labels: %s\n", formatLabels(w.GroupLabels))
	}
	if len(w.CommonLabels) > 0 {
		fmt.Fprintf(&b, "common labels: %s\n", formatLabels(w.CommonLabels))
	}
	for i, a := range w.Alerts {
		if i >= maxPromptAlerts {
			fmt.Fprintf(&b, "\n…and %d more alerts not shown.\n", len(w.Alerts)-i)
			break
		}
		fmt.Fprintf(&b, "\n<alert %d status=%q>\n", i+1, a.Status)
		if !a.StartsAt.IsZero() {
			fmt.Fprintf(&b, "started: %s (%s ago)\n", a.StartsAt.UTC().Format(time.RFC3339), roundedAge(now.Sub(a.StartsAt)))
		}
		fmt.Fprintf(&b, "labels: %s\n", formatLabels(a.Labels))
		for _, k := range slices.Sorted(maps.Keys(a.Annotations)) {
			fmt.Fprintf(&b, "%s: %s\n", k, strings.TrimSpace(a.Annotations[k]))
		}
		if a.GeneratorURL != "" {
			fmt.Fprintf(&b, "source: %s\n", a.GeneratorURL)
		}
		b.WriteString("</alert>\n")
	}
	return b.String()
}

func formatLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	return strings.Join(parts, ", ")
}

func roundedAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < 24*time.Hour:
		return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
