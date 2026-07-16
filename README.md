# VGate Manager

Backend API server for **VGate** — admin/identity/billing management and the data
plane that proxy nodes report into. Written in Go. This is the **source of truth**
for the whole system: nodes, users, plans, orders, and traffic all live here,
including per-user and per-node speed caps that the proxy nodes enforce.

## Tech stack

- [Go 1.26](https://go.dev/)
- [Gin](https://github.com/gin-gonic/gin) — HTTP router/framework
- [GORM](https://gorm.io/) — ORM
- [SQLite](https://github.com/glebarez/sqlite) (default) or PostgreSQL
- [viper](https://github.com/spf13/viper) — config loading
- [cobra](https://github.com/spf13/cobra) — CLI
- [logrus](https://github.com/sirupsen/logrus) — structured logging

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

On first start the database is auto-migrated and an initial admin is bootstrapped
from `admin.bootstrap` in `config.yml` (defaults: **username `admin`**,
**password `change-me`**). The generated password is printed **only once** at
startup — save it. Subsequent starts reuse the existing admin. Data migrations run
automatically on startup (idempotent), and DB-backed system-config overrides are
merged on top of `config.yml`.

### Admin CLI

Create additional admin accounts from the command line:

```bash
./vgate-manager admin create --username alice --password s3cret --role super_admin
```

`--role` is one of `admin` (default) or `super_admin`. Super admins have access to
the `/admins` and plan-management endpoints.

## Configuration (`config.yml`)

Two kinds of settings exist:

**File/env only — require a restart to change:**

| Key                        | Default                   | Notes                       |
|----------------------------|---------------------------|-----------------------------|
| `server.port`              | `8081`                    | HTTP listener port          |
| `db.dialect`               | `sqlite`                  | `sqlite` \| `postgres`      |
| `db.dsn`                   | `vgate_manager.db`        | SQLite path or Postgres DSN |
| `db.max_open_conns`        | `20`                      |                             |
| `db.max_idle_conns`        | `5`                       |                             |
| `jwt.secret`               | `change-me-in-production` | **Set this in production**  |
| `admin.bootstrap.username` | `admin`                   | used only on first run      |
| `admin.bootstrap.password` | `change-me`               | used only on first run      |

**Managed in the database (hot-reloadable via `PUT /api/v1/admin/system-config`)**
— values for these in `config.yml` are **ignored**:

- `jwt.access_ttl_secs` (`7200`)
- `jwt.refresh_ttl_secs` (`604800`)
- `log.level` (`info`), `log.format` (`text` \| `json`)
- `cors.allowed_origins` (`["*"]`)
- `server.read_timeout_secs` (`30`), `server.write_timeout_secs` (`30`)

### Environment overrides

viper reads environment variables with `.` → `_` (uppercase), e.g.
`SERVER_PORT=9000`, `DB_DIALECT=postgres`, `JWT_SECRET=...`.

## API overview

All endpoints are prefixed with `/api/v1`. Auth uses JWT access + refresh tokens.
Login returns both; `/admin/refresh` rotates a session. Admin endpoints require
`Authorization: Bearer <token>`. Node endpoints use a separate node token
(`node_auth` middleware).

**Public / user**

- `POST /user/login`
- `POST /user/register`
- `POST /user/verify-email`
- `GET  /user/config`
- `GET  /sub/:sub_token` — subscription info (node side)
- `GET  /user/profile`, `GET /user/subscribe`, `GET /user/subscribe-url`
- `GET  /user/plans`, `GET /user/nodes`
- `POST /user/regenerate-credential`, `POST /user/reset-sub-token`
- `GET  /user/traffic-packages`
- `POST /user/orders`, `GET /user/orders`, `GET /user/orders/:id`
- `POST /user/orders/:id/pay`, `POST /user/orders/:id/close`
- `GET  /user/traffic`, `GET /user/traffic/hourly`
- `POST /user/change-password`
- `GET/POST/DELETE /user/invites`, `GET /user/invites/status`
- `GET  /user/announcements`
- `POST /api/v1/billing/:platform/notify` — async payment callback (public, `POST`) for `alipay`, `wechat`, or `stripe`

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
- `GET /admin/system-config`, `PUT /admin/system-config`
- `POST /admin/utils/generate-x25519`
- Invites: `GET/POST/DELETE /admin/invites`
- Announcements: `GET/POST/PUT/DELETE /admin/announcements`
- Email: `POST /admin/email/send`
- Orders: `POST /admin/orders`, `GET /admin/orders`, `GET /admin/orders/:id`
- Traffic packages: `GET/POST/PUT/DELETE /admin/traffic-packages[/:id]`
- Super-admin only: `GET/POST /admin/admins`, `PUT /admin/admins/:id/password`,
  plan CRUD (`GET/POST/PUT/DELETE /admin/plans/:id`, `GET /admin/plans`)

**Health**

- `GET /health`

## CORS

Cross-origin requests are controlled by the DB-backed `cors.allowed_origins` system
config (default `["*"]`). When the admin or user frontend is deployed on a separate
origin, add that origin (e.g. `https://admin.example.com`) via the system-config
endpoint so the browser will allow credentialed requests.

## Database

Defaults to a local SQLite file `vgate_manager.db`. To use PostgreSQL set
`db.dialect: postgres` and `db.dsn` to a Postgres DSN. Tables are auto-migrated on
startup (admins, nodes, users, plans, orders, traffic stats, refresh tokens, system
config, …).

## Testing

```bash
go test ./...
go vet ./... && gofmt -l .
```
