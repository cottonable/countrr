# countrr

A lightweight, self-hosted website visitor counter built in Go. Count unique visitors across your site with no database or external dependencies. <8MB RAM idle.

![ram usage screenshot](https://github.com/user-attachments/assets/e1f1dfbf-4918-4222-a1d7-079d5f30815c)
![output](https://github.com/user-attachments/assets/f15407e7-71d6-44f3-bfb8-0e6d209797bb)

## Architecture

```
┌──────────────┐        POST /increment      ┌───────────────┐
│   Your Site  │ ──────────────────────────► │   Go Server   │
│   (browser)  │                             │  configurable │
│              │ ◄─────── GET /count ─────── │               │
│ trackPageView│        visitor count        │  counter.txt  │
└──────────────┘                             └───────────────┘
```

**How it works:** The server rate-limits each IP to one visit every ~6 hours, preventing spam. Visits are saved to disk every 15 minutes, written atomically (temp file plus rename) so an interrupted write can never corrupt the count.

## API

### `POST /increment`

Records one visitor. Returns **200 OK** with the new total as a plain integer (e.g. `42`).

Returns **429 Too Many Requests** when rate limited (per-IP cooldown not expired or global cap exceeded), with body `too many requests`.

CORS enabled on all endpoints. No auth required.

**Example:**

```bash
curl -X POST https://your-counter-host/increment
# → 42
# HTTP 200
```

### `GET /count`

Returns the current total as a plain integer. Not rate limited. Always returns **200 OK**.

**Example:**

```bash
curl https://your-counter-host/count
# → 42
# HTTP 200
```

---

All responses are plain integers. CORS is enabled by default so any origin can call these endpoints from a browser; set `allowed_domains` (see **Configuration**) to restrict the API to specific sites. `/count` is public and open to anyone; only `/increment` is rate limited because it is the only endpoint that changes the number. If you need the total to be private, put `/count` behind your own auth proxy.

**Rate limiting and reverse proxies.** The client IP is taken from `CF-Connecting-IP`, then `X-Forwarded-For`, then the connection address. Those headers are set by whatever sits in front of the server, so if the port is reachable directly, a visitor can forge them and get a fresh rate-limit window on every request. Terminate traffic at Cloudflare or a reverse proxy that overwrites the header, and keep the origin port closed to the internet.

## Self-Hosting

### Prerequisites

Go: [Go 1.24](https://go.dev/dl/) or later (or use a prebuilt binary below)

### Supported platforms

All 3 major OSes are supported, downloads for prebuilt binaries are available on the [Releases page](https://github.com/cottonable/countrr/releases/latest)

The macOS minimum is the deployment target the Go linker stamps into the binary (`minos 13.0`), and the Windows one is the PE subsystem version (`10.0`) plus the toolchain's own floor, which has been Windows 10 since Go 1.21. Building with an older Go toolchain is what would lower the macOS figure.

All Linux builds are fully static (no glibc dependency), so they run on any distribution including Alpine/musl. The five Linux binaries cover every common hosting platform: cloud VPS (AWS Graviton, DigitalOcean, Hetzner), Raspberry Pi (all models Zero through 5), NAS devices (Synology/QNAP), Docker containers, home servers, SBCs (Odroid, Jetson), and anything else running a Linux-compatible OS.

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
| While running (any environment, incl. a service) | Edit `config.json` on disk | Detected within ~2s; the server reloads and rebinds without the process exiting |

The live reconfigure keeps the in-memory visit count and the steady-state signal handling, so nothing is lost during the swap. The wizard needs an interactive terminal, so a systemd service (no TTY) can't open it directly; edit `config.json` instead and the change is applied automatically, or stop the service, run the binary with `--config`, then start it again. If the configured port cannot be bound, the process reports the error and exits rather than running without a listener.

The file watcher only ever reads `config.json` (a `stat` every 2 seconds), so it does not add any writes or storage wear. A change is applied only if the file parses cleanly and differs from the running settings: a malformed or half-written file is ignored with a warning and the current configuration is kept. Because a save only replaces the file atomically, an edit is never seen partway through.

### Run as a background service (systemd)

```ini
# /etc/systemd/system/countrr.service
[Unit]
Description=Countrr Service
After=network.target

[Service]
ExecStart=[path/to/countrr-linux-amd64]
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now countrr
```

Because `config.json` and `counter.txt` live next to the binary, `WorkingDirectory` is irrelevant. A service has no terminal, so the live reconfigure command is unavailable: stop the service, run the binary with `--config` (or edit `config.json`), then start it again.

Finally, point your site at the server's URL by POSTing to `/increment` on page load (see **Integration on Your Site** below).

## Configuration

The server reads `config.json` at startup. If it doesn't exist, an interactive setup wizard runs on the first launch (see **Deploy from source** above). You can also invoke it manually at any time with `--config`.

```jsonc
{
  "port": 1312,                   // HTTP listen port
  "counter_file": "counter.txt",  // path for visit data storage
  "backup_count": 0,              // timestamped counter snapshots to keep (0 = off)
  "persist_interval_s": 900,      // seconds between disk writes (15 min)
  "check_interval_s": 60,         // seconds between log messages (1 min)
  "rate_limit_window_s": 21600,   // cooldown per visitor in seconds (6h)
  "rate_limit_sweep_s": 300,      // seconds between prunes of the per-IP cooldown map (5 min)
  "min_increment_interval_ms": 0, // global minimum spacing between counted visits (0 = off, 500 = 0.5s)
  "allowed_domains": []           // domains allowed to call the API (empty = all)
}
```

All numeric values are in seconds — plain integers for easy JSON parsing.

`rate_limit_window_s` is how long a visitor must wait before counting again, and `rate_limit_sweep_s` is how often expired per-IP cooldowns are dropped from memory. Every distinct IP is remembered for one window and then pruned, so a long-running server does not accumulate entries forever. Set `rate_limit_sweep_s` to a smaller value on very busy sites, or larger on tiny ones; it costs almost nothing either way.

`min_increment_interval_ms` is a global throughput cap, independent of any IP: at `500` the server counts at most one visit every half second, and every other request gets `429`. Leave it at `0` to disable. It is checked before anything else, so a flood never reaches the per-IP map. The trade-off is that a genuine burst of visitors arriving faster than the cap is dropped rather than queued; `500` or `1000` is generous for normal traffic.

`backup_count` keeps a rolling set of timestamped snapshots next to the counter file, so a corrupted or mistaken save can be rolled back. At `10` with the default 15-minute interval you hold roughly the last two and a half hours. Snapshots are named `<counter>-YYMMDD-HHMMSS.txt`, written only when the value actually changes (a restart that saves nothing creates nothing), and anything past the newest `backup_count` is deleted, including snapshots left behind by an earlier run. `0` disables it. The live `counter.txt` always stays the authoritative file; snapshots are copies, never renames.

`allowed_domains` restricts which sites may call the API. Each request's browser `Origin` header is matched against the list, and anything else receives `403 Forbidden`. List multiple domains to allow more than one, separated by commas (in the wizard) or as separate JSON array entries (in `config.json`):

```jsonc
"allowed_domains": ["example.com", "www.example.com"]
```

Entries accept a bare domain (`example.com`), a full origin (`https://example.com`), or either with a port (`example.com:8080`), and matching is case-insensitive. A single `"*"` entry allows every origin. Leave the list empty to keep the default open behavior. Requests without an `Origin` header (same-origin navigations and non-browser clients) are never cross-origin and always pass through, so the allowlist is a defense against other websites, not a substitute for network-level access control.

## Integration on Your Site

No client script is shipped; POST to `/increment` from your own code. A minimal inline snippet:

```html
<script>
  fetch('https://[your-domain]/increment', { method: 'POST' })
    .then(r => { if (r.ok || r.status === 429) return; throw new Error(r.status); })
    .catch(() => {});
</script>
```

That's all. Auto-counts on page load. IP rate limits ensure one counted visit per visitor per configurable cooldown period.

## Displaying the Count on a Website

To show the visitor total on a page, add an element where the number should appear and fetch `GET /count`:

```html
<p>Visitors: <span id="countrr">—</span></p>

<script>
  window.addEventListener('load', async () => {
    try {
      const res = await fetch('https://[your-domain]/count', { cache: 'no-store' });
      if (!res.ok) throw new Error(res.status);
      document.getElementById('countrr').textContent = await res.text();
    } catch (e) {
      document.getElementById('countrr').textContent = "can't connect";
    }
  });
</script>
```

`/count` returns the total as a plain integer (e.g. `42`), CORS is open, and it is never rate limited, so this works from any page on any domain. The `—` placeholder shows until the number loads, and `unknown` if the server can't be reached.

If you also want that page counted as a visit, add the `/increment` snippet from **Integration on Your Site** above.

## License

MIT - feel free to fork, modify, and host your own instance.
