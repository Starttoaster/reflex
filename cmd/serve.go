package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/starttoaster/reflex/internal/discord"
	"github.com/starttoaster/reflex/internal/investigate"
	"github.com/starttoaster/reflex/internal/metrics"
	"github.com/starttoaster/reflex/internal/router"
	"github.com/starttoaster/reflex/internal/server"
)

func newServeCommand() *cobra.Command {
	var (
		opts        routingOptions
		iopts       investigateOptions
		listen      string
		stickyTTL   time.Duration
		sendTimeout time.Duration
	)
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the webhook server",
		Long: `Run the HTTP server:

  POST /alerts   Alertmanager webhook receiver
  GET  /healthz  liveness and readiness
  GET  /metrics  Prometheus metrics

Environment: DISCORD_URGENT_WEBHOOK and DISCORD_INFO_WEBHOOK are required.
JEV_API_KEY is optional; without it every notification is routed by labels.
ANTHROPIC_API_KEY together with --grafana-mcp-url turns on Claude
investigations of the groups Jev judges not self-explanatory.
Each flag can also be set with the variable shown in its description.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			if err := iopts.validate(); err != nil {
				return err
			}
			log, err := opts.logger()
			if err != nil {
				return err
			}
			webhooks := map[router.Channel]string{
				router.Urgent: os.Getenv(envUrgentWebhook),
				router.Info:   os.Getenv(envInfoWebhook),
			}
			if webhooks[router.Urgent] == "" || webhooks[router.Info] == "" {
				return fmt.Errorf("%s and %s must both be set", envUrgentWebhook, envInfoWebhook)
			}
			rt, err := opts.newRouter(router.NewSticky(stickyTTL))
			if err != nil {
				return err
			}
			if rt.Jev == nil {
				log.Warn("JEV_API_KEY is not set; routing every notification by labels")
			}

			sender := &discord.Client{HTTPClient: &http.Client{}}
			srv := &server.Server{
				Router:      rt,
				Sender:      sender,
				Webhooks:    webhooks,
				Metrics:     metrics.New(),
				Log:         log,
				SendTimeout: sendTimeout,
			}
			var inv *investigate.Investigator
			agent := iopts.newAgent(rt.Config.Questions.Context, log)
			switch {
			case agent == nil:
				if iopts.grafanaMCPURL != "" || os.Getenv(envAnthropicAPIKey) != "" {
					log.Warn("investigations are off: set both --grafana-mcp-url and " + envAnthropicAPIKey)
				}
			case rt.Jev == nil:
				log.Warn("investigations are off: Jev decides which groups to investigate, and JEV_API_KEY is not set")
			case !rt.Config.Questions.InvestigateEnabled():
				log.Info("investigations are off: investigate.enabled is false in the questions file")
			default:
				rt.Config.Investigate = true
				inv = investigate.New(investigate.Config{
					Runner:      agent,
					Sender:      sender,
					Metrics:     srv.Metrics,
					Log:         log,
					Concurrency: iopts.concurrency,
					Timeout:     iopts.timeout,
					SendTimeout: sendTimeout,
					DedupeTTL:   stickyTTL,
				})
				srv.Investigator = inv
				log.Info("investigations on", "model", iopts.model, "effort", iopts.effort,
					"threshold", opts.investigateThreshold, "concurrency", iopts.concurrency)
			}
			httpSrv := &http.Server{
				Addr:              listen,
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      opts.jevTimeout + sendTimeout + 10*time.Second,
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errCh := make(chan error, 1)
			go func() {
				log.Info("listening", "addr", listen, "version", version, "threshold", opts.threshold,
					"min_confidence", opts.minConfidence, "jev_timeout", opts.jevTimeout.String())
				errCh <- httpSrv.ListenAndServe()
			}()

			select {
			case err := <-errCh:
				return err
			case <-ctx.Done():
			}
			log.Info("shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := httpSrv.Shutdown(shutdownCtx); err != nil {
				return err
			}
			if inv != nil {
				if err := inv.Close(shutdownCtx); err != nil {
					log.Warn("investigations still running at shutdown", "err", err)
				}
			}
			if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	fs := c.Flags()
	fs.StringVar(&listen, "listen", ":8080", "address to listen on")
	fs.DurationVar(&stickyTTL, "sticky-ttl", 7*24*time.Hour, "how long to remember a firing group's channel for its resolved notification")
	fs.DurationVar(&sendTimeout, "send-timeout", 10*time.Second, "time limit for each Discord delivery, including one rate-limit retry")
	opts.addFlags(fs)
	opts.addLogFlags(fs)
	iopts.addFlags(fs)
	iopts.addServeFlags(fs)
	describeEnv(c)
	return c
}
