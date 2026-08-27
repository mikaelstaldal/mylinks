# MyLinks — Operations Guide

This guide covers production installation of MyLinks on a Linux server, including TLS termination via a reverse proxy (e.g. nginx) and systemd service management.

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Install the Binary](#install-the-binary)
3. [Create a System User](#create-a-system-user)
4. [Set Up the Data Directory](#set-up-the-data-directory)
5. [Set Up Authentication](#set-up-authentication)
6. [Configure systemd](#configure-systemd)
7. [Configure a Reverse Proxy](#configure-a-reverse-proxy)
8. [First Login](#first-login)
9. [Screenshots](#screenshots)
10. [Upgrading](#upgrading)
11. [Firewall](#firewall)

---

## Prerequisites

- A Linux server.
- nginx (or another reverse proxy capable of TLS termination).
- A valid TLS certificate for your domain (e.g. from Let's Encrypt).
- Go if building from source, at least the version in the `go` directive of
  `go.mod`; otherwise download a pre-built binary or use the Docker image.

---

## Install the Binary

### Build from source

```bash
git clone https://github.com/mikaelstaldal/mylinks.git
cd mylinks
go build -tags netgo -v ./cmd/mylinks/
```

> **Note:** A standalone build (without Docker) does not have screenshot support — that requires the headless Chrome browser bundled in the Docker image. See [Screenshots](#screenshots) below.

### Run with Docker instead

If you want screenshot support, run the provided Docker image instead of a standalone binary — see the README for `docker run` examples. The systemd setup below assumes a standalone binary; adapt `ExecStart` to `docker run ...` (or use a Docker-aware unit) if you go that route.

---

## Create a System User

Run mylinks as a dedicated non-root user.

```bash
useradd --system --home-dir /var/lib/mylinks --shell /usr/sbin/nologin mylinks
```

---

## Set Up the Data Directory

```bash
mkdir -p /var/lib/mylinks/data
chown -R mylinks:mylinks /var/lib/mylinks
chmod 0700 /var/lib/mylinks /var/lib/mylinks/data
```

mylinks creates `mylinks.sqlite` (and a `screenshots/` subdirectory, if screenshot support is enabled) in the data directory on first startup.

The database runs in WAL mode, so `mylinks.sqlite-wal` and `mylinks.sqlite-shm` appear alongside it while mylinks is running. Copying `mylinks.sqlite` on its own is not a complete backup; either stop mylinks first or use `sqlite3 mylinks.sqlite ".backup backup.sqlite"`.

---

## Set Up Authentication

mylinks uses HTTP Basic Auth backed by an htpasswd file (bcrypt). Create the file as the `mylinks` user:

```bash
htpasswd -Bc /etc/mylinks/htpasswd myuser
```

Protect the file:

```bash
chown mylinks:mylinks /etc/mylinks/htpasswd
chmod 0600 /etc/mylinks/htpasswd
```

mylinks reads this file once at startup and validates it strictly: every
non-blank line has to be a `username:bcrypt-hash` pair, usernames have to be
unique, and the file cannot be empty. Anything else aborts startup, naming the
file and, where there is one, the offending line number, rather than leaving a
login the operator believes in silently non-existent. A file written by
`htpasswd -B` and nothing else satisfies this; note that `#` does not start a
comment, since it is a legal first character of a username.

> **Important:** HTTP Basic Auth must only be used over HTTPS. Never expose mylinks on a non-loopback interface without TLS. The reverse proxy (see below) provides TLS termination.

---

## Configure systemd

Create `/etc/systemd/system/mylinks.service`:

```ini
[Unit]
Description=MyLinks
After=network.target

[Service]
Type=exec
User=mylinks
Group=mylinks

LoadCredential=basic-auth:/etc/mylinks/htpasswd

ExecStart=/usr/local/bin/mylinks \
    -data /var/lib/mylinks/data \
    -addr 127.0.0.1 \
    -port 8080 \
    -public-url https://links.example.com \
    -basic-auth-file ${CREDENTIALS_DIRECTORY}/basic-auth \
    -basic-auth-realm mylinks

Restart=on-failure
RestartSec=5

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
PrivateTmp=true
ReadWritePaths=/var/lib/mylinks

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
systemctl daemon-reload
systemctl enable mylinks
systemctl start mylinks
systemctl status mylinks
```

View logs:

```bash
journalctl -u mylinks -f
```

---

## Configure a Reverse Proxy

mylinks does not terminate TLS itself. Place it behind a reverse proxy.

Start mylinks with `-public-url https://links.example.com`. The CSRF middleware rejects state-changing requests (POST/PATCH/DELETE) whose `Origin` or `Referer` does not match the configured public URL, so this must match the externally visible URL exactly (scheme and host).

One requirement regardless of which reverse proxy you use:

- **Rate limiting** — mylinks has no built-in rate limiting. The reverse proxy must enforce a per-IP request rate limit, especially since adding a link triggers an outbound fetch (and optionally a headless-browser render) of the target URL.

### nginx

Create `/etc/nginx/sites-available/mylinks`:

```nginx
server {
    listen 80;
    server_name links.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name links.example.com;

    ssl_certificate     /etc/letsencrypt/live/links.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/links.example.com/privkey.pem;

    # Modern TLS settings
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers off;

    # Rate limiting (adjust as needed)
    limit_req_zone $binary_remote_addr zone=mylinks:10m rate=10r/s;
    limit_req zone=mylinks burst=20 nodelay;

    location / {
        proxy_pass http://127.0.0.1:8080;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Enable and test:

```bash
ln -s /etc/nginx/sites-available/mylinks /etc/nginx/sites-enabled/mylinks
nginx -t
systemctl reload nginx
```

#### TLS certificate (Let's Encrypt)

```bash
certbot --nginx -d links.example.com
```

Certbot will modify the nginx config to handle certificate renewal automatically.

### Apache 2

Requires `mod_proxy`, `mod_proxy_http`, `mod_ratelimit`, `mod_ssl`, and `mod_headers`. Enable them with:

```bash
a2enmod proxy proxy_http ratelimit ssl headers
```

```apache
<VirtualHost *:443>
    ServerName links.example.com

    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/links.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/links.example.com/privkey.pem

    ProxyPreserveHost On
    ProxyPass        / http://127.0.0.1:8080/
    ProxyPassReverse / http://127.0.0.1:8080/

    RequestHeader set X-Forwarded-Proto "https"

    # Rate limiting: 10 requests/second per connection
    <Location />
        SetOutputFilter RATE_LIMIT
        SetEnv rate-limit 10
    </Location>
</VirtualHost>

# Redirect HTTP to HTTPS
<VirtualHost *:80>
    ServerName links.example.com
    Redirect permanent / https://links.example.com/
</VirtualHost>
```

> **Note:** Apache's `mod_ratelimit` limits the *response* throughput (bytes/sec), not the request rate. For true per-IP request-rate limiting use `mod_qos` (available as a package on most distributions: `apt install libapache2-mod-qos`) and add `QS_SrvMaxConnPerIP 10` to the VirtualHost block.

### Caddy

```caddy
links.example.com {
    # Rate limiting (requires caddy-ratelimit module)
    rate_limit {remote.ip} 10r/s

    reverse_proxy 127.0.0.1:8080
}
```

> **Note:** The built-in Caddy distribution does not include a rate-limiting module. Build Caddy with `xcaddy build --with github.com/mholt/caddy-ratelimit`, or use nginx if you prefer not to build a custom binary.

---

## First Login

Open `https://links.example.com` in your browser. Log in with the username and password you set in the htpasswd file.

From the web interface, you can add links and notes, search your saved items, and delete them. See the README for details on the web interface and API endpoints.

---

## Screenshots

Screenshot extraction requires a headless Chrome browser, which is only available in the Docker image (it bundles `chromedp/headless-shell` and sets the `CHROMEDP` environment variable for mylinks to connect to).

A standalone binary run outside Docker (as in the systemd setup above) will save links with title and description but **without** screenshots — the `CHROMEDP` environment variable is unset, so the screenshot/headless-browser code path is skipped entirely.

If you need screenshots in a systemd-managed deployment, run the Docker image instead (e.g. via a `docker run` `ExecStart`), or run `headless-shell` as a separate service and set `CHROMEDP=wss://127.0.0.1:9222` (or similar) in the mylinks unit's `Environment=`.

---

## Upgrading

1. Build or download the new binary.
2. Stop the service:
   ```bash
   systemctl stop mylinks
   ```
3. Replace the binary:
   ```bash
   install -o root -g root -m 0755 mylinks-new /usr/local/bin/mylinks
   ```
4. Start the service:
   ```bash
   systemctl start mylinks
   ```
5. Check the logs for any startup errors:
   ```bash
   journalctl -u mylinks -n 50
   ```

---

## Firewall

mylinks binds to `127.0.0.1` by default and is never directly exposed to the internet. Ensure your firewall allows:

| Port | Protocol | Purpose                                  |
|------|----------|------------------------------------------|
| 80   | TCP      | HTTP → redirect to HTTPS (reverse-proxy) |
| 443  | TCP      | HTTPS (reverse-proxy → mylinks)        |

The mylinks process itself (port 8080) must not be reachable from outside the server.
