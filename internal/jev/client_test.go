package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEvaluate(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"model": "jev-1.13.0",
			"answers": {
				"route": {"type": "choice", "choice": "a", "probabilities": {"a": 0.9, "b": 0.1}, "confidence": 0.8},
				"level": {"type": "score", "score": 1.5, "probabilities": {"0": 0, "1": 0.5, "2": 0.5}, "confidence": 0.5}
			},
			"usage": {"input_tokens": 100, "output_tokens": 20}
		}`))
	}))
	defer srv.Close()

	c := &Client{Endpoint: srv.URL, APIKey: "test-key"}
	resp, err := c.Evaluate(context.Background(), Request{
		State: map[string]string{"k": "v"},
		Questions: map[string]Question{
			"route": Choice("Pick one", map[string]string{"a": "first", "b": "second"}),
			"level": Score("Rate it", []string{"low", "mid", "high"}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != DefaultModel {
		t.Errorf("model = %v, want %q", gotBody["model"], DefaultModel)
	}
	qs := gotBody["questions"].(map[string]any)
	if q := qs["route"].(map[string]any); q["type"] != "choice" || q["criteria"].(map[string]any)["a"] != "first" {
		t.Errorf("route question = %v", q)
	}
	if q := qs["level"].(map[string]any); q["type"] != "score" || len(q["criteria"].([]any)) != 3 {
		t.Errorf("level question = %v", q)
	}

	if resp.Model != "jev-1.13.0" || resp.Usage.InputTokens != 100 {
		t.Errorf("response = %+v", resp)
	}
	if a := resp.Answers["route"]; a.Choice != "a" || a.Probabilities["a"] != 0.9 || a.Confidence != 0.8 {
		t.Errorf("route answer = %+v", a)
	}
	if a := resp.Answers["level"]; a.Score != 1.5 {
		t.Errorf("level answer = %+v", a)
	}
}

func TestEvaluateErrors(t *testing.T) {
	t.Run("no key", func(t *testing.T) {
		_, err := (&Client{Endpoint: "http://unused.invalid"}).Evaluate(context.Background(), Request{})
		if !errors.Is(err, ErrNoAPIKey) {
			t.Fatalf("err = %v, want ErrNoAPIKey", err)
		}
	})

	for _, code := range []int{http.StatusUnauthorized, http.StatusUnprocessableEntity, http.StatusTooManyRequests, 529} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"detail":"nope"}`))
			}))
			defer srv.Close()
			_, err := (&Client{Endpoint: srv.URL, APIKey: "k"}).Evaluate(context.Background(), Request{})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != code || apiErr.Body != `{"detail":"nope"}` {
				t.Fatalf("err = %v, want APIError %d", err, code)
			}
		})
	}

	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}))
		defer srv.Close()
		if _, err := (&Client{Endpoint: srv.URL, APIKey: "k"}).Evaluate(context.Background(), Request{}); err == nil {
			t.Fatal("expected a decode error")
		}
	})
}
