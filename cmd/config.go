package cmd

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/starttoaster/reflex/internal/jev"
	"github.com/starttoaster/reflex/internal/router"
	"github.com/starttoaster/reflex/internal/scrub"
)

// Secrets are read from the environment only, so they never show up in a
// process listing.
const (
	envJevAPIKey      = "JEV_API_KEY"
	envUrgentWebhook  = "DISCORD_URGENT_WEBHOOK"
	envInfoWebhook    = "DISCORD_INFO_WEBHOOK"
	envScrubSeparator = "\n"
)

// routingOptions are the flags shared by serve and classify.
type routingOptions struct {
	threshold       float64
	minConfidence   float64
	jevTimeout      time.Duration
	jevEndpoint     string
	jevModel        string
	scrubPrivateIPs bool
	scrubPatterns   []string
	scrubLabels     []string
	logFormat       string
	logLevel        string
}

func (o *routingOptions) addFlags(fs *pflag.FlagSet) {
	fs.Float64Var(&o.threshold, "threshold", 0.8, "minimum P(page now) to route to the urgent channel")
	fs.Float64Var(&o.minConfidence, "min-confidence", 0.6, "minimum Jev confidence to trust its answer; below this, route by labels")
	fs.DurationVar(&o.jevTimeout, "jev-timeout", 1500*time.Millisecond, "time limit for the Jev request")
	fs.StringVar(&o.jevEndpoint, "jev-endpoint", jev.DefaultEndpoint, "Jev System One endpoint")
	fs.StringVar(&o.jevModel, "jev-model", jev.DefaultModel, "Jev model")
	fs.BoolVar(&o.scrubPrivateIPs, "scrub-private-ips", true, "redact private and loopback IPv4 addresses before calling Jev")
	fs.StringArrayVar(&o.scrubPatterns, "scrub-pattern", nil, "extra regex to redact before calling Jev (repeatable; env: one per line)")
	fs.StringSliceVar(&o.scrubLabels, "scrub-label", nil, "label to leave out of the Jev request (repeatable or comma-separated)")
}

// addLogFlags registers the logging flags, which only serve uses.
func (o *routingOptions) addLogFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.logFormat, "log-format", "json", "log format: json or text")
	fs.StringVar(&o.logLevel, "log-level", "info", "log level: debug, info, warn or error")
}

// envNames maps flags to the environment variables that set them.
var envNames = map[string]string{
	"listen":            "REFLEX_LISTEN",
	"threshold":         "REFLEX_THRESHOLD",
	"min-confidence":    "REFLEX_MIN_CONFIDENCE",
	"jev-timeout":       "REFLEX_JEV_TIMEOUT",
	"jev-endpoint":      "JEV_ENDPOINT",
	"jev-model":         "JEV_MODEL",
	"scrub-private-ips": "REFLEX_SCRUB_PRIVATE_IPS",
	"scrub-pattern":     "REFLEX_SCRUB_PATTERNS",
	"scrub-label":       "REFLEX_SCRUB_LABELS",
	"sticky-ttl":        "REFLEX_STICKY_TTL",
	"send-timeout":      "REFLEX_SEND_TIMEOUT",
	"log-format":        "REFLEX_LOG_FORMAT",
	"log-level":         "REFLEX_LOG_LEVEL",
}

// applyEnv sets every flag not given on the command line from its
// environment variable, so flags win over env and env wins over defaults.
func applyEnv(fs *pflag.FlagSet) error {
	var err error
	fs.VisitAll(func(f *pflag.Flag) {
		name, ok := envNames[f.Name]
		if !ok || f.Changed || err != nil {
			return
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			return
		}
		values := []string{v}
		if f.Value.Type() == "stringArray" {
			values = strings.Split(strings.TrimSpace(v), envScrubSeparator)
		}
		for _, val := range values {
			if setErr := fs.Set(f.Name, val); setErr != nil {
				err = fmt.Errorf("invalid %s: %w", name, setErr)
				return
			}
		}
	})
	return err
}

func (o *routingOptions) validate() error {
	if o.threshold < 0 || o.threshold > 1 {
		return fmt.Errorf("--threshold must be between 0 and 1, got %v", o.threshold)
	}
	if o.minConfidence < 0 || o.minConfidence > 1 {
		return fmt.Errorf("--min-confidence must be between 0 and 1, got %v", o.minConfidence)
	}
	if o.jevTimeout <= 0 {
		return fmt.Errorf("--jev-timeout must be positive")
	}
	return nil
}

func (o *routingOptions) logger() (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(o.logLevel)); err != nil {
		return nil, fmt.Errorf("--log-level: %w", err)
	}
	hopts := &slog.HandlerOptions{Level: level}
	switch o.logFormat {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, hopts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, hopts)), nil
	default:
		return nil, fmt.Errorf("--log-format must be json or text, got %q", o.logFormat)
	}
}

// newRouter builds a Router. Without an API key it routes by labels only.
func (o *routingOptions) newRouter(sticky *router.Sticky) (*router.Router, error) {
	patterns := o.scrubPatterns
	if o.scrubPrivateIPs {
		patterns = append([]string{scrub.PrivateIPv4}, patterns...)
	}
	scrubber, err := scrub.New(patterns, o.scrubLabels)
	if err != nil {
		return nil, err
	}
	r := &router.Router{
		Config: router.Config{
			Threshold:     o.threshold,
			MinConfidence: o.minConfidence,
			Timeout:       o.jevTimeout,
			Model:         o.jevModel,
		},
		Scrubber: scrubber,
		Sticky:   sticky,
	}
	if key := os.Getenv(envJevAPIKey); key != "" {
		r.Jev = &jev.Client{
			Endpoint:   o.jevEndpoint,
			APIKey:     key,
			Model:      o.jevModel,
			HTTPClient: &http.Client{},
		}
	}
	return r, nil
}

// describeEnv appends each flag's environment variable to its usage text.
func describeEnv(c *cobra.Command) {
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if name, ok := envNames[f.Name]; ok {
			f.Usage += " [$" + name + "]"
		}
	})
}
