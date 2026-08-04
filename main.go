package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/icholy/digest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// -------------------- Config --------------------

type DeviceConfig struct {
	Name      string   `yaml:"name"`
	Address   string   `yaml:"address"`
	Username  string   `yaml:"username"`
	Password  string   `yaml:"password"`
	ID        int      `yaml:"id"`
	Endpoints []string `yaml:"endpoints"`
}

type Config struct {
	Devices []DeviceConfig `yaml:"devices"`
	Server  struct {
		ListenAddress            string `yaml:"listen_address"`
		RequestTimeoutSeconds    int    `yaml:"request_timeout_seconds"`
		RateLimitCooldownSeconds int    `yaml:"rate_limit_cooldown_seconds"`
	} `yaml:"server"`
	Metrics struct {
		EnableGoRuntime bool `yaml:"enable_go_runtime"`
	} `yaml:"metrics"`
}

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg Config
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	// Defaults
	if cfg.Server.ListenAddress == "" {
		cfg.Server.ListenAddress = ":9905"
	}
	if cfg.Server.RequestTimeoutSeconds <= 0 {
		cfg.Server.RequestTimeoutSeconds = 5
	}
	if cfg.Server.RateLimitCooldownSeconds <= 0 {
		cfg.Server.RateLimitCooldownSeconds = 600
	}

	if len(cfg.Devices) == 0 {
		return nil, errors.New("config: at least one device must be configured under 'devices'")
	}

	// Validate & apply per-device defaults
	seen := map[string]struct{}{}
	for i := range cfg.Devices {
		d := &cfg.Devices[i]
		if strings.TrimSpace(d.Name) == "" {
			return nil, fmt.Errorf("config: device at index %d has no 'name'", i)
		}
		if _, ok := seen[d.Name]; ok {
			return nil, fmt.Errorf("config: duplicate device name %q", d.Name)
		}
		seen[d.Name] = struct{}{}

		if d.Address == "" {
			return nil, fmt.Errorf("config: device %q: 'address' must not be empty", d.Name)
		}
		if d.Username == "" || d.Password == "" {
			return nil, fmt.Errorf("config: device %q: 'username'/'password' must not be empty", d.Name)
		}
		if len(d.Endpoints) == 0 {
			d.Endpoints = []string{fmt.Sprintf("rpc/EM.GetStatus?id=%d", d.ID)}
		}
	}

	return &cfg, nil
}

// -------------------- Collector --------------------

// errRateLimited is returned when a device responds with HTTP 429. Shelly
// firmware 2.0.0 has a bug where the digest-auth challenge path can push the
// device into a sticky "Too many requests" state in which it stops issuing
// auth challenges for several minutes. When we hit it we back off instead of
// hammering the device further.
var errRateLimited = errors.New("rate limited (HTTP 429 Too Many Requests)")

type ShellyCollector struct {
	device   DeviceConfig
	client   *http.Client
	cooldown time.Duration

	upDesc          *prometheus.Desc
	durationDesc    *prometheus.Desc
	valueDesc       *prometheus.Desc
	infoDesc        *prometheus.Desc
	rateLimitedDesc *prometheus.Desc

	mu            sync.Mutex
	cooldownUntil time.Time
}

func NewShellyCollector(device DeviceConfig, timeout, cooldown time.Duration) *ShellyCollector {
	// Dedicated underlying transport per device so the connection pool (and,
	// layered on top of it, the digest challenge cache) stays alive for this
	// device across scrapes instead of borrowing the shared http.DefaultTransport.
	base := &http.Transport{
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	}
	// Per-device digest transport (credentials differ per device). The cached
	// challenge lives on this transport, so keeping the collector alive means
	// only the first scrape does the unauthenticated -> 401 -> auth round trip;
	// subsequent scrapes send digest auth pre-emptively without a fresh 401,
	// which is exactly the challenge pattern that trips the firmware 2.0.0 bug.
	t := &digest.Transport{
		Username:  device.Username,
		Password:  device.Password,
		Transport: base,
	}
	httpClient := &http.Client{
		Transport: t,
		Timeout:   timeout,
	}

	return &ShellyCollector{
		device:   device,
		client:   httpClient,
		cooldown: cooldown,
		upDesc: prometheus.NewDesc(
			"shelly_em_up",
			"Whether the last scrape of all configured RPC endpoints was successful (1 = yes, 0 = no).",
			nil, prometheus.Labels{"device": device.Name},
		),
		durationDesc: prometheus.NewDesc(
			"shelly_em_scrape_duration_seconds",
			"Total duration of the scrape in seconds for this device.",
			nil, prometheus.Labels{"device": device.Name},
		),
		// Label 'key' includes endpoint prefix + JSON path
		valueDesc: prometheus.NewDesc(
			"shelly_em_value",
			"Numerical values exported from configured RPC endpoints; label 'key' contains endpoint prefix plus JSON path.",
			[]string{"key"}, prometheus.Labels{"device": device.Name},
		),
		infoDesc: prometheus.NewDesc(
			"shelly_em_info",
			"Constant info metric about the device (value = 1).",
			nil, prometheus.Labels{"device": device.Name, "address": device.Address},
		),
		rateLimitedDesc: prometheus.NewDesc(
			"shelly_em_rate_limited",
			"Whether the device is currently in a rate-limit cooldown after an HTTP 429 response (1 = yes, 0 = no).",
			nil, prometheus.Labels{"device": device.Name},
		),
	}
}

func (c *ShellyCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.upDesc
	ch <- c.durationDesc
	ch <- c.valueDesc
	ch <- c.infoDesc
	ch <- c.rateLimitedDesc
}

func (c *ShellyCollector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()

	// always expose info
	ch <- prometheus.MustNewConstMetric(c.infoDesc, prometheus.GaugeValue, 1)

	// If the device recently returned 429, stay in cooldown and do not touch it
	// again. Poking a rate-limited Shelly only prolongs the sticky 429 state.
	c.mu.Lock()
	cooldownUntil := c.cooldownUntil
	c.mu.Unlock()
	if now := time.Now(); now.Before(cooldownUntil) {
		log.Printf("device=%q in rate-limit cooldown, skipping scrape for %.0fs",
			c.device.Name, cooldownUntil.Sub(now).Seconds())
		ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, 0)
		ch <- prometheus.MustNewConstMetric(c.rateLimitedDesc, prometheus.GaugeValue, 1)
		ch <- prometheus.MustNewConstMetric(c.durationDesc, prometheus.GaugeValue, time.Since(start).Seconds())
		return
	}

	overallOK := true
	rateLimited := false
	flatAll := map[string]float64{}

	for _, ep := range c.device.Endpoints {
		prefix := endpointKeyPrefix(ep)
		values, err := c.fetchAndFlatten(ep)
		if err != nil {
			overallOK = false
			if errors.Is(err, errRateLimited) {
				rateLimited = true
				log.Printf("scrape error device=%q endpoint=%q: HTTP 429; backing off for %s",
					c.device.Name, ep, c.cooldown)
				// Stop hitting further endpoints on this device this round.
				break
			}
			log.Printf("scrape error device=%q endpoint=%q: %v", c.device.Name, ep, err)
			continue
		}
		for k, v := range values {
			flatAll[joinKey(prefix, k)] = v
		}
	}

	if rateLimited {
		c.mu.Lock()
		c.cooldownUntil = time.Now().Add(c.cooldown)
		c.mu.Unlock()
	}

	// deterministic order
	keys := make([]string, 0, len(flatAll))
	for k := range flatAll {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ch <- prometheus.MustNewConstMetric(c.valueDesc, prometheus.GaugeValue, flatAll[k], k)
	}

	if overallOK {
		ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, 1)
	} else {
		ch <- prometheus.MustNewConstMetric(c.upDesc, prometheus.GaugeValue, 0)
	}
	rl := 0.0
	if rateLimited {
		rl = 1
	}
	ch <- prometheus.MustNewConstMetric(c.rateLimitedDesc, prometheus.GaugeValue, rl)
	ch <- prometheus.MustNewConstMetric(c.durationDesc, prometheus.GaugeValue, time.Since(start).Seconds())
}

func (c *ShellyCollector) fetchAndFlatten(relativePath string) (map[string]float64, error) {
	base := strings.TrimRight(c.device.Address, "/")
	rel := strings.TrimLeft(relativePath, "/")
	url := fmt.Sprintf("%s/%s", base, rel)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%w: %s", errRateLimited, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var payload any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}

	flat := map[string]float64{}
	flattenJSON("", payload, flat)

	// Optional heuristic for "total active power"
	if v, ok := guessTotalActivePower(flat); ok {
		flat["total_active_power_watts"] = v
	}

	return sanitizeKeys(flat), nil
}

// -------------------- Flatten/Sanitize Helpers --------------------

func flattenJSON(prefix string, v any, out map[string]float64) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			key := joinKey(prefix, k)
			flattenJSON(key, vv, out)
		}
	case []any:
		for i, vv := range t {
			key := joinKey(prefix, strconv.Itoa(i))
			flattenJSON(key, vv, out)
		}
	case json.Number:
		if f, err := t.Float64(); err == nil {
			out[prefix] = f
		}
	case float64:
		out[prefix] = t
	case int:
		out[prefix] = float64(t)
	case int64:
		out[prefix] = float64(t)
	case uint64:
		out[prefix] = float64(t)
		// ignore other types
	}
}

func joinKey(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

var keyCleaner = regexp.MustCompile(`[^a-zA-Z0-9._:-]`)

func sanitizeKeys(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		safe := keyCleaner.ReplaceAllString(k, "_")
		out[safe] = v
	}
	return out
}

// guessTotalActivePower returns the first exact key match from a list of
// well-known candidate names for total active power.
func guessTotalActivePower(m map[string]float64) (float64, bool) {
	candidates := []string{
		"total_act_power", "total_active_power", "total_power", "active_power_total",
	}
	for _, c := range candidates {
		if v, ok := m[c]; ok {
			return v, true
		}
	}
	return 0, false
}

// e.g. "rpc/EM.GetStatus?id=0" -> "rpc.EM.GetStatus.id_0"
func endpointKeyPrefix(endpoint string) string {
	ep := strings.TrimSpace(endpoint)
	ep = strings.TrimLeft(ep, "/")
	ep = strings.ReplaceAll(ep, "/", ".")
	ep = strings.ReplaceAll(ep, "?", ".")
	ep = strings.ReplaceAll(ep, "&", ".")
	ep = strings.ReplaceAll(ep, "=", "_")
	ep = strings.ReplaceAll(ep, "__", "_")
	ep = keyCleaner.ReplaceAllString(ep, "_")
	return ep
}

// -------------------- HTTP / Exporter --------------------

// Exporter owns the long-lived per-device collectors. Building them once at
// startup (instead of per request) keeps each device's digest challenge cache
// and connection pool alive across scrapes, so the exporter no longer re-runs
// the unauthenticated -> 401 -> auth handshake on every single scrape.
type Exporter struct {
	cfg        *Config
	collectors map[string]*ShellyCollector
	order      []string
}

func NewExporter(cfg *Config) *Exporter {
	timeout := time.Duration(cfg.Server.RequestTimeoutSeconds) * time.Second
	cooldown := time.Duration(cfg.Server.RateLimitCooldownSeconds) * time.Second

	collectors := make(map[string]*ShellyCollector, len(cfg.Devices))
	order := make([]string, 0, len(cfg.Devices))
	for _, d := range cfg.Devices {
		collectors[d.Name] = NewShellyCollector(d, timeout, cooldown)
		order = append(order, d.Name)
	}
	return &Exporter{cfg: cfg, collectors: collectors, order: order}
}

// buildRegistry constructs a fresh registry wired to the long-lived collectors.
// If deviceName is non-empty, only that device is scraped.
func (e *Exporter) buildRegistry(deviceName string) (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()

	if deviceName != "" {
		col, ok := e.collectors[deviceName]
		if !ok {
			return nil, fmt.Errorf("unknown device %q (available: %s)", deviceName, strings.Join(e.order, ", "))
		}
		reg.MustRegister(col)
	} else {
		// no device param -> scrape all
		for _, name := range e.order {
			reg.MustRegister(e.collectors[name])
		}
	}

	if e.cfg.Metrics.EnableGoRuntime {
		reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
		reg.MustRegister(prometheus.NewGoCollector())
	}

	return reg, nil
}

func deviceNames(devs []DeviceConfig) []string {
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		names = append(names, d.Name)
	}
	return names
}

func (e *Exporter) metricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deviceName := r.URL.Query().Get("device")
		reg, err := e.buildRegistry(deviceName)
		if err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		// promhttp handler bound to this per-request registry
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(w, r)
	})
}

func healthzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	})
}

func landingPageHandler(cfg *Config) http.Handler {
	names := strings.Join(deviceNames(cfg.Devices), ", ")
	body := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en"><head><title>Shelly Exporter</title></head>
<body>
<h1>Shelly Exporter</h1>
<p><a href="/metrics">Metrics</a></p>
<p>Configured devices: %s</p>
</body></html>`, names)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	})
}

// runHealthCheck calls /healthz on the running instance and exits with 0 (ok) or 1 (fail).
// It reads the listen address from the config so the port is always in sync.
func runHealthCheck(configPath string) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health-check: failed to load config: %v\n", err)
		os.Exit(1)
	}

	_, port, err := net.SplitHostPort(cfg.Server.ListenAddress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health-check: invalid listen_address %q: %v\n", cfg.Server.ListenAddress, err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://localhost:" + port + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "health-check: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health-check: unexpected status %d\n", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}

// -------------------- main --------------------

func main() {
	configPath := flag.String("config", "config.yaml", "Path to the YAML configuration")
	healthCheck := flag.Bool("health-check", false, "Perform a health check against the running instance and exit")
	flag.Parse()

	if *healthCheck {
		runHealthCheck(*configPath)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	exporter := NewExporter(cfg)

	mux := http.NewServeMux()
	mux.Handle("/metrics", exporter.metricsHandler())
	mux.Handle("/healthz", healthzHandler())
	mux.Handle("/", landingPageHandler(cfg))

	srv := &http.Server{
		Addr:    cfg.Server.ListenAddress,
		Handler: mux,
	}

	log.Printf("starting Shelly exporter on %s | devices: [%s] | go_runtime_metrics=%v | rate_limit_cooldown=%ds",
		cfg.Server.ListenAddress, strings.Join(deviceNames(cfg.Devices), ", "),
		cfg.Metrics.EnableGoRuntime, cfg.Server.RateLimitCooldownSeconds)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}
	log.Println("stopped")
}
