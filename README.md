# EPay Bridge

Single-merchant EPay-compatible gateway for New API. It translates New API's
legacy EPay form protocol into official Alipay website payments and WeChat Pay
Native payments.

The project is intentionally single merchant: one fixed EPay PID/key pair and
one New API instance.

## Version-one scope

- EPay-compatible `GET/POST /submit.php`
- Alipay computer website payment
- WeChat Pay API v3 Native payment
- New API balance top-ups and subscription purchases
- Durable, idempotent provider callbacks
- Durable New API callback delivery with retries
- PostgreSQL persistence
- PC checkout page and WeChat QR code

Mobile H5, JSAPI, mini-program payments, refunds, and multiple merchants are
out of scope for version one.

See [docs/configuration.md](docs/configuration.md) for configuration and
[docs/protocol.md](docs/protocol.md) for the EPay compatibility contract.

## Tests

Unit tests do not require external services:

```sh
go test ./...
```

The PostgreSQL integration test uses a disposable database and fake payment
providers. It never calls Alipay or WeChat Pay:

```sh
docker compose -f compose.integration.yaml up -d --wait
EPAY_INTEGRATION_DATABASE_URL='postgres://epay:epay-integration-test-password@127.0.0.1:55432/epay_integration?sslmode=disable' \
  go test -tags=integration ./internal/service ./internal/web
docker compose -f compose.integration.yaml down
```

PowerShell uses this equivalent test command after starting the database:

```powershell
$env:EPAY_INTEGRATION_DATABASE_URL = 'postgres://epay:epay-integration-test-password@127.0.0.1:55432/epay_integration?sslmode=disable'
go test -tags=integration ./internal/service ./internal/web
docker compose -f compose.integration.yaml down
Remove-Item Env:EPAY_INTEGRATION_DATABASE_URL
```

