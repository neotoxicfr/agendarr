<div align="center">

<img src="web/static/icons/icon.svg" width="96" height="96" alt="">

# Agendarr

**The release calendar for your Plex household.**

Radarr, Sonarr and Seerr in one calendar. Everyone signs in with their Plex account
and sees everything that's coming, or just what they care about.

[![CI](https://github.com/neotoxicfr/agendarr/actions/workflows/ci.yml/badge.svg?branch=dev)](https://github.com/neotoxicfr/agendarr/actions/workflows/ci.yml)
[![Release](https://img.shields.io/badge/release-v0.4.0-blue)](https://github.com/neotoxicfr/agendarr/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

<picture>
  <source media="(prefers-color-scheme: light)" srcset="docs/screenshots/desktop-month-light.png">
  <img src="docs/screenshots/desktop-month-dark.png" alt="Agendarr month view on a desktop: movies and episodes of October, with the filter panel on the left">
</picture>

## Why Agendarr

Radarr and Sonarr each have a calendar, but each one only covers its own instance,
and only the admin can open them. Agendarr gives the whole household one calendar:

- **Nothing to set up for your users.** Anyone you share your Plex server with
  signs in with Plex. No accounts or invitations to create.
- **Everyone gets their own view.** *My Seerr requests*, *shows I'm watching*,
  *my Plex watchlist*, *everything I've watched*: each person ticks what they want.
- **It goes where people already look.** A personal iCal link puts releases in
  Apple Calendar, Google Calendar or Outlook, with that person's filters applied.

<table>
  <tr>
    <td width="33%"><img src="docs/screenshots/phone-agenda.png" alt="Agenda view on a phone, with posters, episode codes and status"></td>
    <td width="33%"><img src="docs/screenshots/phone-detail.png" alt="Release details in a bottom sheet on a phone"></td>
    <td width="33%"><img src="docs/screenshots/phone-sync.png" alt="Calendar subscription page with Apple, Google and Outlook buttons"></td>
  </tr>
  <tr>
    <td align="center">Agenda on a phone</td>
    <td align="center">Release details</td>
    <td align="center">Subscribe in your calendar app</td>
  </tr>
</table>

![Week view on a desktop, with posters and availability badges](docs/screenshots/desktop-week.png)

## Features

- **Every instance in one place.** Add as many Radarr and Sonarr instances as you
  run (4K, anime…); a release seen by several of them shows once. Movies show
  their cinema, digital and physical releases. Episodes show their air time in
  each viewer's time zone, and season or series finales are flagged.
- **Plex sign-in.** On first run, the owner of the Plex server becomes the
  administrator and their server is bound automatically. Access is re-checked
  every day, so someone you stop sharing with loses access too.
- **What you will get, before you press play.** An available release shows its
  quality, HDR, video and audio formats as Plex read them from the file (as
  logos or text), and every audio and subtitle track (language as a flag,
  title, forced, SDH). One click opens it in
  Plex; administrators also get Radarr or Sonarr. TMDB, TVDB, IMDb and
  Letterboxd links, each person picking the ones they use.
- **Notifications.** Be told when something you requested, are watching or
  follow from its detail sheet becomes available, or when its file is
  upgraded (1080p to 4K, a French track added…). A bell keeps the history;
  notifications reach the device too (Web Push), even with the app closed.
- **Personal filters**, which can be combined: Seerr requests, shows watched in
  the last 90 days, Plex watchlist, full Plex history. Every release shows who
  requested it.
- **Made for desktop and phone alike.** On desktop: month, week and agenda views,
  keyboard shortcuts and a filter panel. On phones: a bottom tab bar, swipe
  between periods and bottom sheets. Installable as an app (PWA) and readable
  offline.
- **Calendar subscription.** A private iCal link per person, which they can revoke
  at any time.
- **Accessible.** Checked against WCAG 2.2 AA with axe on every page. Fully
  usable with a keyboard, labelled for screen readers, and it respects reduced
  motion.
- **Light and dark**, following the system theme. **English and French**, taken
  from the browser or picked in the footer.
- **Small and private.** A single Go binary in a distroless image (amd64, arm64,
  armv7) and one SQLite file. API keys never reach the browser, and the server
  only talks to your own servers and plex.tv.

## Requirements

- A **Plex Media Server**. You sign in with the account that owns it.
- **Radarr** and/or **Sonarr** (API v3; tested with Radarr 6 and Sonarr 4).
- Optional: **Seerr** (or Overseerr / Jellyseerr) for the *my requests* filter
  and the *requested by* names.
- A reverse proxy with **HTTPS** if you want the app to install on phones and
  calendar apps to subscribe.

## Quick start

```yaml
# docker-compose.yml
services:
  agendarr:
    image: ghcr.io/neotoxicfr/agendarr:latest
    container_name: agendarr
    restart: unless-stopped
    environment:
      TZ: Europe/Paris
      AGENDARR_BASE_URL: https://agenda.example.com
      # Optional: how Agendarr reaches Plex for the history-based filters.
      # AGENDARR_PLEX_SERVER_URL: http://plex:32400
    volumes:
      - ./data:/data
    ports:
      - "8080:8080"
    read_only: true
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
```

```bash
docker compose up -d
```

Then open `https://agenda.example.com`. [`deploy/compose.yml.example`](deploy/compose.yml.example)
has the same file with comments.

### First run

1. **Sign in with the Plex account that owns the server.** It becomes the
   administrator. If it owns several servers, pick one. Do this right after
   starting the container, before sharing the URL: until then, the first
   person to sign in with a Plex server of their own becomes the administrator.
2. In **Admin › Instances**, add **Seerr**, then click **Import Radarr and Sonarr**:
   Agendarr copies the servers Seerr already knows, API keys included. You can
   also add Radarr and Sonarr by hand (the API key is in *Settings › General* of
   each app). **Test connection** checks each one before saving.
3. **Share the URL** with your Plex users. They sign in with Plex, and that's it.

<p align="center">
  <img src="docs/screenshots/desktop-admin.png" width="80%" alt="Admin page listing the Radarr, Sonarr and Seerr instances, with an Import Radarr and Sonarr button">
</p>

### Behind a reverse proxy

Agendarr is a plain HTTP server on port 8080. Leave compression to the proxy.

<details>
<summary>Caddy</summary>

```caddy
agenda.example.com {
    encode zstd gzip
    reverse_proxy agendarr:8080
}
```
</details>

<details>
<summary>Traefik (labels)</summary>

```yaml
    labels:
      traefik.enable: "true"
      traefik.http.routers.agendarr.rule: Host(`agenda.example.com`)
      traefik.http.routers.agendarr.entrypoints: websecure
      traefik.http.routers.agendarr.tls.certresolver: letsencrypt
      traefik.http.services.agendarr.loadbalancer.server.port: "8080"
```
</details>

<details>
<summary>nginx</summary>

```nginx
server {
    listen 443 ssl;
    server_name agenda.example.com;
    # ssl_certificate …

    location / {
        proxy_pass http://agendarr:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```
</details>

Calendar apps fetch the iCal link from their own servers, so don't put an IP
allowlist in front of `/ics/`, or Google and Outlook won't be able to read it.

## Configuration

Everything else is set up in the app. The environment only covers what the
app can't work out by itself:

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENDARR_BASE_URL` | derived from requests | Public URL, used for the Plex sign-in return and iCal links. Set it behind a proxy. |
| `AGENDARR_PLEX_SERVER_URL` | addresses advertised by plex.tv | How Agendarr reaches Plex for the history-based filters, e.g. `http://plex:32400` on a shared Docker network |
| `AGENDARR_LISTEN` | `:8080` | HTTP listen address |
| `AGENDARR_DATA_DIR` | `/data` | Database, encryption key, poster cache |
| `AGENDARR_SECURE_COOKIES` | `true` when the base URL is https | Force the `Secure` cookie flag on or off |
| `TZ` | UTC | Server time zone (logs). Each viewer sees times in their own time zone. |

## Updating and backups

```bash
docker compose pull && docker compose up -d
```

Database migrations run on start. To back up, copy the data volume while the
container is stopped. Keep `secret.key` with `agendarr.db`: tokens and API
keys are encrypted with it, and a database without its key can't be read.

## Troubleshooting

- **"Shows I'm watching" or "Everything I've watched" says Plex is unreachable.**
  Agendarr reads the history on your Plex server itself. Set
  `AGENDARR_PLEX_SERVER_URL` to an address the container can reach, such as
  `http://plex:32400`.
- **"My Seerr requests" says no account is linked.** The person has never signed
  in to Seerr with Plex, so Seerr doesn't know them.
- **Google Calendar shows nothing yet.** Google refreshes subscribed calendars
  every 12 to 24 hours. Apple and Outlook refresh about every hour.
- **The app doesn't offer to install.** Installing requires HTTPS.

## How it works

- Radarr and Sonarr calendars are read with their API keys and cached per month
  for five minutes. An unreachable instance keeps its last known dates and is
  named in a warning. Posters go through Agendarr, so API keys never reach the
  browser.
- Seerr requests are indexed every ten minutes. Plex history is read from your
  server with the owner's token and resolved to TMDB/TVDB/IMDb ids once.
- Everything lives in one SQLite file. Plex tokens and API keys are encrypted
  with AES-256-GCM. See [SECURITY.md](SECURITY.md) and the in-app privacy page.

## Development

Requires Go (see [`go.mod`](go.mod)).

```bash
make dev         # http://localhost:8080, data in ./data
make test-race
make lint
```

Contributions are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md). Report
security issues privately as described in [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE). Country flags come from [Twemoji](https://github.com/twitter/twemoji)
(CC-BY 4.0) through [country-flag-emoji-polyfill](https://github.com/talkjs/country-flag-emoji-polyfill)
(MIT), see [`web/static/fonts`](web/static/fonts/LICENSE.md). Format logos (Dolby, DTS,
HDR10) come from Wikimedia Commons and remain trademarks of their owners, see
[`web/static/logos`](web/static/logos/LICENSE.md).
