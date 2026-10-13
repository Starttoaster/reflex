// Package discord formats Alertmanager notifications as Discord embeds and
// posts them to a webhook.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/starttoaster/reflex/internal/alertmanager"
)

// Discord embed limits, in characters.
// See https://discord.com/developers/docs/resources/message#embed-object-embed-limits.
const (
	MaxTitle       = 256
	MaxDescription = 4096
	MaxFooter      = 2048
)

// Embed colours, matching Alertmanager's built-in Discord integration.
const (
	colorFiring   = 0x992D22
	colorResolved = 0x2ECC71
)

// Message is a Discord webhook execute body.
type Message struct {
	Embeds          []Embed         `json:"embeds"`
	AllowedMentions AllowedMentions `json:"allowed_mentions"`
}

// AllowedMentions controls which mentions in the message ping anyone.
type AllowedMentions struct {
	Parse []string `json:"parse"`
}

// Embed is a Discord rich embed.
type Embed struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Color       int     `json:"color"`
	Footer      *Footer `json:"footer,omitempty"`
}

// Footer is the small text under an embed.
type Footer struct {
	Text string `json:"text"`
}

// Format builds the message for a notification, with footer as the routing
// reason. Alert text never triggers mentions.
func Format(w *alertmanager.Webhook, footer string) Message {
	firing := w.Firing()
	title := "[RESOLVED] " + w.AlertName()
	color := colorResolved
	if w.Status == alertmanager.StatusFiring {
		title = fmt.Sprintf("[FIRING:%d] %s", len(firing), w.AlertName())
		color = colorFiring
	}

	// Firing alerts are separated by blank lines; resolved ones follow as a
	// compact list, one per line.
	var pieces []piece
	for _, a := range firing {
		pieces = append(pieces, piece{sep: "\n\n", text: alertBlock(a, false)})
	}
	for i, a := range w.Resolved() {
		sep := "\n"
		if i == 0 {
			sep = "\n\n"
		}
		pieces = append(pieces, piece{sep: sep, text: alertBlock(a, true)})
	}

	e := Embed{
		Title:       truncate(title, MaxTitle),
		Description: joinWithin(pieces, MaxDescription),
		Color:       color,
	}
	if footer != "" {
		e.Footer = &Footer{Text: truncate(footer, MaxFooter)}
	}
	return Message{Embeds: []Embed{e}, AllowedMentions: AllowedMentions{Parse: []string{}}}
}

// alertBlock renders one alert the way talos-home's Alertmanager Discord
// template does: a firing alert is a red marker, bold title and its
// description; a resolved alert is a single struck-through title.
func alertBlock(a alertmanager.Alert, resolved bool) string {
	title := a.Annotations["title"]
	if title == "" {
		title = a.Labels["alertname"]
		if _, host := a.Host(); host != "" {
			title += " on " + host
		}
	}
	if resolved {
		return "✅ ~~" + title + "~~ (resolved)"
	}
	desc := a.Annotations["description"]
	if desc == "" {
		desc = a.Annotations["summary"]
	}
	if desc == "" {
		return "🔴 **" + title + "**"
	}
	return "🔴 **" + title + "**\n" + desc
}

// piece is a block of the description and the separator written before it.
type piece struct {
	sep, text string
}

// joinWithin joins pieces, dropping trailing ones that don't fit within
// limit characters and noting how many were left out.
func joinWithin(pieces []piece, limit int) string {
	var b strings.Builder
	used := 0
	for i, p := range pieces {
		text := p.text
		if i > 0 {
			text = p.sep + p.text
		}
		size := utf8.RuneCountInString(text)
		if used+size+utf8.RuneCountInString(moreNote(len(pieces)-i-1)) > limit {
			more := moreNote(len(pieces) - i)
			if i == 0 {
				// A single oversized first block: cut it rather than send nothing.
				more = moreNote(len(pieces) - 1)
				return truncate(p.text, limit-utf8.RuneCountInString(more)) + more
			}
			return b.String() + more
		}
		b.WriteString(text)
		used += size
	}
	return b.String()
}

func moreNote(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\n\n…and %d more", n)
}

// truncate shortens s to at most limit characters, ending with an ellipsis
// when cut.
func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:limit-1]), " \n") + "…"
}

// Client posts messages to Discord webhooks.
type Client struct {
	HTTPClient *http.Client
	// MaxRetryAfter caps how long a rate-limited send waits before its single retry.
	MaxRetryAfter time.Duration
}

// Send posts msg to webhookURL, retrying once if Discord rate-limits it.
func (c *Client) Send(ctx context.Context, webhookURL string, msg Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("discord: encode message: %w", err)
	}
	for attempt := 0; ; attempt++ {
		retryAfter, err := c.post(ctx, webhookURL, body)
		if err == nil {
			return nil
		}
		if retryAfter <= 0 || attempt > 0 {
			return err
		}
		maxWait := c.MaxRetryAfter
		if maxWait <= 0 {
			maxWait = 5 * time.Second
		}
		select {
		case <-time.After(min(retryAfter, maxWait)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// post sends one request. On a 429 it returns how long Discord asked to wait.
func (c *Client) post(ctx context.Context, webhookURL string, body []byte) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("discord: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Don't wrap the *url.Error: its message contains the webhook URL,
		// which is a secret.
		return 0, fmt.Errorf("discord: send failed: %w", unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	err = fmt.Errorf("discord: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	if resp.StatusCode == http.StatusTooManyRequests {
		var rl struct {
			RetryAfter float64 `json:"retry_after"`
		}
		if json.Unmarshal(msg, &rl) == nil && rl.RetryAfter > 0 {
			return time.Duration(rl.RetryAfter * float64(time.Second)), err
		}
		return time.Second, err
	}
	return 0, err
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
