# VGate Manager

Backend API server for **VGate** — admin/identity/billing management and the data plane that proxy nodes report into.
Written in Go. This is the **source of truth**
for the whole system: nodes, users, plans, orders, and traffic all live here, including per-user and per-node speed caps
that the proxy nodes enforce.

## Tech stack

- [Go 1.26](https://go.dev/)
- [Gin](https://github.com/gin-gonic/gin) — HTTP router/framework
- [GORM](https://gorm.io/) — ORM
- [SQLite](https://github.com/glebarez/sqlite) (default) or PostgreSQL
- [viper](https://github.com/spf13/viper) — config loading
- [cobra](https://github.com/spf13/cobra) — CLI
- [logrus](https://github.com/sirupsen/logrus) — structured logging
- [golang-jwt/jwt](https://github.com/golang-jwt/jwt) v5 — JWT signing/validation
- [telebot](https://github.com/tucnak/telebot) v4 (module `gopkg.in/telebot.v4`) — Telegram bot
- [google/uuid](https://github.com/google/uuid) + [oklog/ulid](https://github.com/oklog/ulid) v2 — ID generation
- [gorm.io/driver/postgres](https://gorm.io/docs/connecting_to_the_database.html) — PostgreSQL driver
- [gorm.io/datatypes](https://github.com/go-gorm/datatypes) — JSON config columns (nodes)
- [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) — bcrypt password hashing
- [golang.org/x/time](https://pkg.go.dev/golang.org/x/time) — rate-limiting middleware
- [go.yaml.in/yaml/v3](https://pkg.go.dev/go.yaml.in/yaml/v3) — Clash YAML subscription rendering
- Payment / email SDKs: [stripe-go](https://github.com/stripe/stripe-go),
  [go-pay/gopay](https://github.com/go-pay/gopay), [resend-go](https://github.com/resend/resend-go)

## Prerequisites

- Go **1.26+**

## Build & run

```bash
# from this directory
go build -o vgate-manager .

# run with an explicit config file (defaults to ./config.yml)
./vgate-manager --config config.yml

# or just run the default:
./vgate-manager
```

On first start the database is auto-migrated and an initial admin is bootstrapped from `admin.bootstrap` in `config.yml`
(default **username `admin`**; the bundled
`docker-compose.yml` defaults the password to `change-me`, otherwise set
`admin.bootstrap.password` / `ADMIN_BOOTSTRAP_PASSWORD` explicitly). The admin is created **only once** on first start;
subsequent starts reuse the existing admin. GORM `AutoMigrate` runs automatically on startup (idempotent), and DB-backed
system-config overrides are merged on top of `config.yml`.

### Admin CLI

Create additional admin accounts from the command line:

```bash
./vgate-manager admin create --username alice --password s3cret --role super_admin
```

`--role` is one of `admin` (default) or `super_admin`. Super admins have access to the `/admins` and plan-management
endpoints.

### CLI flags

| Flag                | Type   | Default        | Description                                                                                                                                                 |
|---------------------|--------|----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `--config`          | string | `./config.yml` | Path to the config file.                                                                                                                                    |
| `--captcha-enabled` | bool   | `false`        | Enable Cloudflare Turnstile captcha on auth endpoints (`/user/login`, `/user/register`, `/user/verify-email`, `/user/resend-verification`, `/admin/login`). |

The captcha toggle is normally a DB-backed, hot-reloadable setting (`captcha.turnstile_enabled`) that an admin flips
from the admin console. The
`--captcha-enabled` flag lets you force that switch at startup:

```bash
# turn captcha on at startup
./vgate-manager --captcha-enabled

# turn it off at startup
./vgate-manager --captcha-enabled=false
```

When the flag is **omitted**, the existing DB value is left untouched, so an admin can still toggle captcha live at
runtime. When the flag **is** passed (either
`true` or `false`), it overrides the DB value on each start. The flag only gates the Turnstile challenge — the widget's
`captcha.turnstile_site_key` and
`captcha.turnstile_secret_key` are still configured via system-config.

## Configuration (`config.yml`)

Two kinds of settings exist:

**File/env only — require a restart to change:**

| Key                        | Default                   | Notes                                                          |
|----------------------------|---------------------------|----------------------------------------------------------------|
| `server.port`              | `8081`                    | HTTP listener port                                             |
| `db.dialect`               | `sqlite`                  | `sqlite` \| `postgres`                                         |
| `db.dsn`                   | `vgate_manager.db`        | SQLite path or Postgres DSN                                    |
| `db.max_open_conns`        | `20`                      |                                                                |
| `db.max_idle_conns`        | `5`                       |                                                                |
| `jwt.secret`               | `change-me-in-production` | **Set this in production**                                     |
| `admin.bootstrap.username` | `admin`                   | used only on first run                                         |
| `admin.bootstrap.password` | _(unset)_                 | used only on first run; Docker Compose defaults to `change-me` |

**Managed in the database (hot-reloadable via `PUT /api/v1/admin/system-config`)**
— values for these in `config.yml` are **ignored**:

- **JWT:** `jwt.access_ttl_secs` (`7200`), `jwt.refresh_ttl_secs` (`604800`)
- **Logging:** `log.level` (`info`), `log.format` (`text` \| `json`)
- **CORS:** `cors.allowed_origins` (`["*"]`)
- **Server timeouts:** `server.read_timeout_secs` (`30`), `server.write_timeout_secs` (`30`)
- **Quota:** `quota.reset_day` (day-of-month the monthly usage counters reset)
- **Password policy:** `password.min_length`, `password.require_complexity`
- **Registration:** `user.register_enabled` (open registration), `user.register_require_invite`,
  `user.register_require_email_verify`, `user.register_email_suffix_whitelist`
- **Trial accounts:** `user.trial_enabled`, `user.trial_quota_bytes`, `user.trial_duration_days`
- **Invites:** `invite.default_user_quota`
- **Site / subscription:** `site.name`, `site.base_url`, `sub.base_urls` (per-node subscription base URLs),
  `payment.product_name_template`
- **Email:** `email.provider` (`smtp` \| `resend`), `email.enabled`, `email.from`, `email.from_name`, plus
  `email.smtp_*` / `email.resend_*` backend settings
- **Captcha:** `captcha.turnstile_enabled`, `captcha.turnstile_site_key`, `captcha.turnstile_secret_key`
- **Telegram:** `telegram.enabled`, `telegram.bot_token`, `telegram.bot_username`,
  `telegram.user_bot_enabled`, `telegram.alert_ticket`, `telegram.alert_announcement`,
  `telegram.alert_order_paid`, `telegram.alert_new_registration`, `telegram.alert_node_up`,
  `telegram.alert_node_down`, `telegram.alert_traffic_exceeded`
- **Payments:** `alipay.*`, `wechat.*`, `stripe.*`, `paypal.*` (client id/secret, notify/webhook urls,
  `currency`, `sandbox`), and `apple.*` (issuer id, key id, bundle id, private key, environment,
  `notify_url`, `product_map`) gateway credentials — configured from the admin console **System
  Config → Payment**
- **Traffic reminders:** `reminder.enabled`, `reminder.pct_threshold` (`80`),
  `reminder.days_threshold` (`3`), `reminder.cooldown_days` (`1`)

**Common defaults** (set on first start, overridable via system-config): `quota.reset_day` = `1`,
`password.min_length` = `8`, `invite.default_user_quota` = `5`, `user.trial_enabled` = `false`,
`user.trial_quota_bytes` = `1073741824` (1 GiB), `user.trial_duration_days` = `7`.

### Environment overrides

viper reads environment variables with `.` → `_` (uppercase), e.g.
`SERVER_PORT=9000`, `DB_DIALECT=postgres`, `JWT_SECRET=...`.

## API overview

All endpoints are prefixed with `/api/v1`. Auth uses JWT. **Admin** login (`POST /admin/login`) returns an access token
plus a refresh token, and `POST /admin/refresh` rotates the session; **user** login (`POST /user/login`) returns only an
access token — users do not receive refresh tokens, so a `401` means re-login. Admin endpoints require
`Authorization: Bearer <token>`. Node endpoints use a separate node token (`node_auth` middleware).

**Public / user**

- `POST /user/login`
- `POST /user/register`
- `POST /user/verify-email`
- `GET  /user/config`, `GET /user/dashboard` — runtime config bundle / dashboard summary
- `GET  /sub/:sub_token` — subscription info (node side)
- `POST /user/resend-verification` — resend the email-verification message (captcha-gated)
- `GET  /user/profile`, `PUT /user/profile`, `GET /user/subscribe`, `GET /user/subscribe-url`
- `GET  /user/plans`, `GET /user/nodes`
- `POST /user/regenerate-credential`, `POST /user/reset-sub-token`
- `GET  /user/traffic-packages`
- `POST /user/orders`, `GET /user/orders`, `GET /user/orders/:id`
- `POST /user/orders/:id/pay`, `POST /user/orders/:id/close`
- `POST /user/orders/:id/apple-verify` — verify an Apple App Store IAP receipt
- `GET  /user/payment-methods` — enabled payment channels for the current user
- `GET  /user/balance`, `POST /user/change-plan`, `GET /user/change-plan/preview`
  — wallet balance, prorated plan change, and proration preview
- `GET  /user/traffic`, `GET /user/traffic/hourly`
- `POST /user/change-password`
- `GET/POST/DELETE /user/invites`, `GET /user/invites/status`
- `GET  /user/announcements`
- **Support tickets** — users open, reply to, and close their own tickets, and pick how they are notified of admin
  replies:
  `GET/POST /user/tickets`, `GET /user/tickets/:id`, `POST /user/tickets/:id/messages`,
  `POST /user/tickets/:id/close`
- `GET /user/tickets/unread` — count of unread admin replies on your tickets.
- **Redemption codes** — redeem an invite/redemption code and view your redemption history:
  `POST /user/redemption-codes/redeem`, `GET /user/redemption-codes/records`.
- `PUT /user/reminder-channel` — choose the channel (`none` / `email` / `telegram`)
  for traffic-reminder alerts.
- **Telegram link (self-service)** — bind/unbind a personal Telegram account and toggle announcement delivery:
  `GET /user/telegram/status`, `POST /user/telegram/bind`,
  `POST /user/telegram/unbind`, `PUT /user/telegram/notify`
- `POST /api/v1/billing/:platform/notify` — async payment callback (public, `POST`) for `alipay`,
  `wechat`, `stripe`, `paypal`, or `apple`

**Node (data plane)**

- `GET  /server/config`, `GET /server/users`, `POST /server/traffic`

**Admin (requires `Authorization: Bearer <token>`)**

- `POST /admin/login`, `POST /admin/refresh`
- `GET  /admin/config` (public, unauthenticated)
- Nodes: `GET/POST /admin/nodes`, `GET/PUT/DELETE /admin/nodes/:id`,
  `POST /admin/nodes/:id/regenerate-token`, `GET /admin/nodes/:id/users`
- Users: `GET/POST /admin/users`, `GET/PUT/DELETE /admin/users/:id`,
  `POST /admin/users/:id/regenerate-sub-token`,
  `POST /admin/users/:id/regenerate-credential`,
  `PUT /admin/users/:id/password`, `GET /admin/users/:id/nodes`,
  `PUT /admin/users/:id/nodes`, `POST /admin/change-password`
- `GET /admin/traffic`, `GET /admin/stats/overview`
- Zombie users (super-admin only): `POST /admin/users/zombies/preview`, `POST /admin/users/zombies/cleanup`
- `GET /admin/system-config`, `PUT /admin/system-config` (super-admin only)
- `POST /admin/utils/generate-x25519`
- Invites: `GET/POST/DELETE /admin/invites`
- Redemption codes: `GET/POST /admin/redemption-codes`, `GET /admin/redemption-codes/:id/records`,
  `DELETE /admin/redemption-codes/:id`
- Announcements: `GET/POST/PUT/DELETE /admin/announcements`
- Email: `POST /admin/email/send`, `POST /admin/email/test`
- Orders: `POST /admin/orders`, `GET /admin/orders`, `GET /admin/orders/:id`, `PUT /admin/orders/:id/status`
- Traffic packages: `GET/POST/PUT/DELETE /admin/traffic-packages[/:id]`
- Tickets: `GET/POST /admin/tickets`, `GET /admin/tickets/:id`,
  `POST /admin/tickets/:id/messages`, `PUT /admin/tickets/:id/status`, `GET /admin/tickets/unread`
- Telegram: `POST /admin/telegram/broadcast` (send to all linked users), and the admin self-link
  `GET/POST /admin/me/telegram/{status,bind,unbind}`
- Reference data: `GET /admin/reference` — static lookup lists for admin dialogs.
- Payment channels: `GET /admin/payment-methods` (super-admin) — enabled payment channels.
- Wallet: `GET/POST /admin/users/:id/balance` — read / adjust a user's wallet balance.
- Super-admin only: full admin CRUD `GET/POST/PUT/DELETE /admin/admins[/:id]`,
  `PUT /admin/admins/:id/password`, and plan **management** CRUD (`POST /admin/plans`,
  `PUT/DELETE /admin/plans/:id`). Any admin may `GET /admin/plans`, `GET /admin/plans/:id`,
  and `GET /admin/traffic-packages`.

**Health**

- `GET /health`

## Email

Outbound mail (registration verification, admin broadcasts) is configured entirely via DB-backed system config — no
restart required. Two backends are supported:

- `smtp` (default) — a traditional SMTP server (`email.smtp_host` / `email.smtp_port` /
  `email.smtp_security` / `email.smtp_user` / `email.smtp_pass`).
- `resend` — the Resend API (`email.resend_api_key`).

Shared settings (both backends): `email.enabled` (master switch), `email.from` (the sender address; for Resend it must
be a verified domain), and an optional `email.from_name`
(display name, e.g. `VGate` → `"VGate" <noreply@vgate.io>`).

Verify connectivity from the admin console (**System Config → Email → General → Test Email**)
or call the endpoint directly:

- `POST /admin/email/test` — body `{ "to": "you@example.com", "subject?": "...", "body?": "..." }`. Uses the **currently
  saved** configuration (save first if you just edited settings) and returns `{ "ok": true }` on success or
  `{ "ok": false, "error": "..." }` on delivery failure.

## Registration & email verification

Registration (`POST /user/register`) is open when `user.register_enabled` is true. When
`user.register_require_email_verify` is also true, the account is held pending and a verification email is sent;
otherwise it is active immediately.

Either way the API returns a session token and the client auto-logs-in:

- `201` — account active (verified, or verification not required).
- `202` — pending verification, but the user is **already logged in**.

Email verification gates **purchases and traffic only** — an unverified user can log in and browse, but cannot place
orders and the proxy nodes will not serve their traffic until
`email_verified` is true. Completing verification (clicking the emailed link, or using the in-app resend) flips that
flag and the restriction lifts on the next node sync.

## Traffic quota

A user's traffic cap is stored as `quota_bytes` with this sentinel convention:

- `-1` — unlimited.
- `0` — no quota (access blocked; the user cannot consume traffic until granted a plan).
- `>0` — capped at that many bytes.

The manager filters the authorized users it pushes to proxy nodes accordingly, so a node never serves traffic for a
blocked or over-quota user.

## Telegram integration

The manager can run a Telegram bot that delivers alerts and announcements and lets users and admins bind their personal
accounts for ticket notifications. It is enabled and configured via DB-backed system config (`TelegramConfig`):

| Key                           | Default | Meaning                                             |
|-------------------------------|---------|-----------------------------------------------------|
| `telegram.enabled`            | `false` | Master switch for the bot.                          |
| `telegram.bot_token`          | `""`    | BotFather token (secret).                           |
| `telegram.bot_username`       | `""`    | Bot `@username`, used to build `/start` deep links. |
| `telegram.user_bot_enabled`   | `false` | Allow users to self-bind via deep link.             |
| `telegram.alert_ticket`           | `false` | Notify linked admins on new tickets / user replies. |
| `telegram.alert_announcement`     | `false` | Forward announcements to linked users.              |
| `telegram.alert_order_paid`       | `false` | Notify on paid orders.                              |
| `telegram.alert_new_registration` | `false` | Notify on new user registrations.                   |
| `telegram.alert_node_up`          | `false` | Notify when a node comes online.                    |
| `telegram.alert_node_down`        | `false` | Notify when a node goes offline.                    |
| `telegram.alert_traffic_exceeded` | `false` | Notify when a user exceeds their traffic quota.     |

Binding uses a `/start <code>` deep link. The code carries a `u_` (user) or `a_`
(admin) prefix so the bot routes the bind to the right account: admins link from **Settings → Telegram** in the admin
console, users from **Settings** in the portal.

When an admin replies to a ticket, the owner is notified on the channel they chose when opening it (`none` / `email` /
`telegram`). Every admin with a linked Telegram account also receives an alert on each new ticket and user reply.

## Support tickets

Tickets are a lightweight support channel between users and admins.

- **Users** open tickets (`POST /user/tickets`), reply, and can **close their own ticket**
  (`POST /user/tickets/:id/close`). When opening one they pick a notification method (`notify_method`: `none` |
  `email` | `telegram`); if omitted it defaults to
  `telegram` when their account is Telegram-linked, else `none`.
- **Admins** list/view all tickets, reply (`POST /admin/tickets/:id/messages`), and move them through a status machine
  `open → in_progress → resolved → closed`
  (`PUT /admin/tickets/:id/status`). A later user reply reopens a closed ticket.

Admins can also broadcast a message to every linked Telegram user via
`POST /admin/telegram/broadcast` (optionally also published as an announcement).

## CORS

Cross-origin requests are controlled by the DB-backed `cors.allowed_origins` system config (default `["*"]`). When the
admin or user frontend is deployed on a separate origin, add that origin (e.g. `https://admin.example.com`) via the
system-config endpoint so the browser will allow credentialed requests.

## Database

Defaults to a local SQLite file `vgate_manager.db`. To use PostgreSQL set
`db.dialect: postgres` and `db.dsn` to a Postgres DSN. Tables are auto-migrated on startup (admins, nodes, users,
user_nodes, user_node_traffic, traffic hourly stats, refresh tokens, system config, invite codes, email verifications,
redemption codes and records, announcements, plans — plan prices are stored as a JSON column on the plan, so there is
no separate `plan_prices` write path (the legacy `plan_prices` table is read only as a fallback for historical order
reads and is not provisioned on a fresh install) — traffic packages,
traffic grants, balance transactions, orders, tickets, ticket messages, and ticket read states, …).

## Background tasks

Several jobs run automatically (started in `cmd/root.go`):

- **Expired-order closer** — every 5 minutes (`orderSvc.CloseExpired`).
- **Hourly-stats pruning** — once at startup, then every 24 hours (deletes `traffic_hourly_stats`
  rows older than 48h).
- **Quota reset** — once at startup, then every 24 hours (resets usage counters on `quota.reset_day`).
- **Traffic-reminder scanner** — every hour (`reminderSvc.CheckAndSend`): sends threshold/days-left
  reminders on each user's chosen channel (`none` / `email` / `telegram`).

The Telegram bot (when enabled) additionally reconciles its long-poll loop every 15s and runs its
own node-up/down (1 min) and traffic-exceeded (15 min) monitors inside `internal/service/telegram.go`.
Hourly traffic is aggregated **as nodes report it** (event-driven), not by a scheduled job.

## Testing

```bash
go test ./...
go vet ./... && gofmt -l .
```
