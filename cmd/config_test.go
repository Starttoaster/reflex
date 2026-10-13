package cmd

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestApplyEnv(t *testing.T) {
	t.Setenv("REFLEX_THRESHOLD", "0.9")
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
