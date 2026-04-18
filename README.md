# telegram-connector

Telegram connector service for Agyn. It enrolls via the gateway, binds/dials over OpenZiti, and syncs Telegram updates with Agyn threads and notifications.

## Configuration

Required environment variables:

- `DATABASE_URL`
- `ZITI_IDENTITY_FILE`
- `SERVICE_TOKEN`
- `GATEWAY_URL`
- `APP_ID`

Optional environment variables:

- `HTTP_ADDRESS` (default `:8080`)
- `ZITI_SERVICE_NAME` (default `app-telegram`)
- `GATEWAY_SERVICE_NAME` (default `gateway`)
- `TELEGRAM_BASE_URL` (default `https://api.telegram.org`)
- `TELEGRAM_POLL_TIMEOUT` (default `30s`)

## Development

Generate protobuf bindings (same as CI):

```bash
buf generate buf.build/agynio/api \
  --path agynio/api/apps/v1/apps.proto \
  --path agynio/api/files/v1/files.proto \
  --path agynio/api/threads/v1/threads.proto \
  --path agynio/api/notifications/v1/notifications.proto \
  --path agynio/api/gateway/v1/apps.proto \
  --path agynio/api/gateway/v1/files.proto \
  --path agynio/api/gateway/v1/threads.proto \
  --path agynio/api/gateway/v1/notifications.proto
```

Run locally:

```bash
go run ./cmd/telegram-connector
```

Run tests:

```bash
go vet ./...
go test ./...
go test -tags e2e ./test/e2e
```
