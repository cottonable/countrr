# countrr

A lightweight, self-hosted website visitor counter built in Go. Track unique visitors across your site with no database or external dependencies.

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

All responses are plain integers. CORS is enabled by default so any origin can call these endpoints from a browser. `/count` is public and open to anyone; only `/increment` is rate limited because it is the only endpoint that changes the number. If you need the total to be private, put `/count` behind your own auth proxy.

**Rate limiting and reverse proxies.** The client IP is taken from `CF-Connecting-IP`, then `X-Forwarded-For`, then the connection address. Those headers are set by whatever sits in front of the server, so if the port is reachable directly, a visitor can forge them and get a fresh rate-limit window on every request. Terminate traffic at Cloudflare or a reverse proxy that overwrites the header, and keep the origin port closed to the internet.

## Self-Hosting

### Prerequisites

Go: [Go 1.24](https://go.dev/dl/) or later (or use a prebuilt binary below)

### Supported platforms

|| Binary | OS | Architecture | Minimum version |
|--------|----|--------------|-----------------|
| `countrr-linux-amd64` | Linux | x86_64 | any |
| `countrr-linux-386` | Linux | i386 (32-bit x86) | any |
| `countrr-linux-arm` | Linux | ARMv7 (32-bit) | any |
| `countrr-linux-armv6` | Linux | ARMv6 (32-bit) | any |
| `countrr-linux-arm64` | Linux | ARM64 (AArch64) | any |
| `countrr-darwin-amd64` | macOS | Intel (x86_64) | 13 Ventura |
| `countrr-darwin-arm64` | macOS | Apple Silicon (M1+) | 13 Ventura |
| `countrr-windows-amd64.exe` | Windows | x86_64 | 10 or later |
| `countrr-windows-arm64.exe` | Windows | ARM64 | 10 or later |

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

The live reconfigure keeps the in-memory visit count and the steady-state signal handling, so nothing is lost during the swap. It needs an interactive terminal: under systemd (no TTY) stop the service, run the binary with `--config`, then start it again. If the configured port cannot be bound, the process reports the error and exits rather than running without a listener.

### Deploy with a prebuilt binary

Download the right binary for your platform from the [Releases page](https://github.com/cottonable/countrr/releases/latest):

```bash
wget https://github.com/cottonable/countrr/releases/latest/download/countrr-{platform}-{architecture}
chmod +x countrr-{platform}-{architecture}
./countrr-{platform}-{architecture}
```

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
  "min_increment_interval_ms": 0  // global minimum spacing between counted visits (0 = off, 500 = 0.5s)
}
```

All numeric values are in seconds — plain integers for easy JSON parsing.

`rate_limit_window_s` is how long a visitor must wait before counting again, and `rate_limit_sweep_s` is how often expired per-IP cooldowns are dropped from memory. Every distinct IP is remembered for one window and then pruned, so a long-running server does not accumulate entries forever. Set `rate_limit_sweep_s` to a smaller value on very busy sites, or larger on tiny ones; it costs almost nothing either way.

`min_increment_interval_ms` is a global throughput cap, independent of any IP: at `500` the server counts at most one visit every half second, and every other request gets `429`. Leave it at `0` to disable. It is checked before anything else, so a flood never reaches the per-IP map. The trade-off is that a genuine burst of visitors arriving faster than the cap is dropped rather than queued; `500` or `1000` is generous for normal traffic.

`backup_count` keeps a rolling set of timestamped snapshots next to the counter file, so a corrupted or mistaken save can be rolled back. At `10` with the default 15-minute interval you hold roughly the last two and a half hours. Snapshots are named `<counter>-YYMMDD-HHMMSS.txt`, written only when the value actually changes (a restart that saves nothing creates nothing), and anything past the newest `backup_count` is deleted, including snapshots left behind by an earlier run. `0` disables it. The live `counter.txt` always stays the authoritative file; snapshots are copies, never renames.

## Integration on Your Site

No client script is shipped; POST to `/increment` from your own code. A minimal inline snippet:

```html
<script>
  fetch('https://[your-domain]/increment', { method: 'POST' })
    .then(r => { if (r.ok || r.status === 429) return; throw new Error(r.status); })
    .catch(() => {});
</script>
```

That's all. Auto-tracks on page load. IP rate limits ensure one counted visit per visitor per configurable cooldown period.

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

## Project Structure

```
countrr/
├── main.go               # Go server (handlers, persistence, CORS, live reconfigure)
├── setup.go              # Bubbletea setup wizard
├── go.mod / go.sum       # Go module definition
├── release/              # Cross-compiled binaries for each platform
├── config.json           # Server configuration (auto-created / wizard)
├── counter.txt           # Visit data (created at runtime)
├── LICENSE
└── README.md
```

## License

MIT - feel free to fork, modify, and host your own instance.
