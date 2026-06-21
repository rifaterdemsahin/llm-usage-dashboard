# LLM Usage Dashboard

Track your **Gemini**, **Claude**, and **OpenRouter** usage in one visual dashboard.
Log in once and your spend, requests, and token charts are right there.

🌐 **Live (Fly.io):** https://llm-usage-dashboard.fly.dev/

![status](https://img.shields.io/badge/deploy-fly.io-8b5cf6) ![backend](https://img.shields.io/badge/backend-Go-00ADD8) ![db](https://img.shields.io/badge/store-MongoDB%20Atlas-47A248)

---

## 🔗 Links

- **Live app:** https://llm-usage-dashboard.fly.dev/
- **Health check:** https://llm-usage-dashboard.fly.dev/healthz
- **Fly.io dashboard:** https://fly.io/apps/llm-usage-dashboard/monitoring
- **OpenRouter keys:** https://openrouter.ai/settings/keys · **activity:** https://openrouter.ai/activity
- **Anthropic Console (Claude usage):** https://console.anthropic.com/settings/usage
- **Google AI Studio (Gemini):** https://aistudio.google.com/

---

## What it does

- 🔐 **Login** with a single account; a signed **HttpOnly cookie** keeps you signed in (no re-login on the same browser).
- 📊 **Visual dashboard** — summary cards, 14-day spend line chart, cost-share doughnut, and token bar chart (Chart.js).
- ☁️ **Credentials stored in MongoDB Atlas** — your provider API keys and usage numbers persist server-side per user.
- 🔄 **Live usage pull** — OpenRouter usage is fetched **server-side** (no browser CORS limits) using the key saved in Atlas.
- ✍️ **Manual entry** for Claude & Gemini (their APIs require admin/cloud credentials and block direct browser calls).

## Architecture

| Layer | Tech |
|-------|------|
| Frontend | Single `index.html` (vanilla JS + Chart.js via CDN), embedded into the Go binary |
| Backend | Go (`net/http`), serves the page + JSON API |
| Auth | HMAC-signed session cookie; credentials checked against secrets |
| Storage | MongoDB Atlas (`MONGODB_URI`); in-memory fallback if unset |
| Hosting | Fly.io (Docker, distroless image) |

### API

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/login` | POST | Validate credentials, set session cookie |
| `/api/logout` | POST | Clear session cookie |
| `/api/me` | GET | Return current user if cookie is valid |
| `/api/settings` | GET/POST | Load/save the user's settings from MongoDB Atlas |
| `/api/usage` | GET | Pull live usage server-side using stored credentials |
| `/healthz` | GET | Health check |

## Configuration (secrets)

Set via Fly secrets (the deployment's "vault") — never committed to the repo:

| Secret | Description |
|--------|-------------|
| `DASH_USER` | Login username |
| `DASH_PASS` | Login password |
| `SESSION_SECRET` | Random key used to sign session cookies |
| `MONGODB_URI` | MongoDB Atlas connection string (`mongodb+srv://...`) |
| `MONGODB_DB` | (optional) database name, defaults to `llmdash` |

```bash
fly secrets set \
  DASH_USER='you@example.com' \
  DASH_PASS='your-password' \
  SESSION_SECRET="$(openssl rand -base64 32)" \
  MONGODB_URI='mongodb+srv://user:pass@cluster.xxxxx.mongodb.net/?retryWrites=true&w=majority'
```

## Run locally

```bash
DASH_USER='you@example.com' \
DASH_PASS='your-password' \
SESSION_SECRET='dev-secret' \
MONGODB_URI='mongodb+srv://...' \   # optional; omit to use the in-memory store
go run .
# open http://localhost:8080
```

## Deploy

```bash
fly deploy
```

The frontend is embedded into the binary via `go:embed`, so the deployed image is a single ~3 MB static binary on a distroless base.

## Security notes

- The login is a single shared account intended for personal use. Credentials live only in Fly secrets.
- Session cookies are `HttpOnly`, `Secure` (over HTTPS), and `SameSite=Lax`.
- Provider API keys are stored in **your** MongoDB Atlas database. Treat that cluster's access accordingly.
