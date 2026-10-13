package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeQuestions(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "questions.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadQuestions(t *testing.T) {
	def := DefaultQuestions()
	tests := []struct {
		name    string
		yaml    string
		check   func(t *testing.T, q Questions)
		wantErr string
	}{
		{
			name: "empty file uses defaults",
			yaml: "",
			check: func(t *testing.T, q Questions) {
				if q.Route != def.Route || !q.UrgencyEnabled() || len(q.Urgency.Levels) != len(def.Urgency.Levels) {
					t.Errorf("got %+v, want defaults", q)
				}
			},
		},
		{
			name: "partial file keeps other defaults",
			yaml: "context: Backups matter most.\nroute:\n  page_now: Backups failing or a service is down.\n",
			check: func(t *testing.T, q Questions) {
				if q.Context != "Backups matter most." || q.Route.PageNow != "Backups failing or a service is down." {
					t.Errorf("overrides not applied: %+v", q)
				}
				if q.Route.Instructions != def.Route.Instructions || q.Route.Informational != def.Route.Informational {
					t.Errorf("defaults not kept: %+v", q.Route)
				}
			},
		},
		{
			name: "custom levels",
			yaml: "urgency:\n  levels: [low, high]\n",
			check: func(t *testing.T, q Questions) {
				if strings.Join(q.Urgency.Levels, ",") != "low,high" {
					t.Errorf("levels = %q", q.Urgency.Levels)
				}
			},
		},
		{
			name: "urgency disabled",
			yaml: "urgency:\n  enabled: false\n",
			check: func(t *testing.T, q Questions) {
				if q.UrgencyEnabled() {
					t.Error("urgency still enabled")
				}
				if _, ok := q.jevQuestions()[urgencyQuestion]; ok {
					t.Error("urgency question still asked")
				}
			},
		},
		{name: "unknown key", yaml: "route:\n  pagenow: typo\n", wantErr: "field pagenow not found"},
		{name: "one level", yaml: "urgency:\n  levels: [only]\n", wantErr: "2 to 10 entries"},
		{name: "too many levels", yaml: "urgency:\n  levels: [a, b, c, d, e, f, g, h, i, j, k]\n", wantErr: "2 to 10 entries"},
		{name: "empty level", yaml: "urgency:\n  levels: [a, '']\n", wantErr: "urgency.levels[1] is empty"},
		{name: "not yaml", yaml: "route: [\n", wantErr: "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := LoadQuestions(writeQuestions(t, tt.yaml))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, q)
		})
	}
}

func TestLoadQuestionsMissingFile(t *testing.T) {
	if _, err := LoadQuestions(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCustomQuestionsReachJev(t *testing.T) {
	f := &fakeJev{resp: routeAnswer(0.9, 0.9)}
	r := newTestRouter(f)
	r.Config.Questions = Questions{
		Context: "Media server outages can wait until evening.",
		Route:   RouteQuestion{Informational: "Media server issues."},
		Urgency: UrgencyQuestion{Enabled: new(false)},
	}
	r.Route(context.Background(), loadWebhook(t, "firing-warning.json"))

	if st := f.last.State.(State); st.Context != "Media server outages can wait until evening." {
		t.Errorf("state context = %q", st.Context)
	}
	opts := f.last.Questions[routeQuestion].Criteria.(map[string]string)
	if opts[optionInfo] != "Media server issues." || opts[optionPageNow] != DefaultQuestions().Route.PageNow {
		t.Errorf("route options = %v", opts)
	}
	if _, ok := f.last.Questions[urgencyQuestion]; ok {
		t.Error("urgency question asked while disabled")
	}
}
