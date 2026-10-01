# HTTP API

The web UI is a single page that talks to a JSON API. You can use the same API from scripts.

## Conventions

- All endpoints are under `/api/` (or `<base path>/api/` behind a proxy).
- Requests and responses are JSON. Errors return a non-2xx status with `{"error": "message"}`.
- Authenticate with `POST /api/login`; the response sets the `pbsm_session` cookie.
- Every request other than `GET` must include the header `X-PBSM: 1`.
- Secrets (`secret`, `password`, `keyfile_password`) can be sent but are never returned. Sending an empty value on update keeps the stored one.

```bash
B=https://nas:8099/api
curl -k -c jar -H 'X-PBSM: 1' -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"..."}' $B/login
curl -k -b jar $B/overview
```

With two-step verification on, `/login` returns `{"totp_required": true, "ticket": "..."}`; send `{"ticket", "code"}` to `/login/totp`.

## Endpoints

### Session and account

| Method | Path | Auth | Body / notes |
| --- | --- | --- | --- |
| GET | `/health` | no | `{"ok": true}` |
| GET | `/session` | no | Current user (or null), host, version |
| POST | `/login` | no | `username`, `password` |
| POST | `/login/totp` | no | `ticket`, `code` (6 digits or a recovery code) |
| POST | `/logout` | no | |
| GET | `/account` | yes | Username, two-step status, recovery codes left |
| PUT | `/account/username` | yes | `username`, `password` |
| POST | `/account/password` | yes | `current`, `new` (10+ characters) |
| POST | `/account/totp/setup` | yes | `password`. Returns `secret`, `uri`, `qr_svg` |
| POST | `/account/totp/enable` | yes | `code`. Returns `recovery_codes` |
| POST | `/account/totp/disable` | yes | `password`, `code` |
| POST | `/account/totp/recovery` | yes | `password`, `code`. Returns new `recovery_codes` |

### Dashboard

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/overview` | Jobs with recent runs, next run and sizes; `sizes` totals; `destinations` with usage |
| POST | `/sizes/refresh` | `what`: `all`, `sources`, `destinations` or `backups` |

### Destinations

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/targets` | List, with `repository`, `secret_set` and cached `usage` |
| POST | `/targets` | `name`, `host`, `port`, `datastore`, `username`, `token_name`, `secret`, `fingerprint` |
| PUT | `/targets/{id}` | Same fields |
| DELETE | `/targets/{id}` | 409 if a job uses it |
| POST | `/targets/test` | Same fields (plus optional `id` to reuse the stored secret). Returns `total`, `used`, `avail` |

### Jobs and runs

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/jobs` | List |
| POST | `/jobs` | `name`, `target_id`, `backup_id`, `shares` (`[{path, archive}]`), `excludes` (list or newline text), `schedule` (`{type: manual\|daily\|hourly, time, days, interval_hours}`), `change_detection_mode`, `rate`, `keyfile`, `keyfile_password`, `enabled` |
| PUT | `/jobs/{id}` | Same fields |
| DELETE | `/jobs/{id}` | 409 while running |
| POST | `/jobs/{id}/run` | Queue a run. `created` is false if one is already queued or running |
| GET | `/jobs/{id}/snapshots` | Snapshots on PBS for the job's group |
| GET | `/runs?job=&limit=` | Run history, newest first |
| GET | `/runs/{id}` | One run |
| GET | `/runs/{id}/log?offset=` | Log text from byte `offset`; returns new `offset` and `done` |
| POST | `/runs/{id}/cancel` | Cancel a queued or running run |

### Email, settings and files

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/email` | Alert settings |
| PUT | `/email` | `enabled`, `host`, `port`, `security` (`starttls`, `ssl`, `none`), `username`, `password`, `from_addr`, `to_addrs`, `notify_failure`, `notify_success` |
| POST | `/email/test` | Same fields; sends a test message |
| GET | `/config/export` | Settings without credentials |
| POST | `/config/import` | `config` (an export), `apply` (false for a preview). Returns a `summary` |
| GET | `/browse?path=` | Subfolder names of `path` |

## Run object

```json
{
  "id": "d90166b1f2624b69", "job_id": "3dd6e5befdea", "job_name": "media",
  "target_name": "Home PBS", "trigger": "schedule",
  "status": "success", "queued_at": 1790812450.9, "started": 1790812451.0,
  "ended": 1790812890.2, "exit_code": 0, "summary": "Backup finished.", "email_error": ""
}
```

`status` is one of `queued`, `running`, `success`, `failed`, `cancelled`. Times are Unix timestamps.
