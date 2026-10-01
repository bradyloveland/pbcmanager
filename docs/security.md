# Security

Backup Manager holds the credentials to your backup server, so it's designed to keep them on the machine and to make sign-in hard to abuse.

## Credentials at rest

- All settings, including token secrets, the SMTP password and key file passwords, live in `/etc/pbs-manager/config.json`, written with mode 600 and owned by root. The service runs with `UMask=0077`.
- Secrets are **write-only** in the API and UI. Once saved they're never returned to the browser; responses only say whether one is set.
- Exports never include credentials (see [Export and import](usage.md#export-and-import-settings)).
- The admin password is stored as PBKDF2-SHA256 with 310,000 iterations and a random salt.
- Recovery codes are stored only as SHA-256 hashes. They're 10 random characters from a 31-character alphabet (about 50 bits each), so a hash is not practical to reverse.
- The TOTP secret is stored in the settings file because the server must compute codes from it. Protect the file like any credential store.

## Least privilege on PBS

Use a dedicated API token with only the `DatastoreBackup` role (see [Preparing Proxmox Backup Server](pbs-setup.md)). It can add backups but not delete them, so if the NAS is compromised, existing backup history on PBS stays intact.

## Sign-in

- **Two-step verification**: RFC 6238 TOTP, SHA-1, 6 digits, 30-second steps. Codes from the previous or next step are accepted for clock drift. Each step can be used once; replaying a code fails.
- **Enrollment** happens after re-entering the password. The QR code is generated on the server; nothing is sent to third parties.
- **Recovery codes**: 10 single-use codes. Generating a new set cancels the old ones.
- **Throttling**: after 5 failed attempts from one address, that address waits 60 seconds. After 20 failures in total, all sign-ins pause for 5 minutes. Each failure also costs one second. Password and code failures both count.
- **Second-factor tickets** expire after 5 minutes and after 5 wrong codes.
- Failed attempts are logged with the client address, suitable for fail2ban.

## Sessions

- Random 256-bit tokens in an `HttpOnly`, `SameSite=Strict` cookie, `Secure` over HTTPS (directly or via a trusted proxy).
- Expire after 12 hours idle. Stored in memory, so restarting the service signs everyone out.
- Changing the password or turning on two-step verification signs out every other session.

## Request protection

- Every state-changing request must carry an `X-PBSM: 1` header, which browsers won't add on cross-site requests. Combined with `SameSite=Strict`, this blocks cross-site request forgery.
- Strict `Content-Security-Policy`, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and `Cache-Control: no-store`.
- Request bodies are limited to 1 MB.
- All user content is escaped before rendering.

## Network

- The default install serves HTTPS with a self-signed certificate (TLS 1.2+).
- Behind a reverse proxy, forwarded headers are trusted only from configured proxy addresses.
- Prefer a VPN over exposing the port to the internet.

## The folder browser

The folder picker can list directory names anywhere on the machine (not file contents). It's only available to the signed-in admin, who by definition can already configure backups of any folder.

## Reporting a vulnerability

Please open a private security advisory on the GitHub repository rather than a public issue.
