# countrr

A lightweight, self-hosted website visitor counter built in Go. Track unique visitors across your site with no database or external dependencies.

## Architecture

```
┌──────────────┐      POST /increment       ┌───────────────┐
│   Your Site  │ ──────────────────────────► │   Go Server    │
│  (browser)   │     X-Turnstile-Token       │  configurable    │
│              │ ◄──── GET /count ────────── │                │
│ trackPageView│        visitor count         │ counter.txt   │
└──────────────┘                            └───────────────┘
```

**How it works:** Each browser uses [Cloudflare Turnstile](https://developers.cloudflare.com/turnstile/) to prove it's a real device. The server verifies the token and rate-limits each IP to one vote every ~6 hours, preventing spam. Visits are saved to disk every 15 minutes, written atomically (temp file plus rename) so an interrupted write can never corrupt the count. Turnstile can be disabled entirely by leaving both `turnstile_*` fields empty in config.

## API

| Method | Path | Description | Headers |
|--------|------|-------------|---------|
| `POST` | `/increment` | Record one visitor | `X-Turnstile-Token` (when turnstile enabled) |
| `GET`  | `/count` | Get current visitor total | — |
| `GET`  | `/countrr.js` | Serves the client-side tracking script | — |

All responses are plain integers (the count). CORS is enabled by default so any origin can call the API. When turnstile is configured, only verified tokens are accepted; when disabled (no keys in config), any request is accepted with IP-based rate limiting applied.

**`/count` is intentionally open.** It needs no token, is not rate limited, and any origin can read it. Only `/increment` is protected by Turnstile and the rate limits, because that is the only endpoint that can change the number. If you need the total to be private, put `/count` behind your own auth proxy.

**Rate limiting and reverse proxies.** The client IP is taken from `CF-Connecting-IP`, then `X-Forwarded-For`, then the connection address. Those headers are set by whatever sits in front of the server, so if the port is reachable directly, a visitor can forge them and get a fresh rate-limit window on every request. Terminate traffic at Cloudflare or a reverse proxy that overwrites the header, and keep the origin port closed to the internet.

## Self-Hosting

### Prerequisites

Go: [Go 1.24](https://go.dev/dl/) or later (or use a prebuilt binary below)

### Supported platforms

| Binary | OS | Architecture | Notes |
|--------|----|--------------|-------|
| `counter-linux-amd64` | Linux | x86_64 | statically linked, no glibc dependency |
| `counter-linux-arm` | Linux | ARM 32-bit | statically linked, ARMv7 (`GOARM=7`) |
| `counter-darwin-amd64` | macOS | Intel (x86_64) | 10.15 Catalina or later |
| `counter-darwin-arm64` | macOS | Apple Silicon | 11 Big Sur or later |
| `counter-windows-amd64.exe` | Windows | x86_64 | 7 or later |

The Linux builds are fully static, so they run on any distribution (including Alpine/musl) and do not depend on a particular glibc version. For ARMv6 boards such as the Raspberry Pi 1 or Zero, rebuild `counter-linux-arm` with `GOARM=6`.

On macOS and Linux run `chmod +x <binary>` before the first launch. On Windows it is a plain `.exe`.

### Deploy from source

```bash
git clone https://github.com/cottonable/countrr.git
cd countrr
go build -o countrr .
./countrr
```

On first run (no `config.json`), an interactive setup wizard appears — guiding you through every configurable option with inline explanations for each field. Use **↑↓** to navigate, **Enter** to edit, **Q** to save and finish (**Ctrl-C** cancels without saving).

If no TTY is available (e.g. remote deployment via SSH or systemd), a non-interactive stdin version runs instead. The server starts on `http://0.0.0.0:1312`, and `counter.txt` is written next to the binary on the first save interval and on shutdown.

`config.json` and `counter.txt` are always resolved relative to the directory containing the binary (relative paths in `counter_file` are anchored there too), so the server reads and writes the same files no matter which directory you launch it from.

### Reconfiguring

| When | How | Effect |
|------|-----|--------|
| Before starting | `./countrr --config` | Opens the wizard, then exits |
| While running | Type `config` and press **Enter** in the terminal running the server | Opens the wizard; when saved, the server reloads and rebinds without the process exiting |

The live reconfigure keeps the in-memory visit count and the steady-state signal handling, so nothing is lost during the swap. It needs an interactive terminal: under systemd (no TTY) stop the service, run the binary with `--config`, then start it again. If the configured port cannot be bound, the process reports the error and exits rather than running without a listener.

### Deploy with a prebuilt binary

Download the right binary for your platform from the [Releases page](https://github.com/cottonable/countrr/releases/latest):

```bash
wget https://github.com/cottonable/countrr/releases/latest/download/counter-linux-arm
chmod +x counter-linux-arm
./counter-linux-arm
```

### Run as a background service (systemd)

```ini
# /etc/systemd/system/countrr.service
[Unit]
Description=Countrr Service
After=network.target

[Service]
ExecStart=[path/to/counter-linux-amd64]
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now countrr
```

Because `config.json` and `counter.txt` live next to the binary, `WorkingDirectory` is irrelevant. A service has no terminal, so the live reconfigure command is unavailable: stop the service, run the binary with `--config` (or edit `config.json`), then start it again.

Finally, point your site at the server's URL via `data-api` (or the `window.__COUNTRR_API` global).

## Configuration

The server reads `config.json` at startup. If it doesn't exist, an interactive setup wizard runs on the first launch (see **Deploy from source** above). You can also invoke it manually at any time with `--config`.

```jsonc
{
  "port": 1312,                   // HTTP listen port
  "counter_file": "counter.txt",  // path for visit data storage
  "persist_interval_s": 900,      // seconds between disk writes (15 min)
  "check_interval_s": 60,         // seconds between log messages (1 min)
  "rate_limit_window_s": 21600,   // cooldown per visitor in seconds (6h)
  "rate_limit_sweep_s": 300,      // seconds between prunes of the per-IP cooldown map (5 min)
  "min_increment_interval_ms": 0, // global minimum spacing between counted visits (0 = off, 500 = 0.5s)
  "turnstile_site_key": "",       // Cloudflare Turnstile site key (optional)
  "turnstile_secret": ""          // Cloudflare Turnstile secret key (optional)
}
```

All numeric values are in seconds — plain integers for easy JSON parsing.

`rate_limit_window_s` is how long a visitor must wait before counting again, and `rate_limit_sweep_s` is how often expired per-IP cooldowns are dropped from memory. Every distinct IP is remembered for one window and then pruned, so a long-running server does not accumulate entries forever. Set `rate_limit_sweep_s` to a smaller value on very busy sites, or larger on tiny ones; it costs almost nothing either way.

`min_increment_interval_ms` is a global throughput cap, independent of any IP: at `500` the server counts at most one visit every half second, and every other request gets `429`. Leave it at `0` to disable. It is checked before anything else, so a flood never reaches the Turnstile API or the per-IP map. The trade-off is that a genuine burst of visitors arriving faster than the cap is dropped rather than queued, because the client script treats `429` as final; `500` or `1000` is generous for normal traffic.

**Turnstile (anti-bot protection):** Leave both `turnstile_*` fields empty to disable. When you provide a site key + secret, every `/increment` request must include a verified turnstile token from the client-side script below. If verification fails (bot detected), the request is rejected with 403. Network errors during verification are lenient — they allow the request rather than breaking your site's visitors.

## Integration on Your Site

Drop this into any page you want to track. One script tag pointing to your own server. The Go binary serves everything: the JS file, `/increment`, and `/count`. Same origin, no cross-domain headaches.

If you'd rather host `countrr.js` separately, grab it directly from [the Releases page](https://github.com/cottonable/countrr/releases/latest).

### With Turnstile (recommended)

```html
<!-- Cloudflare Turnstile SDK -->
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>

<!-- Our counter — same domain as your API -->
<script src="https://[your-domain]/countrr.js"
        data-api="https://[your-domain]"
        data-sitekey="[your-turnstile-sitekey]"></script>
```

### Without Turnstile

If turnstile isn't configured in `config.json` (both keys empty):

```html
<script src="https://[your-domain]/countrr.js"
        data-api="https://[your-domain]"></script>
```

That's all. Auto-tracks on page load. No config, no callback, no build step.

### What the script does

1. Runs once the DOM is ready (immediately if it already is)
2. If turnstile is configured → renders an invisible widget and runs the challenge to get a verified token
3. Sends POST to your counter server (with or without turnstile token depending on config)
4. IP rate limits ensure one vote per visitor per configurable cooldown period
5. Exposes `window.Tracker.trackPageView()` and `window.Tracker.getCount()` if you need manual control

### Override defaults

Any value can be changed via HTML attributes or by declaring globals **before** loading the script:

| Script attribute | Global var | Default | Description |
|------------------|------------|---------|-------------|
| `data-api`       | `__COUNTRR_API` | (your host) | URL of your counter server |
| `data-sitekey`   | `__COUNTRR_SITEKEY` | none | Cloudflare site key |

### Display the count on your page

Add an element and fetch the number:

```html
<p>Visitors: <span id="visitor-count">—</span></p>

<script src="https://[your-domain]/countrr.js"
        data-api="https://[your-domain]"></script>

<script>
  window.addEventListener('load', async () => {
    try {
      const count = await window.Tracker.getCount();
      document.getElementById('visitor-count').textContent = count;
    } catch (e) {
      document.getElementById('visitor-count').textContent = 'unknown';
    }
  });
</script>
```

## Project Structure

```
countrr/
├── main.go               # Go server (handlers, persistence, CORS, live reconfigure)
├── setup.go              # Bubbletea setup wizard
├── countrr.js            # Client-side tracking script (embedded into the binary)
├── go.mod / go.sum       # Go module definition
├── release/              # Cross-compiled binaries for each platform
├── config.json           # Server configuration (auto-created / wizard)
├── counter.txt           # Visit data (created at runtime)
├── LICENSE
└── README.md
```

## License

MIT - feel free to fork, modify, and host your own instance.
