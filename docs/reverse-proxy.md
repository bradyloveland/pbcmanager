# Running behind a reverse proxy

Install or upgrade with one of:

```bash
sudo ./install.sh --behind-proxy                    # proxy on the same machine
sudo ./install.sh --proxy-ip 192.168.1.10           # proxy on another machine or container
sudo ./install.sh --behind-proxy --base-path /backups   # under a sub-path
```

With a trusted proxy configured, the app:

- Takes the client address from `X-Forwarded-For` (or `X-Real-IP`) for logging and sign-in throttling. These headers are only believed when the request comes directly from an address in `trusted_proxies`, so other clients can't spoof them.
- Marks the session cookie `Secure` when the proxy sends `X-Forwarded-Proto: https`.
- Accepts requests with or without the base path, so it works whether your proxy strips the prefix or passes it through. A request for the bare base path (`/backups`) is redirected to `/backups/`.
- Answers health checks at `/api/health` without signing in.

The page polls every few seconds rather than using WebSockets, so no special proxy settings are needed. Let the proxy handle HTTPS.

## Nginx

Own subdomain:

```nginx
server {
    listen 443 ssl http2;
    server_name backups.example.com;
    # ssl_certificate ...; ssl_certificate_key ...;

    location / {
        proxy_pass http://127.0.0.1:8099;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Sub-path (install with `--base-path /backups`):

```nginx
location /backups/ {
    proxy_pass http://127.0.0.1:8099;   # no trailing slash: /backups/ is passed through
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

## Caddy

```caddy
backups.example.com {
    reverse_proxy 127.0.0.1:8099
}
```

Caddy sets the forwarding headers and gets a certificate automatically.

## Nginx Proxy Manager

Add a proxy host with scheme `http`, the NAS's IP and port 8099, then request a certificate on the SSL tab. Install Backup Manager with `--proxy-ip <NPM's IP>`.

If NPM runs in Docker on the same machine, its requests come from the Docker bridge rather than localhost. Use `--proxy-ip 172.16.0.0/12`, which covers Docker's default networks, or the specific bridge gateway (often `172.17.0.1`).

## Traefik

Route your host to `http://<nas-ip>:8099` and install with `--proxy-ip <Traefik's IP>`. Traefik sends `X-Forwarded-*` headers by default. For a sub-path, either strip the prefix with a `stripPrefix` middleware or set `--base-path`.

## Checking it works

```bash
journalctl -u pbs-manager -n 20
```

On start the log lists the trusted proxy addresses and base path. Failed sign-ins are logged with the client's real address; if they show the proxy's address instead, the proxy's IP isn't in `trusted_proxies`.

## A note on exposure

Even with HTTPS, two-step verification and throttling, this app holds the credentials to your backup server. Prefer reaching it over a VPN (WireGuard, Tailscale) rather than publishing it to the internet.
