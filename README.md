<h1 align="center">OmniWA GO</h1>

<p align="center">
  High-performance WhatsApp API built in Go.
</p>

<p align="center">
  <a href="https://opensource.org/licenses/Apache-2.0"><img src="https://img.shields.io/badge/License-Apache%202.0-blue.svg" alt="License: Apache 2.0" /></a>
</p>

> **OmniWA GO** is a downstream fork of
> [Evolution Go](https://github.com/evolution-foundation/evolution-go) (Apache-2.0),
> maintained independently. It is not affiliated with or endorsed by Evolution
> Foundation. See [Attribution](#attribution) and [docs/SYNC.md](./docs/SYNC.md).

---

## About

**OmniWA GO** is a high-performance WhatsApp API built in Go. It provides a
robust, lightweight solution for WhatsApp integration using the
[whatsmeow](https://github.com/tulir/whatsmeow) library.

---

## Features

- **High performance** — built with Go for minimal resource usage
- **RESTful API** — clean, well-documented REST endpoints with Swagger
- **Real-time events** — WebSocket, Webhook, AMQP/RabbitMQ and NATS support
- **Media support** — images, videos, audio, documents with MinIO/S3 storage
- **Message storage** — optional PostgreSQL persistence
- **QR code pairing** — built-in QR code generation for device linking
- **Docker ready** — production-ready Docker configuration

---

## Quick Start

### Docker (recommended)

```bash
git clone https://github.com/hiepknor/omniwa-go.git
cd omniwa-go
make docker-build
make docker-run
```

### Local development

```bash
git clone https://github.com/hiepknor/omniwa-go.git
cd omniwa-go

# Setup, configure and run
make setup
cp .env.example .env
make dev
```

> Run `make help` to see all available commands. See [COMMANDS.md](./COMMANDS.md) for detailed workflows.

---

## Configuration

Create a `.env` file (see [.env.example](./.env.example)):

```env
# Server
SERVER_PORT=8080
CLIENT_NAME=omniwa

# Security
GLOBAL_API_KEY=your-secure-api-key-here

# Database
POSTGRES_AUTH_DB=postgresql://postgres:password@localhost:5432/omniwa_auth?sslmode=disable
POSTGRES_USERS_DB=postgresql://postgres:password@localhost:5432/omniwa_users?sslmode=disable
DATABASE_SAVE_MESSAGES=false

# License gate (see below). Set to false to run without activation.
# LICENSE_GATE_ENABLED=true
```

| Variable | Description | Default |
|---|---|---|
| `SERVER_PORT` | Server port | `8080` |
| `CLIENT_NAME` | Client identifier | `omniwa` |
| `GLOBAL_API_KEY` | API authentication key | **Required** |
| `DATABASE_SAVE_MESSAGES` | Enable message storage | `false` |
| `LICENSE_GATE_ENABLED` | Enable the license activation gate | `true` |

---

## License Gate

The upstream project ships a license activation gate: the API is blocked
(`503`) until the instance is activated against the licensing server, and a
periodic heartbeat is sent while running.

OmniWA GO keeps this behavior by default, but makes it opt-out:

- **`LICENSE_GATE_ENABLED=true` (default)** — the gate is active. On first run,
  open the **Manager** at `http://localhost:8080/manager/login`, enter your API
  URL and `GLOBAL_API_KEY`, and complete the registration flow. Status persists
  in the `runtime_configs` table.
- **`LICENSE_GATE_ENABLED=false`** — the API is served without the activation
  gate and without the remote heartbeat, for fully independent operation.

---

## API Documentation

Swagger UI available at:

```
http://localhost:8080/swagger/index.html
```

### Key Endpoints

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/instance/create` | Create WhatsApp instance |
| `GET` | `/instance/{name}/qrcode` | Get QR code for pairing |
| `POST` | `/message/sendText` | Send text message |
| `POST` | `/message/sendMedia` | Send media message |
| `GET` | `/instance/{name}/status` | Get instance status |
| `DELETE` | `/instance/{name}` | Delete instance |

---

## Project Structure

```
omniwa-go/
├── cmd/evolution-go/     # Application entry point (dir kept to ease upstream sync)
├── pkg/
│   ├── core/            # License management & middleware
│   ├── instance/        # Instance management
│   ├── message/         # Message handling
│   ├── sendMessage/     # Message sending
│   ├── routes/          # HTTP routes
│   ├── middleware/      # Auth & validation middleware
│   ├── config/          # Configuration
│   ├── events/          # Event producers (AMQP, NATS, Webhook, WS)
│   └── storage/         # Media storage (MinIO)
├── docs/                # Swagger documentation + SYNC.md
├── Dockerfile
├── Makefile
└── VERSION
```

> The Go module path (`github.com/evolution-foundation/evolution-go`) and the
> `cmd/evolution-go/` directory are intentionally kept unchanged to keep
> upstream syncs low-conflict. See [docs/SYNC.md](./docs/SYNC.md).

---

## Tech Stack

| Component | Technology |
|---|---|
| Language | Go 1.25+ |
| HTTP framework | Gin |
| WhatsApp | [whatsmeow](https://github.com/tulir/whatsmeow) |
| Database | PostgreSQL |
| ORM | GORM |
| Message queue | RabbitMQ, NATS |
| Object storage | MinIO/S3 |
| Documentation | Swagger/OpenAPI |
| Container | Docker |

---

## Syncing with upstream

OmniWA GO tracks [Evolution Go](https://github.com/evolution-foundation/evolution-go)
and can merge upstream releases. The workflow is documented in
[docs/SYNC.md](./docs/SYNC.md).

---

## Attribution

OmniWA GO is a derivative work of
[Evolution Go](https://github.com/evolution-foundation/evolution-go) by Evolution
Foundation, licensed under the Apache License 2.0.

- Original copyright and attribution are preserved in [NOTICE](./NOTICE).
- "Evolution Foundation", "Evolution", and "Evolution Go" are trademarks of
  Evolution Foundation. OmniWA GO is an independent fork and does not use those
  marks for its own branding, nor is it affiliated with or endorsed by Evolution
  Foundation.

### Acknowledgments

- [whatsmeow](https://github.com/tulir/whatsmeow) by [Tulir Asokan](https://github.com/tulir) — WhatsApp protocol library
- [Evolution Go](https://github.com/evolution-foundation/evolution-go) — the upstream project this fork is based on

---

## License

Licensed under the Apache License 2.0. See [LICENSE](./LICENSE) for full details
and [NOTICE](./NOTICE) for attributions.
