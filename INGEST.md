# Daily Insider Ingest

The default `go run .` path is now the broad daily scraper. Its job is to collect significant insider transactions into an append-only local JSONL dataset, not decide whether a strategy is attractive.

## Run

```sh
go run .
```

By default it reads `settings.json`, scans the configured universe, and writes new transactions to:

```txt
history/insider_transactions.jsonl
```

## Universe

Use `holdingsFile` in `settings.json` for the broad mid-market company list. The file can be a simple CSV or one ticker per line. Example:

```txt
FG
ODP
SBCF
CTKB
```

`tickerMapFile` should point at your pasted SEC ticker-to-CIK file. This avoids the SEC ticker-map HTTP request.

## Threshold

`thresholdUsd` controls what gets recorded. The pivot default is:

```json
"thresholdUsd": 100000
```

The scraper records both buys and sells above this amount when they appear in Form 4 non-derivative transactions.

## Output

Each JSONL row contains:

- ticker
- owner name
- position
- transaction type and Form 4 code
- transaction date
- shares, price, dollar amount
- 10b5-1 flag
- shares owned after

Rows are de-duped by ticker, owner, date, transaction code, shares, price, and amount, so overlapping lookback windows should not duplicate old records.

## Notifications

Set `"notify": true` if you still want the old notification behavior. For dataset collection, keep it false.
