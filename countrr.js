/**
 * countrr - plug-and-play site tracking with Cloudflare Turnstile.
 *
 * Usage: add before </body> on pages you want to track.
 *   <script src="https://your-host/countrr.js"
 *           data-api="https://your-host"
 *           data-sitekey="YOUR_TURNSTILE_SITEKEY"></script>
 *
 * data-api is optional; it defaults to the page's own origin.
 * If no sitekey is provided, the script increments directly (no turnstile).
 * Exposes window.Tracker.trackPageView() and window.Tracker.getCount().
 */

/* ── config ──────────────────────────────────────────────── */

function resolveApiUrl() {
    const raw = window.__COUNTRR_API
        ?? document.currentScript?.dataset.api
        ?? window.location.origin;
    return String(raw).replace(/\/+$/, '');
}

const API_URL     = resolveApiUrl();
const SITE_KEY    = window.__COUNTRR_SITEKEY ?? document.currentScript?.dataset.sitekey;
const MAX_RETRIES = 2;

/* ── helpers ─────────────────────────────────────────────── */

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// Polls for the Turnstile SDK (loaded async by Cloudflare's api.js).
function waitForTurnstile(timeoutMs) {
    if (window.turnstile) return Promise.resolve();
    return new Promise((resolve, reject) => {
        const started = Date.now();
        const handle = setInterval(() => {
            if (window.turnstile) {
                clearInterval(handle);
                resolve();
            } else if (Date.now() - started > timeoutMs) {
                clearInterval(handle);
                reject(new Error('turnstile SDK timeout'));
            }
        }, 50);
    });
}

// Renders an invisible widget, runs the challenge, and resolves with the
// token (or null if the challenge could not be completed). Tokens are
// single-use, so a fresh widget is used for every call.
function getTurnstileToken(timeoutMs) {
    return new Promise((resolve) => {
        const container = document.createElement('div');
        (document.body || document.documentElement).appendChild(container);

        let settled = false;
        const finish = (token) => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            container.remove();
            resolve(token);
        };
        const timer = setTimeout(() => finish(null), timeoutMs);

        try {
            const widgetId = window.turnstile.render(container, {
                sitekey: SITE_KEY,
                size: 'invisible',
                callback: (token) => finish(token),
                'error-callback': () => finish(null),
                'timeout-callback': () => finish(null),
            });
            window.turnstile.execute(widgetId);
        } catch (err) {
            console.warn('[countrr] turnstile render failed:', err.message);
            finish(null);
        }
    });
}

/* ── public API ──────────────────────────────────────────── */

async function trackPageView() {
    let token = null;

    if (SITE_KEY) {
        try {
            await waitForTurnstile(10000);
            token = await getTurnstileToken(10000);
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

async function getCount() {
    const res = await fetch(`${API_URL}/count`, { cache: 'no-store' });
    if (!res.ok) throw new Error(`count failed: ${res.status}`);
    return parseInt(await res.text(), 10);
}

/* ── auto-tracks on load ─────────────────────────────────── */

function start() {
    if (SITE_KEY) {
        // Give the turnstile SDK a moment to initialise
        setTimeout(trackPageView, 200);
    } else {
        // No turnstile - direct beacon
        navigator.sendBeacon(`${API_URL}/increment`, new Blob([''], { type: 'text/plain' }));
    }
}

if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
} else {
    start();
}

window.Tracker = { trackPageView, getCount };
