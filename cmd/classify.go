package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/router"
)

func newClassifyCommand() *cobra.Command {
	var (
		opts routingOptions
		file string
	)
	c := &cobra.Command{
		Use:   "classify -f payload.json",
		Short: "Dry-run routing for a saved Alertmanager payload",
		Long: `Read an Alertmanager webhook payload, ask Jev about it and print the state sent,
Jev's response and the routing decision. Nothing is sent to Discord.

Use "-f -" to read from stdin. Resolved payloads are classified like firing
ones here; in serve they follow the channel their firing notification used.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			in := cmd.InOrStdin()
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				in = f
			}
			hook, err := alertmanager.Parse(in)
			if err != nil {
				return err
			}
			rt, err := opts.newRouter(nil)
			if err != nil {
				return err
			}
			return printDecision(cmd.OutOrStdout(), rt.Classify(cmd.Context(), hook), rt.Config.Questions)
		},
	}
	fs := c.Flags()
	fs.StringVarP(&file, "file", "f", "", "Alertmanager webhook payload (JSON), or - for stdin")
	_ = c.MarkFlagRequired("file")
	opts.addFlags(fs)
	describeEnv(c)
	return c
}

func printDecision(w io.Writer, d router.Decision, q router.Questions) error {
	section := func(title string, v any) error {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "== %s\n%s\n\n", title, b)
		return err
	}
	if d.State != nil {
		if err := section("State sent to Jev", d.State); err != nil {
			return err
		}
	}
	if d.Response != nil {
		if err := section("Jev response", d.Response); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(w, "== Decision\n")
	_, _ = fmt.Fprintf(w, "channel:       %s\n", d.Channel)
	_, _ = fmt.Fprintf(w, "reason:        %s\n", d.Reason)
	if d.Model != "" {
		_, _ = fmt.Fprintf(w, "p(page now):   %.3f\n", d.PageProbability)
		_, _ = fmt.Fprintf(w, "confidence:    %.3f\n", d.Confidence)
	}
	if d.Urgency != nil {
		levels := q.Urgency.Levels
		_, _ = fmt.Fprintf(w, "urgency:       %.2f (0 %q … %d %q)\n", *d.Urgency, levels[0], len(levels)-1, levels[len(levels)-1])
	}
	if d.JevCalled {
		_, _ = fmt.Fprintf(w, "jev latency:   %s\n", d.JevLatency.Round(time.Millisecond))
	}
	if d.Err != nil {
		_, _ = fmt.Fprintf(w, "jev error:     %v\n", d.Err)
	}
	_, _ = fmt.Fprintf(w, "label routing: %s (severity=%s)\n", router.LabelChannel(d.Severity), d.Severity)
	_, err := fmt.Fprintf(w, "footer:        %s\n", d.Footer())
	return err
}
