# multichain-wallet-data-service

HTTP API over Zerion wallet data for EVM and Solana addresses. It returns a simple portfolio summary and one page of recent activity.

This project is not affiliated with Zerion.
Licensed under the Apache License 2.0.

## What it does

The service sits in front of the [Zerion API](https://developers.zerion.io/). Callers send an EVM or Solana wallet address. The service authenticates to Zerion, maps the JSON:API responses into a smaller local JSON contract, and handles retries, short-lived caching, and pagination cursors.

## Features

- EVM and Solana wallet summaries
- Paged wallet activity
- Zerion API authentication
- Bounded retries
- Short summary cache
- Prometheus metrics
- Graceful shutdown

## Requirements

- Go 1.22+
- A Zerion API key

## Configuration

Environment variables only.

| Variable | Required | Default |
| --- | --- | --- |
| `ZERION_API_KEY` | yes | — |
| `HTTP_ADDR` | no | `:8080` |
| `ZERION_BASE_URL` | no | `https://api.zerion.io` |

The process exits at startup if `ZERION_API_KEY` is missing or blank.

Do not put a real API key in source files or commit it.

## Run

```bash
export ZERION_API_KEY="your_api_key"
go run ./cmd/server
```

## API

### `GET /healthz`

Process liveness. Does not call Zerion.

```bash
curl -s http://127.0.0.1:8080/healthz
```

```json
{"status":"ok"}
```

### `GET /metrics`

Prometheus text metrics.

```bash
curl -s http://127.0.0.1:8080/metrics
```

### `GET /v1/wallets/{address}/summary`

Simple-asset portfolio totals. Optional `currency` (default `usd`).

```bash
curl -s "http://127.0.0.1:8080/v1/wallets/0x1111111111111111111111111111111111111111/summary?currency=usd"
```

Example response:

```json
{
  "address": "0x1111111111111111111111111111111111111111",
  "address_type": "evm",
  "currency": "usd",
  "total": 2017.48,
  "change_1d": {"absolute": 102.02, "percent": 5.33},
  "by_type": {
    "wallet": 1864.77,
    "deposited": 0,
    "borrowed": 0,
    "locked": 0,
    "staked": 0
  },
  "by_chain": {
    "ethereum": 1214.01,
    "solana": 50.0
  }
}
```

A valid empty/zero portfolio is `200`, not `404`.

### `GET /v1/wallets/{address}/activity`

One page of recent non-trash transactions.

Query parameters on the first page:

- `currency` (default `usd`)
- `page_size` (default `20`, max `100`)
- `chain_ids` (comma-separated)
- `operation_types` (comma-separated)

```bash
curl -s "http://127.0.0.1:8080/v1/wallets/11111111111111111111111111111111/activity?page_size=20"
```

A valid empty page is `200` with `"items": []`.

## Pagination

`next_cursor` is an opaque token. Pass it back unchanged as `cursor`.

Do not combine `cursor` with `currency`, `page_size`, `chain_ids`, or `operation_types`. Those filters are already stored in the continuation.

## Tests

```bash
go test ./...
go test -race ./...
go vet ./...
```

Tests use a local fake Zerion server. They do not need a live API key.

## Design notes

- One activity request fetches exactly one Zerion page.
- Successful summaries are cached in memory for 15 seconds (about 1024 entries).
- The service does not walk full transaction history.
- There is no database.
- The Zerion key stays on the server. It is not accepted from callers and is not logged.
- Summary always requests `filter[positions]=only_simple`, so EVM and Solana totals mean wallet-held assets, not protocol positions.

## Zerion

Documentation: [https://developers.zerion.io/](https://developers.zerion.io/)

This project is not affiliated with Zerion.
