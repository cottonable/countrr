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

**How it works:** Each browser uses [Cloudflare Turnstile](https://developers.cloudflare.com/turnstile/) to prove it's a real device. The server verifies the token and rate-limits each IP to one vote every ~6 hours, preventing spam. Visits are saved to disk every 15 minutes. Turnstile can be disabled entirely by leaving both `turnstile_*` fields empty in config.

## API

| Method | Path | Description | Headers |
|--------|------|-------------|---------|
| `POST` | `/increment` | Record one visitor | `X-Turnstile-Token` (when turnstile enabled) |
| `GET`  | `/count` | Get current visitor total | — |

All responses are plain integers (the count). CORS is enabled by default so any origin can call the API. When turnstile is configured, only verified tokens are accepted; when disabled (no keys in config), any request is accepted with IP-based rate limiting applied.

## Self-Hosting

### Prerequisites

Go: [Go 1.22](https://go.dev/dl/) or later (or use a prebuilt binary below)

Prebuilt binaries are available for Linux x86_64, Linux ARM 32-bit, macOS Intel, macOS Apple Silicon, and Windows. They run without any dependencies, don't forger to `chmod +x`, before running. For OS-specific requirements, see Release Notes.

### Deploy from source

```bash
git clone https://github.com/cottonable/countrr.git
cd countrr
go build -o countrr .
./countrr
```

The server starts on `http://0.0.0.0:1312`. A `counter.txt` file is created automatically on first run.

### Deploy with a prebuilt binary

Download the right binary for your platform from the [Releases page](https://github.com/cottonable/countrr/releases/latest):

```bash
wget https://github.com/cottonable/countrr/releases/download/v1.0.0/counter-linux-arm
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
ExecStart=[path/to/countrr]
WorkingDirectory=[path/to/project]
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now countrr
```


Then ensure `COUNTRR_API` in your site points to this URL.

## Configuration

The server reads `config.json` at startup. If it doesn't exist, it creates one with defaults.

```jsonc
{
  "port": 1312,                   // HTTP listen port
  "counter_file": "counter.txt",  // path for visit data storage
  "persist_interval_s": 900,      // seconds between disk writes (15 min)
  "check_interval_s": 60,         // seconds between log messages (1 min)
  "rate_limit_window_s": 21600,   // cooldown per visitor in seconds (6h)
  "turnstile_site_key": "",       // Cloudflare Turnstile site key (optional)
  "turnstile_secret": ""          // Cloudflare Turnstile secret key (optional)
}
```

All numeric values are in seconds — plain integers for easy JSON parsing.

**Turnstile (anti-bot protection):** Leave both `turnstile_*` fields empty to disable. When you provide a site key + secret, every `/increment` request must include a verified turnstile token from the client-side script below. If verification fails (bot detected), the request is rejected with 403. Network errors during verification are lenient — they allow the request rather than breaking your site's visitors.

### Configuration constants (client-side)

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

1. Waits for `DOMContentLoaded`
2. If turnstile is configured → runs invisible challenge, gets verified token
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
├── main.go               # Go server (handlers, persistence, CORS)
├── go.mod                # Go module definition
├── RELEASE.md            # Release notes and OS requirements
├── countrr.js            # Client-side tracking script
├── config.json           # Server configuration (auto-created)
├── counter.txt           # Visit data (created at runtime)
├── LICENSE
└── README.md
```

## License

MIT - feel free to fork, modify, and host your own instance.
