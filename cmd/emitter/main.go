package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	statsd "github.com/DataDog/datadog-go/v5/statsd"
)

type config struct {
	host       string
	port       int
	iterations int
	interval   time.Duration
	namespace  string
	tags       []string
}

func main() {
	log.SetFlags(0)
	cfg := parseFlags()

	addr := fmt.Sprintf("%s:%d", cfg.host, cfg.port)
	client, err := statsd.New(addr,
		statsd.WithNamespace(cfg.namespace),
		statsd.WithTags(cfg.tags),
		statsd.WithMaxMessagesPerPayload(32),
	)
	if err != nil {
		log.Fatalf("failed to create statsd client (%s): %v", addr, err)
	}
	defer client.Close()

	log.Printf("dogstatsd emitter connected to %s (iterations=%d interval=%s)", addr, cfg.iterations, cfg.interval)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rand.Seed(time.Now().UnixNano())

	if err := runEmitter(ctx, client, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("emission failed: %v", err)
	}

	log.Println("emission complete")
}

func parseFlags() config {
	defaultTags := "service:edge-scanner,product:fastscan,priority:medium,region:us-east-1"

	host := flag.String("host", envOr("DOGSTATSD_HOST", "127.0.0.1"), "dogstatsd host")
	port := flag.Int("port", envOrInt("DOGSTATSD_PORT", 8125), "dogstatsd UDP port")
	iterations := flag.Int("iterations", envOrInt("EMIT_ITERATIONS", 120), "number of emission cycles (0=continuous)")
	interval := flag.Duration("interval", envOrDuration("EMIT_INTERVAL", 5*time.Second), "delay between emission cycles")
	namespace := flag.String("namespace", envOr("EMIT_NAMESPACE", "edge_nodes."), "metric namespace prefix")
	tags := flag.String("tags", envOr("EMIT_TAGS", defaultTags), "comma-separated global tags")

	flag.Parse()

	return config{
		host:       *host,
		port:       *port,
		iterations: *iterations,
		interval:   *interval,
		namespace:  *namespace,
		tags:       parseTags(*tags),
	}
}

func runEmitter(ctx context.Context, client *statsd.Client, cfg config) error {
	iterations := cfg.iterations
	for count := 0; iterations == 0 || count < iterations; count++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := emitAllMetrics(client, cfg.tags); err != nil {
			return err
		}

		if iterations > 0 {
			log.Printf("emitted metric batch %d/%d", count+1, iterations)
		} else {
			log.Printf("emitted metric batch %d", count+1)
		}

		if iterations != 0 && count+1 >= iterations {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.interval):
		}
	}
	return nil
}

func emitAllMetrics(client *statsd.Client, baseTags []string) error {
	var errs []error

	enqueueTags := extendTags(baseTags, "priority:high", "event:enqueue")
	errs = appendErr(errs, client.Incr("core_job_events_total", enqueueTags, 1))

	completeTags := extendTags(baseTags, "priority:medium", "event:complete")
	errs = appendErr(errs, client.Incr("core_job_events_total", completeTags, 1))

	dispatchTags := extendTags(baseTags, "priority:medium")
	errs = appendErr(errs, client.Incr("core_jobs_dispatched_total", dispatchTags, 1))

	emptyPollTags := extendTags(baseTags, "priority:low")
	errs = appendErr(errs, client.Incr("core_sqs_polls_empty_total", emptyPollTags, 1))

	gaugeTags := extendTags(baseTags, "priority:high")
	depth := 10 + rand.Intn(25)
	errs = appendErr(errs, client.Gauge("core_queue_depth", float64(depth), gaugeTags, 1))

	pollStartTags := extendTags(baseTags, "step:poll", "outcome:started")
	errs = appendErr(errs, client.Incr("scanner_jobs_started_total", pollStartTags, 1))

	stepSuccessTags := extendTags(baseTags, "step:dispatch", "outcome:success")
	errs = appendErr(errs, client.Incr("scanner_chain_step_total", stepSuccessTags, 1))

	stepFailureTags := extendTags(baseTags, "step:upload", "outcome:failure", "error_class:s3_timeout")
	errs = appendErr(errs, client.Incr("scanner_chain_step_total", stepFailureTags, 1))

	artifactSuccessTags := extendTags(baseTags, "step:upload")
	errs = appendErr(errs, client.Incr("scanner_artifacts_uploaded_total", artifactSuccessTags, 1))

	artifactFailureTags := extendTags(baseTags, "step:upload", "error_class:s3_timeout")
	errs = appendErr(errs, client.Incr("scanner_artifacts_upload_failed_total", artifactFailureTags, 1))

	resultUploadTags := extendTags(baseTags, "priority:high")
	errs = appendErr(errs, client.Incr("core_result_uploads_reported_total", resultUploadTags, 1))

	dlqTags := extendTags(baseTags, "priority:high")
	errs = appendErr(errs, client.Incr("core_dead_letter_messages_total", dlqTags, 1))

	webhookSuccessTags := extendTags(baseTags, "product:edge-core", "outcome:success")
	errs = appendErr(errs, client.Incr("core_webhook_delivery_total", webhookSuccessTags, 1))

	webhookErrorTags := extendTags(baseTags, "product:edge-core", "outcome:error", "error_class:http_500")
	errs = appendErr(errs, client.Incr("core_webhook_delivery_total", webhookErrorTags, 1))

	runtime := sampleDuration(25*time.Second, 60*time.Second)
	runtimeTags := extendTags(baseTags, "product:fastscan")
	errs = appendErr(errs, client.Distribution("scanner_job_runtime_seconds", runtime.Seconds(), runtimeTags, 1))

	stepDuration := sampleDuration(100*time.Millisecond, 2*time.Second)
	stepDurationTags := extendTags(baseTags, "step:dispatch")
	errs = appendErr(errs, client.Histogram("scanner_chain_step_duration_seconds", stepDuration.Seconds(), stepDurationTags, 1))

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func sampleDuration(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	delta := rand.Float64()
	return min + time.Duration(delta*float64(max-min))
}

func extendTags(base []string, additions ...string) []string {
	if len(additions) == 0 {
		return append([]string{}, base...)
	}
	out := make([]string, len(base), len(base)+len(additions))
	copy(out, base)
	return append(out, additions...)
}

func parseTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func appendErr(list []error, err error) []error {
	if err != nil {
		list = append(list, err)
	}
	return list
}

func envOr(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return val
	}
	return fallback
}

func envOrInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return parsed
		}
	}
	return fallback
}

func envOrDuration(key string, fallback time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := time.ParseDuration(strings.TrimSpace(val)); err == nil {
			return parsed
		}
	}
	return fallback
}
