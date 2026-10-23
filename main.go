package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Config holds every tunable setting for the server.
type Config struct {
	Port             int    `json:"port"`
	CounterFile      string `json:"counter_file"`
	PersistIntervalS int64  `json:"persist_interval_s"`
	CheckIntervalS   int64  `json:"check_interval_s"`
	RateLimitWindowS int64  `json:"rate_limit_window_s"`
	TurnstileSiteKey string `json:"turnstile_site_key"`
	TurnstileSecret  string `json:"turnstile_secret"`
}

// Default values used when config.json doesn't exist or is invalid.
func DefaultConfig() Config {
	return Config{
		Port:             1312,
		CounterFile:      "counter.txt",
		PersistIntervalS: 900,   // 15 min
		CheckIntervalS:   60,    // 1 min
		RateLimitWindowS: 21600, // 6h
	}
}

// LoadConfig reads a JSON config file and falls back to defaults on any error.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = WriteConfig(path, cfg)
		} else {
			fmt.Println("could not read config, using defaults:", err)
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Println("invalid config, using defaults:", err)
		return cfg, err
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

func verifyTurnstile(token, remoteIP string) bool {
	if config.TurnstileSecret == "" || config.TurnstileSiteKey == "" {
		return true // not configured → allow
	}

	payload := strings.NewReader(fmt.Sprintf(
		`{"secret":"%s","response":"%s","remoteip":"%s"}`,
		config.TurnstileSecret,
		token,
		remoteIP,
	))

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"https://challenges.cloudflare.com/turnstile/v0/siteverify",
		payload,
	)
	if err != nil {
		fmt.Println("turnstile req error:", err)
		return true
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("turnstile verification error:", err)
		return true // network failure → allow (don't break site)
	}
	defer resp.Body.Close()

	var tr TurnstileResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		fmt.Println("turnstile decode error:", err)
		return true
	}
	if !tr.Success {
		fmt.Printf("turnstile failed: %v\n", tr.ErrorCodes)
	}
	return tr.Success
}

// Version is the current release version.
const Version = "1"

//go:embed countrr.js
var visitorJS []byte

var (
	config    Config
	visitors  atomic.Int64
	lastSaved atomic.Int64
	lastCheck atomic.Int64

	turnstileFailures atomic.Int64

	hitsMu  sync.Mutex
	lastHit map[string]time.Time
)

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
	var err error
	config, err = LoadConfig("config.json")
	if err != nil {
		fmt.Println("using default configuration")
	}

	if err := loadCounter(); err != nil {
		fmt.Println("failed to load counter, starting at 0:", err)
	}

	lastHit = make(map[string]time.Time)

	fmt.Printf("[%s] hello countrr version %s is online | saved: %d\n",
		time.Now().Format("2006-01-02 15:04:05"), Version, lastSaved.Load())

	go func() {
		ticker := time.NewTicker(time.Duration(config.CheckIntervalS) * time.Second)
		for range ticker.C {
			current := visitors.Load()
			delta := current - lastCheck.Load()
			fails := turnstileFailures.Swap(0)
			if delta > 0 || fails > 0 {
				msg := fmt.Sprintf("[%s] +%d new hits | ram: %d | last saved: %d",
					time.Now().Format("2006-01-02 15:04:05"), delta, current, lastSaved.Load())
				if fails > 0 {
					msg += fmt.Sprintf(" | turnstile failures: %d", fails)
				}
				fmt.Println(msg)
			}
			lastCheck.Store(current)
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Duration(config.PersistIntervalS) * time.Second)
		for range ticker.C {
			n := visitors.Load()
			if n <= lastSaved.Load() {
				continue // nothing new, skip save
			}
			if err := writeCounter(n); err != nil {
				fmt.Println("failed to persist counter:", err)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /increment", increment)
	mux.HandleFunc("GET /count", getCount)
	mux.Handle("GET /countrr.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(visitorJS)
	}))

	server := &http.Server{Addr: fmt.Sprintf(":%d", config.Port), Handler: corsMiddleware(mux)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Println("server error:", err)
		}
	}()

	<-ctx.Done()

	if err := server.Shutdown(context.Background()); err != nil {
		fmt.Println("server shutdown error:", err)
	}

	fmt.Println("LOSING") // SHOWS "^CLOSING" IF EXIT WITH ^C

	if err := writeCounter(visitors.Load()); err != nil {
		fmt.Println("failed to persist counter:", err)
	}

	fmt.Fprintf(os.Stderr, "[%s] ok bye\n", time.Now().Format("2006-01-02 15:04:05"))
	os.Stdout.Sync()
}

func loadCounter() error {
	data, err := os.ReadFile(config.CounterFile)
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

func writeCounter(n int64) error {
	if err := os.WriteFile(config.CounterFile, []byte(strconv.FormatInt(n, 10)), 0644); err != nil {
		return err
	}
	lastSaved.Store(n)
	fmt.Printf("[%s] storage saved: %d\n", time.Now().Format("2006-01-02 15:04:05"), n)
	return nil
}

func increment(w http.ResponseWriter, r *http.Request) {
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
	if ok && now.Sub(last) < time.Duration(config.RateLimitWindowS)*time.Second {
		return false
	}
	lastHit[ip] = now
	return true
}

func getCount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(strconv.FormatInt(visitors.Load(), 10)))
}
