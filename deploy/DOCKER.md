# Sub2API Docker Image

Sub2API is an AI API Gateway Platform for distributing and managing AI product subscription API quotas.

## Quick Start

```bash
docker run -d \
  --name sub2api \
  -p 8080:8080 \
  -e DATA_DIR="/app/data" \
  -e DATABASE_URL="postgres://user:pass@host:5432/sub2api" \
  -e REDIS_URL="redis://host:6379" \
  -v sub2api_data:/app/data \
  weishaw/sub2api:latest
```

## Docker Compose

```yaml
version: '3.8'

services:
  sub2api:
    image: weishaw/sub2api:latest
    ports:
      - "8080:8080"
    environment:
      - DATA_DIR=/app/data
      - DATABASE_URL=postgres://postgres:postgres@db:5432/sub2api?sslmode=disable
      - REDIS_URL=redis://redis:6379
    volumes:
      - sub2api_data:/app/data
    depends_on:
      - db
      - redis

  db:
    image: postgres:15-alpine
    environment:
      - POSTGRES_USER=postgres
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=sub2api
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data

volumes:
  sub2api_data:
  postgres_data:
  redis_data:
```

## Session Archive Persistence

Session Archive is disabled by default. When using
`session_archive.storage_backend: filesystem` with an empty
`session_archive.filesystem.root`, objects are stored at
`/app/data/session-archive`; the `sub2api_data` volume above is therefore
required across container recreation. If a custom root is configured, mount a
persistent volume at that exact path. Multiple application replicas must share
the same compatible filesystem instead of using one local volume per replica.

The encryption key ring is normally part of the persisted configuration under
`DATA_DIR`. Back up that configuration together with `/app/data` and the
PostgreSQL database. PostgreSQL-backed archive content is included in the
database backup and can materially increase database, WAL, and restore sizes;
S3-backed content needs a separate private bucket backup or versioning policy.
For external S3/filesystem storage, pause archive writers or use coordinated
snapshots so database metadata, objects, and keys form one recovery point.

Migration 237 is intentionally absent from ordinary binary execution until a
source build with the `session_archive_storage_finalize` Go build tag is started.
Complete the documented two-stage rollout in the repository README before
selecting `filesystem` or `postgresql`; database migrations are forward-only.

## Startup and Database Recovery

Sub2API runs database migrations while starting. PostgreSQL may still be
recovering briefly after a host or Docker daemon restart. The application
retries transient PostgreSQL startup and connection errors with bounded
exponential backoff, then continues startup when the database is ready.
Permanent errors such as invalid credentials, migration checksum mismatches,
SQL errors, and incompatible data fail immediately.

The Compose deployment also checks PostgreSQL readiness with both `pg_isready`
and a simple SQL query. `depends_on: condition: service_healthy` helps order a
fresh Compose start, but application-level retries are still required when
Docker restores existing containers after a host restart.

## Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string | Yes | - |
| `REDIS_URL` | Redis connection string | Yes | - |
| `PORT` | Server port | No | `8080` |
| `GIN_MODE` | Gin framework mode (`debug`/`release`) | No | `release` |
| `DATA_DIR` | Persistent application data and default filesystem archive root | No | `/app/data` in the image |

## Supported Architectures

- `linux/amd64`
- `linux/arm64`

## Tags

- `latest` - Latest stable release
- `x.y.z` - Specific version
- `x.y` - Latest patch of minor version
- `x` - Latest minor of major version

## Links

- [GitHub Repository](https://github.com/weishaw/sub2api)
- [Documentation](https://github.com/weishaw/sub2api#readme)
