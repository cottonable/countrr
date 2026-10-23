/**
 * countrr — plug-and-play site tracking with Cloudflare Turnstile.
 *
 * Usage: add before </body> on pages you want to track.
 *   <script src="https://your-host/countrr.js"
 *           data-api="https://your-host"
 *           data-sitekey="YOUR_TURNSTILE_SITEKEY"></script>
 *
 * If no sitekey is provided, the script increments directly (no turnstile).
 * Exposes window.Tracker.trackPageView() and window.Tracker.getCount().
 */

const API_URL     = window.__COUNTRR_API ?? document.currentScript?.dataset.api  ?? 'https://localhost:1312';
const SITE_KEY    = window.__COUNTRR_SITEKEY ?? document.currentScript?.dataset.sitekey;
const MAX_RETRIES = 2;

/* ── helpers ─────────────────────────────────────────────── */

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

/* ── public API ──────────────────────────────────────────── */

async function trackPageView() {
    let token = null;

    if (SITE_KEY) {
        // Wait for turnstile SDK then get a verified token
        try {
            await new Promise((resolve, reject) => {
                if (window.turnstile) { resolve(); return; }
                const handler = setInterval(() => {
                    if (window.turnstile) { clearInterval(handler); resolve(); }
                }, 50);
                setTimeout(() => { clearInterval(handler); reject(new Error('turnstile timeout')); }, 10000);
            });

            const widgetId = 'countrr-tc-widget-' + Math.random().toString(36).slice(2);
            const response = await window.turnstile.execute(widgetId, { sitekey: SITE_KEY });
            token = response;
        } catch (e) {
            console.warn('[countrr] turnstile error:', e.message);
            // Fallback: increment without verification so real visitors aren't blocked
        }
    }

    for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
        try {
            const headers = {};
            if (token) headers['X-Turnstile-Token'] = token;

            const res = await fetch(`${API_URL}/increment`, {
                method: 'POST',
                headers,
            });

            if (res.ok || res.status === 429 || res.status === 403) break;
            throw new Error(`unexpected status: ${res.status}`);
        } catch (err) {
            console.warn('[countrr] increment failed:', err.message);
            if (attempt < MAX_RETRIES) await sleep(500 * (attempt + 1));
        }
    }
}

export async function getCount() {
    const res = await fetch(`${API_URL}/count`, { cache: 'no-store' });
    if (!res.ok) throw new Error(`count failed: ${res.status}`);
    return parseInt(await res.text(), 10);
}

/* ── auto-tracks on load ─────────────────────────────────── */

if (SITE_KEY) {
    // Create invisible widget div
    const div = document.createElement('div');
    div.id = 'countrr-tc-widget-' + Math.random().toString(36).slice(2);
    div.style.display = 'none';
    document.body.appendChild(div);

    // Execute when DOM ready (turnstile needs time to initialize)
    document.addEventListener('DOMContentLoaded', () => {
        setTimeout(trackPageView, 200);
    });
} else {
    // No turnstile — direct beacon
    document.addEventListener('DOMContentLoaded', () => {
        navigator.sendBeacon(`${API_URL}/increment`, new Blob([''], { type: 'text/plain' }));
    });
}

window.Tracker = { trackPageView, getCount };
