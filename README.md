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
  --path agynio/api/organizations/v1/organizations.proto \
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

## E2E (local cluster)

E2E runs against a local Agyn cluster with real Apps/Threads/Files/Notifications/Gateway services plus a deployed connector and a Telegram mock. The pipeline bootstraps a test org/app via `x-identity-id`, so no pre-provisioned IDs are required.

```bash
devspace run test:e2e
```

Optional: set `E2E_CLEANUP=true` to delete the created org/app/installation after the run.
