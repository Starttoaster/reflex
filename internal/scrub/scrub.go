// Package scrub redacts sensitive values from text before it leaves the
// cluster for a third-party API.
package scrub

import (
	"fmt"
	"regexp"
)

// Redacted replaces every match of a scrub pattern.
const Redacted = "[redacted]"

// PrivateIPv4 matches RFC 1918 and loopback IPv4 addresses, with an optional
// port or CIDR suffix.
const PrivateIPv4 = `\b(?:10(?:\.\d{1,3}){3}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2}|192\.168(?:\.\d{1,3}){2}|127(?:\.\d{1,3}){3})(?::\d+|/\d{1,2})?\b`

// Scrubber redacts regex matches from strings and drops selected labels.
// The zero value redacts nothing.
type Scrubber struct {
	patterns   []*regexp.Regexp
	dropLabels map[string]bool
}

// New compiles patterns and returns a Scrubber that also drops the named labels.
func New(patterns []string, dropLabels []string) (*Scrubber, error) {
	s := &Scrubber{dropLabels: make(map[string]bool, len(dropLabels))}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile scrub pattern %q: %w", p, err)
		}
		s.patterns = append(s.patterns, re)
	}
	for _, l := range dropLabels {
		s.dropLabels[l] = true
	}
	return s, nil
}

// String redacts every pattern match in v.
func (s *Scrubber) String(v string) string {
	if s == nil {
		return v
	}
	for _, re := range s.patterns {
		v = re.ReplaceAllString(v, Redacted)
	}
	return v
}

// Labels returns a copy of labels without dropped keys and with values scrubbed.
func (s *Scrubber) Labels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if s != nil && s.dropLabels[k] {
			continue
		}
		out[k] = s.String(v)
	}
	return out
}

// Drops reports whether label k is removed entirely.
func (s *Scrubber) Drops(k string) bool {
	return s != nil && s.dropLabels[k]
}
