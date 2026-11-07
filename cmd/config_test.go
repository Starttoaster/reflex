package cmd

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestApplyEnv(t *testing.T) {
	t.Setenv("REFLEX_THRESHOLD", "0.9")
	t.Setenv("REFLEX_INVESTIGATE_THRESHOLD", "0.55")
	t.Setenv("REFLEX_JEV_TIMEOUT", "750ms")
	t.Setenv("REFLEX_MIN_CONFIDENCE", "0.3")
	t.Setenv("REFLEX_SCRUB_PATTERNS", "foo\\d+\nbar")
	t.Setenv("REFLEX_SCRUB_LABELS", "instance,pod")

	var o routingOptions
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.addFlags(fs)
	// A flag on the command line beats the environment.
	if err := fs.Parse([]string{"--min-confidence=0.7"}); err != nil {
		t.Fatal(err)
	}
	if err := applyEnv(fs); err != nil {
		t.Fatal(err)
	}

	if o.threshold != 0.9 || o.jevTimeout != 750*time.Millisecond {
		t.Errorf("env not applied: threshold=%v timeout=%v", o.threshold, o.jevTimeout)
	}
	if o.investigateThreshold != 0.55 {
		t.Errorf("investigateThreshold = %v", o.investigateThreshold)
	}
	if o.minConfidence != 0.7 {
		t.Errorf("minConfidence = %v, want the flag's 0.7", o.minConfidence)
	}
	if len(o.scrubPatterns) != 2 || o.scrubPatterns[0] != `foo\d+` || o.scrubPatterns[1] != "bar" {
		t.Errorf("scrubPatterns = %q", o.scrubPatterns)
	}
	if len(o.scrubLabels) != 2 || o.scrubLabels[1] != "pod" {
		t.Errorf("scrubLabels = %q", o.scrubLabels)
	}
	if !o.scrubPrivateIPs {
		t.Error("scrubPrivateIPs should default to true")
	}
}

func TestApplyEnvInvestigate(t *testing.T) {
	t.Setenv("REFLEX_INVESTIGATE_MODEL", "claude-sonnet-5-5")
	t.Setenv("REFLEX_INVESTIGATE_EFFORT", "high")
	t.Setenv("REFLEX_GRAFANA_MCP_URL", "http://mcp-grafana:8000/mcp")
	t.Setenv("REFLEX_INVESTIGATE_CONCURRENCY", "4")
	t.Setenv("REFLEX_MAX_HOURLY_INVESTIGATIONS", "3")

	var o investigateOptions
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.addFlags(fs)
	o.addServeFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := applyEnv(fs); err != nil {
		t.Fatal(err)
	}
	if o.model != "claude-sonnet-5-5" || o.effort != "high" || o.grafanaMCPURL != "http://mcp-grafana:8000/mcp" ||
		o.concurrency != 4 || o.maxPerHour != 3 {
		t.Errorf("env not applied: %+v", o)
	}
	if err := o.validate(); err != nil {
		t.Error(err)
	}
}

func TestApplyEnvInvalid(t *testing.T) {
	t.Setenv("REFLEX_THRESHOLD", "high")
	var o routingOptions
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.addFlags(fs)
	if err := applyEnv(fs); err == nil {
		t.Fatal("expected an error for a non-numeric threshold")
	}
}

func TestValidate(t *testing.T) {
	for _, o := range []routingOptions{
		{threshold: 1.5, jevTimeout: time.Second},
		{threshold: 0.8, minConfidence: -1, jevTimeout: time.Second},
		{threshold: 0.8, jevTimeout: 0},
	} {
		if err := o.validate(); err == nil {
			t.Errorf("validate(%+v) = nil, want an error", o)
		}
	}
}
