package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/starttoaster/reflex/internal/alertmanager"
	"github.com/starttoaster/reflex/internal/investigate"
)

// Bounds on talking to mcp-grafana and the Anthropic API.
const (
	grafanaConnectTimeout = 15 * time.Second
	grafanaRequestTimeout = 60 * time.Second
	modelRequestTimeout   = 5 * time.Minute
	toolResultMaxBytes    = 16 << 10
	investigateMaxOutput  = 16000
)

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// investigateOptions are the flags for Claude investigations.
type investigateOptions struct {
	grafanaMCPURL string
	model         string
	effort        string
	timeout       time.Duration
	maxIterations int
	maxTokens     int64
	concurrency   int
	maxPerHour    int
}

func (o *investigateOptions) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.grafanaMCPURL, "grafana-mcp-url", "", "mcp-grafana streamable HTTP endpoint; investigations are off without it")
	fs.StringVar(&o.model, "investigate-model", "claude-opus-5-5", "Claude model for investigations")
	fs.StringVar(&o.effort, "investigate-effort", "medium", "effort for investigations: low, medium, high, xhigh or max")
	fs.DurationVar(&o.timeout, "investigate-timeout", 10*time.Minute, "time limit for one investigation")
	fs.IntVar(&o.maxIterations, "investigate-max-iterations", 15, "maximum model requests per investigation")
	fs.Int64Var(&o.maxTokens, "investigate-max-tokens", 300000, "maximum billable tokens per investigation")
}

// addServeFlags registers the flags only serve uses.
func (o *investigateOptions) addServeFlags(fs *pflag.FlagSet) {
	fs.IntVar(&o.concurrency, "investigate-concurrency", 2, "investigations that may run at once; more are skipped")
	fs.IntVar(&o.maxPerHour, "max-hourly-investigations", 10, "investigations that may start in any rolling hour; more are skipped (0 for no limit)")
}

func (o *investigateOptions) validate() error {
	if !slices.Contains(efforts, o.effort) {
		return fmt.Errorf("--investigate-effort must be one of %v, got %q", efforts, o.effort)
	}
	if o.maxPerHour < 0 {
		return errors.New("--max-hourly-investigations must not be negative")
	}
	if o.timeout <= 0 || o.maxIterations <= 0 || o.maxTokens <= 0 {
		return errors.New("--investigate-timeout, --investigate-max-iterations and --investigate-max-tokens must be positive")
	}
	return nil
}

// newAgent builds an investigation agent, or returns nil when
// ANTHROPIC_API_KEY or --grafana-mcp-url is missing.
func (o *investigateOptions) newAgent(operatorContext string, log *slog.Logger) *investigate.Agent {
	key := os.Getenv(envAnthropicAPIKey)
	if key == "" || o.grafanaMCPURL == "" {
		return nil
	}
	client := anthropic.NewClient(option.WithAPIKey(key), option.WithRequestTimeout(modelRequestTimeout))
	return &investigate.Agent{
		Loop: &investigate.Loop{Messages: &client.Beta.Messages, Log: log},
		Grafana: &investigate.Grafana{
			URL:            o.grafanaMCPURL,
			Version:        version,
			ConnectTimeout: grafanaConnectTimeout,
			RequestTimeout: grafanaRequestTimeout,
			Allowed:        investigate.DefaultAllowedTools,
			Log:            log,
		},
		Limits: investigate.Limits{
			Model:              o.model,
			Effort:             o.effort,
			MaxIterations:      o.maxIterations,
			MaxTotalTokens:     o.maxTokens,
			MaxOutputTokens:    investigateMaxOutput,
			ToolResultMaxBytes: toolResultMaxBytes,
		},
		Context: operatorContext,
	}
}

func newInvestigateCommand() *cobra.Command {
	var (
		opts      investigateOptions
		questions routingOptions
		file      string
		verbose   bool
	)
	c := &cobra.Command{
		Use:   "investigate -f payload.json",
		Short: "Run a Claude investigation of a saved Alertmanager payload",
		Long: `Read an Alertmanager webhook payload, investigate it with Claude against
mcp-grafana and print the result. Jev is not asked and nothing is sent to
Discord. This spends Anthropic tokens.

Environment: ANTHROPIC_API_KEY is required. Use "-f -" to read from stdin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			if opts.grafanaMCPURL == "" || os.Getenv(envAnthropicAPIKey) == "" {
				return fmt.Errorf("--grafana-mcp-url and %s must both be set", envAnthropicAPIKey)
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
			q, err := questions.loadQuestions()
			if err != nil {
				return err
			}
			level := slog.LevelWarn
			if verbose {
				level = slog.LevelDebug
			}
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: level}))
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			run, err := opts.newAgent(q.Context, log).Investigate(ctx, hook)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "%s\n\n-- %s\n", run.Summary, investigate.Footer(run))
			return nil
		},
	}
	fs := c.Flags()
	fs.StringVarP(&file, "file", "f", "", "Alertmanager webhook payload (JSON), or - for stdin")
	_ = c.MarkFlagRequired("file")
	fs.BoolVarP(&verbose, "verbose", "v", false, "log each tool call to stderr")
	fs.StringVar(&questions.questionsFile, "questions-file", "", "YAML questions file; its context is passed to Claude")
	opts.addFlags(fs)
	describeEnv(c)
	return c
}
