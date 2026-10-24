package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Config holds every tunable setting for the server.
type Config struct {
	Port                   int    `json:"port"`
	CounterFile            string `json:"counter_file"`
	BackupCount            int64  `json:"backup_count"`
	PersistIntervalS       int64  `json:"persist_interval_s"`
	CheckIntervalS         int64  `json:"check_interval_s"`
	RateLimitWindowS       int64  `json:"rate_limit_window_s"`
	RateLimitSweepS        int64  `json:"rate_limit_sweep_s"`
	MinIncrementIntervalMs int64  `json:"min_increment_interval_ms"`
	TurnstileSiteKey       string `json:"turnstile_site_key"`
	TurnstileSecret        string `json:"turnstile_secret"`
	TurnstileTimeoutS      int64  `json:"turnstile_timeout_s"`
}

// Default values used when config.json doesn't exist or is invalid.
func DefaultConfig() Config {
	return Config{
		Port:                   1312,
		CounterFile:            "counter.txt",
		BackupCount:            0,     // disabled
		PersistIntervalS:       900,   // 15 min
		CheckIntervalS:         60,    // 1 min
		RateLimitWindowS:       21600, // 6h
		RateLimitSweepS:        300,   // 5 min
		MinIncrementIntervalMs: 0,     // disabled
		TurnstileTimeoutS:      5,     // 5s
	}
}

// LoadConfig reads a JSON config file and falls back to defaults on any error.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := WriteConfig(path, cfg); err != nil {
				fmt.Println(logError.Render("could not create default config:", err.Error()))
			}
		} else {
			fmt.Println(logWarning.Render("could not read config, using defaults:", err.Error()))
		}
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Println(logWarning.Render("invalid config, using defaults:", err.Error()))
		return cfg, nil
	}
	if cfg.PersistIntervalS <= 0 {
		cfg.PersistIntervalS = DefaultConfig().PersistIntervalS
	}
	if cfg.CheckIntervalS <= 0 {
		cfg.CheckIntervalS = DefaultConfig().CheckIntervalS
	}
	if cfg.RateLimitWindowS <= 0 {
		cfg.RateLimitWindowS = DefaultConfig().RateLimitWindowS
	}
	if cfg.RateLimitSweepS <= 0 {
		cfg.RateLimitSweepS = DefaultConfig().RateLimitSweepS
	}
	if cfg.MinIncrementIntervalMs < 0 {
		cfg.MinIncrementIntervalMs = 0
	}
	if cfg.BackupCount < 0 {
		cfg.BackupCount = 0
	}
	if cfg.TurnstileTimeoutS <= 0 {
		cfg.TurnstileTimeoutS = DefaultConfig().TurnstileTimeoutS
	}
	return cfg, nil
}

// WriteConfig serializes the current config to disk.
func WriteConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// baseDir is the directory the running binary lives in. Relative config and
// data paths are anchored here so the server reads and writes the same files
// no matter which directory it was launched from.
func baseDir() string {
	exe, err := os.Executable()
	if err != nil {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			return wd
		}
		return "."
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// resolvePath anchors a relative path to baseDir. Absolute paths are returned
// unchanged.
func resolvePath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir(), path)
}

// logStamp renders the white bracketed timestamp that prefixes log lines.
func logStamp() string {
	return logTimestamp.Render("[" + time.Now().Format("2006-01-02 15:04:05") + "]")
}

// TurnstileResponse represents the JSON from Cloudflare's verification API.
type TurnstileResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error_codes"`
	Score      *float64 `json:"score"`
	Action     string   `json:"action,omitempty"`
}

// verifyTurnstile checks a token against Cloudflare. Returns false if turnstile
// is not configured or verification fails.
// realIP extracts the visitor's real IP from headers (for reverse proxy support),
// falling back to RemoteAddr. Checks CF-Connecting-IP first, then X-Forwarded-For.
func realIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		// Take the first (client) IP in the chain
		for i := 0; i < len(ip); i++ {
			if ip[i] == ',' {
				return ip[:i]
			}
		}
		return ip
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// turnstileTimeout is how long the server waits for Cloudflare before treating
// verification as inconclusive. On a timeout the request is allowed through, so
// this is the leniency window. It never returns zero: a zero client timeout
// means "wait forever", which would hang the request instead of failing open.
func turnstileTimeout(cfg Config) time.Duration {
	if cfg.TurnstileTimeoutS <= 0 {
		return time.Duration(DefaultConfig().TurnstileTimeoutS) * time.Second
	}
	return time.Duration(cfg.TurnstileTimeoutS) * time.Second
}

// serverWriteTimeout leaves enough room for the slowest handler, which is a
// request that has to wait out the outbound Turnstile verification before it
// can answer.
func serverWriteTimeout(cfg Config) time.Duration {
	const base = 10 * time.Second
	if padded := turnstileTimeout(cfg) + 5*time.Second; padded > base {
		return padded
	}
	return base
}

func verifyTurnstile(token, remoteIP string) bool {
	cfg := currentConfig()
	if cfg.TurnstileSecret == "" || cfg.TurnstileSiteKey == "" {
		return true // not configured → allow
	}

	payload := strings.NewReader(fmt.Sprintf(
		`{"secret":"%s","response":"%s","remoteip":"%s"}`,
		cfg.TurnstileSecret,
		token,
		remoteIP,
	))

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"https://challenges.cloudflare.com/turnstile/v0/siteverify",
		payload,
	)
	if err != nil {
		fmt.Println(logError.Render("turnstile req error:", err.Error()))
		return true
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: turnstileTimeout(cfg)}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(logWarning.Render("turnstile verification error:", err.Error()))
		return true // network failure → allow (don't break site)
	}
	defer resp.Body.Close()

	var tr TurnstileResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		fmt.Println(logError.Render("turnstile decode error:", err.Error()))
		return true
	}
	if !tr.Success {
		fmt.Println(logWarning.Render(fmt.Sprintf("turnstile failed: %v", tr.ErrorCodes)))
	}
	return tr.Success
}

// Version is the current release version.
const Version = "1"

//go:embed countrr.js
var visitorJS []byte

var (
	configMu sync.RWMutex
	config   Config

	visitors  atomic.Int64
	lastSaved atomic.Int64
	lastCheck atomic.Int64

	turnstileFailures atomic.Int64
	throttled         atomic.Int64

	throttleMu    sync.Mutex
	lastIncrement time.Time

	hitsMu  sync.Mutex
	lastHit map[string]time.Time
)

// currentConfig returns a snapshot of the active configuration. The config can
// be swapped at runtime when the setup wizard is reopened, so handlers read it
// through here instead of touching the global directly.
func currentConfig() Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return config
}

func setConfig(cfg Config) {
	configMu.Lock()
	config = cfg
	configMu.Unlock()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "X-Turnstile-Token, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	flagCfg := flag.Bool("config", false, "run setup wizard regardless of existing config")
	flag.Parse()

	configPath := resolvePath("config.json")

	shouldSetup := *flagCfg

	if !shouldSetup {
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			shouldSetup = true
		}
	}

	if shouldSetup {
		fmt.Println(logInfo.Render("countrr: no valid config found - launching setup wizard..."))
		existing := DefaultConfig()
		if _, err := os.Stat(configPath); err == nil {
			if loaded, err := LoadConfig(configPath); err == nil {
				existing = loaded
			}
		}
		setConfig(InitSetup(existing))
		fmt.Println(logSuccess.Render("configuration set."))
	} else {
		cfg, err := LoadConfig(configPath)
		if err != nil {
			fmt.Println(logWarning.Render("using default configuration:", err.Error()))
		}
		setConfig(cfg)
	}

	if err := loadCounter(); err != nil {
		fmt.Println(logWarning.Render("failed to load counter, starting at 0:", err.Error()))
	}

	lastHit = make(map[string]time.Time)

	fmt.Println(logStamp() + " " + logSuccess.Render(fmt.Sprintf(
		"hello countrr version %s is online | saved: %d", Version, lastSaved.Load())))
	fmt.Println(logStamp() + " " + logInfo.Render("source: https://github.com/cottonable/countrr"))

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	commands := watchStdin()
	var commandCh <-chan struct{}
	if commands != nil {
		commandCh = commands.C
		fmt.Println(logStamp() + " " + logInfo.Render(`type "config" and press enter to change settings while the server runs`))
	}

	shuttingDown := false
	interrupted := false

	for {
		cfg := currentConfig()
		run, err := startService(cfg)
		if err != nil {
			fmt.Println(logError.Render("failed to start server:", err.Error()))
			if perr := writeCounter(visitors.Load()); perr != nil {
				fmt.Println(logError.Render("failed to persist counter:", perr.Error()))
			}
			os.Exit(1)
		}

		restart := false
		select {
		case sig := <-signals:
			shuttingDown = true
			interrupted = sig == os.Interrupt
		case <-commandCh:
			restart = true
		}
		run.stop()

		if !restart {
			break
		}

		fmt.Println(logInfo.Render("opening configuration screen..."))
		setConfig(reconfigure())
		commands.Resume()

		if shuttingDown {
			break // a shutdown signal arrived while the configuration screen was open
		}
		fmt.Println(logSuccess.Render("configuration applied, restarting service."))
	}

	// "LOSING" is deliberate: an interrupt echoes "^C" first, so the two render
	// as "^CLOSING". Any other signal (systemd stop, OS shutdown) prints nothing
	// extra and just continues with the shutdown lines below.
	if interrupted {
		fmt.Println(logInfo.Render("LOSING"))
	}

	if err := writeCounter(visitors.Load()); err != nil {
		fmt.Println(logError.Render("failed to persist counter:", err.Error()))
	}

	fmt.Fprintf(os.Stderr, "%s %s\n", logStamp(), logSuccess.Render("ok bye"))
	os.Stdout.Sync()
}

// service is one running instance of the counter: an HTTP listener plus the
// background loops that report and persist the count.
type service struct {
	server *http.Server
	cancel context.CancelFunc
}

// reconfigure reopens the setup wizard and returns the configuration to run
// with. Without an interactive terminal the wizard cannot be shown, so the
// current settings are kept rather than writing defaults over them.
func reconfigure() Config {
	current := currentConfig()
	if !isTTY() {
		fmt.Println(logWarning.Render("no terminal available, keeping the current configuration"))
		return current
	}
	return InitSetup(current)
}

// startService binds the configured port and launches the loops. It returns an
// error when the port cannot be bound, so the caller can react instead of
// silently running without a listener.
func startService(cfg Config) (*service, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /increment", increment)
	mux.HandleFunc("GET /count", getCount)
	mux.Handle("GET /countrr.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(visitorJS)
	}))

	// Timeouts keep slow or half-open clients from holding a connection forever.
	// WriteTimeout has to outlast the outbound Turnstile verification.
	server := &http.Server{
		Handler:           corsMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      serverWriteTimeout(cfg),
		IdleTimeout:       60 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Println(logError.Render("server error:", err.Error()))
		}
	}()
	go checkLoop(ctx, cfg)
	go persistLoop(ctx, cfg)
	go sweepLoop(ctx, cfg)

	listening := fmt.Sprintf(
		"listening on http://0.0.0.0:%d | check every %ds | persist every %ds | sweep every %ds",
		cfg.Port, cfg.CheckIntervalS, cfg.PersistIntervalS, cfg.RateLimitSweepS)
	if cfg.MinIncrementIntervalMs > 0 {
		listening += fmt.Sprintf(" | max 1 increment per %dms", cfg.MinIncrementIntervalMs)
	}
	if cfg.BackupCount > 0 {
		listening += fmt.Sprintf(" | keep %d counter backups", cfg.BackupCount)
	}
	fmt.Println(logStamp() + " " + logSuccess.Render(listening))

	return &service{server: server, cancel: cancel}, nil
}

func (s *service) stop() {
	s.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		fmt.Println(logError.Render("server shutdown error:", err.Error()))
	}
}

func checkLoop(ctx context.Context, cfg Config) {
	ticker := time.NewTicker(time.Duration(cfg.CheckIntervalS) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := visitors.Load()
			delta := current - lastCheck.Load()
			fails := turnstileFailures.Swap(0)
			dropped := throttled.Swap(0)
			if delta > 0 || fails > 0 || dropped > 0 {
				msg := fmt.Sprintf("[%s] +%d new hits | ram: %d | last saved: %d",
					time.Now().Format("2006-01-02 15:04:05"), delta, current, lastSaved.Load())
				if fails > 0 {
					msg += fmt.Sprintf(" | turnstile failures: %d", fails)
				}
				if dropped > 0 {
					msg += fmt.Sprintf(" | throttled: %d", dropped)
				}
				fmt.Println(logInfo.Render(msg))
			}
			lastCheck.Store(current)
		}
	}
}

func persistLoop(ctx context.Context, cfg Config) {
	ticker := time.NewTicker(time.Duration(cfg.PersistIntervalS) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n := visitors.Load()
			if n <= lastSaved.Load() {
				continue // nothing new, skip save
			}
			if err := writeCounter(n); err != nil {
				fmt.Println(logError.Render("failed to persist counter:", err.Error()))
			}
		}
	}
}

// stdinCommands lets the terminal that started the server ask for the setup
// wizard to be reopened without restarting the process.
type stdinCommands struct {
	C      chan struct{}
	resume chan struct{}
}

// watchStdin starts the command watcher. It returns nil when stdin is not an
// interactive terminal, in which case the command is unavailable and the wizard
// is never opened behind a daemon's back.
func watchStdin() *stdinCommands {
	if !isTTY() {
		return nil
	}
	return watchReader(os.Stdin)
}

func watchReader(input io.Reader) *stdinCommands {
	commands := &stdinCommands{C: make(chan struct{}, 1), resume: make(chan struct{})}
	go func() {
		reader := bufio.NewReader(input)
		for {
			line, err := reader.ReadString('\n')
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "config", "c":
				commands.C <- struct{}{}
				<-commands.resume // yield the terminal to the wizard
			}
			if err != nil {
				return
			}
		}
	}()
	return commands
}

// Resume hands the terminal back to the watcher once the wizard has exited.
func (c *stdinCommands) Resume() {
	if c == nil {
		return
	}
	select {
	case c.resume <- struct{}{}:
	default:
	}
}

func loadCounter() error {
	data, err := os.ReadFile(resolvePath(currentConfig().CounterFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	n, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return err
	}
	visitors.Store(n)
	lastSaved.Store(n)
	lastCheck.Store(n)
	return nil
}

// writeCounter persists the count atomically: the value is synced to a
// temporary file and then renamed over the target. A crash can therefore never
// leave a truncated counter behind, which a plain overwrite could.
func writeCounter(n int64) error {
	cfg := currentConfig()
	path := resolvePath(cfg.CounterFile)
	tmp := path + ".tmp"

	if err := writeFileSynced(tmp, strconv.FormatInt(n, 10)); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}

	if n != lastSaved.Load() {
		backupCounter(path, n, cfg.BackupCount)
	}

	lastSaved.Store(n)
	fmt.Println(logStamp() + " " + logSuccess.Render(fmt.Sprintf("storage saved: %d", n)))
	return nil
}

// backupCounter drops a timestamped snapshot next to the counter file and keeps
// only the newest keep of them. Snapshots use a fixed-width YYMMDD-HHMMSS stamp,
// so sorting the names sorts them chronologically.
func backupCounter(path string, n int64, keep int64) {
	if keep <= 0 {
		return
	}

	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	snapshot := fmt.Sprintf("%s-%s%s", base, time.Now().Format("060102-150405"), ext)

	if err := writeFileSynced(snapshot, strconv.FormatInt(n, 10)); err != nil {
		fmt.Println(logWarning.Render("could not write counter backup:", err.Error()))
		return
	}

	snapshots, err := filepath.Glob(base + "-*" + ext)
	if err != nil {
		fmt.Println(logWarning.Render("could not list counter backups:", err.Error()))
		return
	}
	if int64(len(snapshots)) <= keep {
		return
	}

	sort.Strings(snapshots)
	for _, stale := range snapshots[:len(snapshots)-int(keep)] {
		if err := os.Remove(stale); err != nil {
			fmt.Println(logWarning.Render("could not prune counter backup:", err.Error()))
		}
	}
}

// writeFileSynced writes data and fsyncs it, so the bytes are durable before the
// rename that publishes them.
func writeFileSynced(path, data string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func increment(w http.ResponseWriter, r *http.Request) {
	// Checked first so a flood cannot reach the outbound Turnstile call or grow
	// the per-IP map.
	interval := time.Duration(currentConfig().MinIncrementIntervalMs) * time.Millisecond
	if !allowIncrement(interval) {
		throttled.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("too many requests"))
		return
	}

	ip := realIP(r)

	token := r.Header.Get("X-Turnstile-Token")
	if !verifyTurnstile(token, ip) {
		turnstileFailures.Add(1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("verification failed"))
		return
	}

	if !canHit(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("too many requests"))
		return
	}
	n := visitors.Add(1)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(strconv.FormatInt(n, 10)))
}

func canHit(ip string) bool {
	hitsMu.Lock()
	defer hitsMu.Unlock()
	now := time.Now()
	last, ok := lastHit[ip]
	if ok && now.Sub(last) < time.Duration(currentConfig().RateLimitWindowS)*time.Second {
		return false
	}
	lastHit[ip] = now
	return true
}

// allowIncrement enforces a global minimum spacing between counted visits. Real
// traffic never comes close to it, but it caps what a flood can achieve. An
// interval of zero disables the cap.
func allowIncrement(interval time.Duration) bool {
	if interval <= 0 {
		return true
	}
	throttleMu.Lock()
	defer throttleMu.Unlock()
	now := time.Now()
	if !lastIncrement.IsZero() && now.Sub(lastIncrement) < interval {
		return false
	}
	lastIncrement = now
	return true
}

// sweepHits drops cooldowns that have already expired. Without it the map keeps
// one entry per IP forever, which grows without bound on a long-running server.
func sweepHits(now time.Time, window time.Duration) int {
	hitsMu.Lock()
	defer hitsMu.Unlock()

	removed := 0
	for ip, last := range lastHit {
		if now.Sub(last) >= window {
			delete(lastHit, ip)
			removed++
		}
	}

	// Deleting does not shrink a Go map: the bucket array stays at its peak, so
	// a burst of unique IPs would hold that memory for the life of the process.
	// Once the map is mostly dead, move the survivors into a fresh map so the
	// old allocation can be collected.
	if removed > len(lastHit) {
		live := make(map[string]time.Time, len(lastHit))
		for ip, last := range lastHit {
			live[ip] = last
		}
		lastHit = live
	}
	return removed
}

func sweepLoop(ctx context.Context, cfg Config) {
	interval := time.Duration(cfg.RateLimitSweepS) * time.Second
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			window := time.Duration(cfg.RateLimitWindowS) * time.Second
			if n := sweepHits(time.Now(), window); n > 0 {
				fmt.Println(logInfo.Render(fmt.Sprintf("[%s] pruned %d expired rate-limit entries",
					time.Now().Format("2006-01-02 15:04:05"), n)))
			}
		}
	}
}

func getCount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(strconv.FormatInt(visitors.Load(), 10)))
}
