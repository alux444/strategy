# Insider purchase shortlist

Run `go run .` to collect significant Form 4 transactions across all issuers and generate a ranked research shortlist. The default scan starts with yesterday and looks back seven New York calendar days. It uses SEC directory listings to download only published daily indexes. Today and other unpublished dates are excluded. Daily indexes become available overnight, so the report is not a live filing feed.

## What gets saved

Only officers and directors qualify as reporting owners. The script reads non-derivative transactions with valid, positive shares and prices:

| Transaction | Collection floor | Purpose |
| --- | --- | --- |
| Purchase, code P | $25,000 per row | Evidence for individual and clustered buying |
| Sale, code S | $200,000 per row | Context for companies with buying signals |

Grants, gifts, exercises, and other transaction codes are excluded. The purchase floor retains smaller buys that can become meaningful together. Saving a purchase does not automatically create a lead.

Rows retain their existing deduplication key: ticker, owner name, transaction date, code, shares, price, and amount. A separate `transaction_evidence` table stores source URLs, owner CIKs, plan flags, joint-reporting and amendment flags, and supported holding comparisons. Existing transaction rows are preserved; rescanning can fill missing evidence.

## What becomes a lead

The report uses transaction dates in the latest 14 New York calendar days. A company qualifies through at least one rule:

| Signal | Rule | Rank points |
| --- | --- | --- |
| Cluster | Two or more distinct insiders buy at least $100,000 combined | 40 |
| Commitment | An individual purchase of at least $100,000 establishes a reported direct holding or increases it by at least 10% | 30 |
| Large purchase | An individual purchase of at least $250,000 | 25 |

Each qualifying purchase must meet the purchase collection floor. Planned 10b5-1 trades, amendments, joint reporting, and historical purchases without source evidence cannot create a lead. Owner CIKs distinguish insiders; normalized names are the fallback when a CIK is unavailable.

After qualification, CEO/CFO participation adds 15 points, director participation adds 5, and an insider buying on multiple distinct dates adds 10. Multiple rows on one date do not earn the repeat-buying bonus. Each category contributes at most once. Ties sort by purchase dollars, then ticker.

These are initial research heuristics, not backtested predictions. The report includes every qualifying reason, purchase evidence, source URLs, and saved sales in the same window. Sales do not add rank points.

A holding increase is `purchased shares / (reported shares afterward - purchased shares)`. It is calculated only for direct ownership with consistent reported numbers. Missing or indirect holdings stay unknown. This comparison concerns the reported holding, not the insider's total wealth or all accounts.

Code P includes private purchases as well as open-market purchases. Review filing footnotes, related sales, and the business before acting. Amendments are retained for review but do not automatically replace or delete previously collected originals.

## Run and output

Configure a real SEC identity before a live scan. Environment variables override the settings file:

```sh
export SEC_USER_AGENT='Your organization your-real-email@your-domain.com'
export SEC_CONTACT_EMAIL='your-real-email@your-domain.com'
go run .
```

With the checked-in settings, transactions go to `db/insider_transactions.db`, successful collection summaries to `db/ingest_runs.txt`, and the JSON shortlist to `history/leads.json`. A text shortlist is also printed. Notifications remain disabled; enabling `notify` sends the shortlist through the existing notification path.

Each report also gets a timestamped JSON snapshot in `history/leads-history/`. The first report after updating archives any existing `leads.json` before replacing it. The filename timestamp is UTC. Each snapshot includes the New York scoring window, the flagged tickers, their scores and reasons, and the transactions and filing links. The `collectionStatus` field identifies normal, partial, and report-only runs. The first archived report from an older binary may lack that field. Older overwritten reports are available as summaries in any retained run logs.

List saved reports with `ls -1 history/leads-history/`. To find when a ticker appeared, use `jq` with the ticker you want:

```sh
jq -r --arg ticker STWD '
  . as $report | .leads[] | select(.ticker == $ticker) |
  [$report.windowEnd, $report.generatedAt, .ticker, .score, .purchaseUsd] | @tsv
' history/leads-history/*.json
```

`--leads-file` places the archive beside the chosen output file. For example, `--leads-file history/shortlist.json` uses `history/shortlist-history/`.

To generate a report from the existing database without downloads or database writes:

```sh
go run . --report-only
```

Older purchases without source evidence are counted separately and excluded until rescanned. For initial coverage of the full lead window, or recovery after a longer outage:

```sh
go run . --lookback 21
```

A broad initial scan may take a long time because every Form 4 filing must be downloaded before its transactions can be filtered. Later scans reuse cached filing bodies. The report always evaluates the latest 14 transaction dates, even when the collection lookback is longer.

To limit collection to specified tickers:

```sh
go run . --tickers AAPL,MSFT
```

Alternatively set `allIssuers` to false to use `holdingsFile`. Restricted scans require `tickerMapFile` or the SEC ticker mapping download. Reports still evaluate all qualifying transactions stored in the database.

`minimumBuyUsd` / `--min-buy-usd` controls the purchase collection floor and must be at least $25,000. `thresholdUsd` / `--minusd` controls the sale context floor. `--transactions-file`, `--ingest-log-file`, and `--leads-file` override output paths. Signal rules and rank weights are named constants in `leads_types.go`.

## Collection reliability

For a small live access check before a broad scan:

```sh
go run . --check-sec --verbose=true
```

This reads the published directory listing, fetches the latest available index in the previous eight calendar days, and checks one Form 4 filing. It validates the real SEC header and ownership XML, bypasses the cache, and writes no database records. If an index contains no Form 4 filings, it reports that filing access was not checked.

Requests are sequential with a one-second delay by default and up to four attempts for transient failures. HTTP 403/429 and recognized SEC block pages stop the current request and further discovery or filing downloads immediately. A 403 alone does not establish whether the URL is unavailable or access from the host is restricted. Daily indexes and browse feeds bypass the permanent cache; filing bodies remain cached. New York dates use calendar midnight, including daylight saving changes.

Dates absent from the published directory listing, including holidays, are skipped without requesting nonexistent index URLs. An advertised index that fails to download is reported as a collection failure. Failed requests and invalid filing/index payloads remain visible. Partial collection writes its available evidence and a report containing `collectionWarnings`, exits unsuccessfully, and suppresses notifications and the successful-run log. A failure to retrieve any daily index exits before recording a successful run.

Overlaps cover ordinary missed runs, not unlimited downtime. Increase `--lookback` after a longer outage. The SEC documents its [daily index publication schedule and fair-access rules](https://www.sec.gov/search-filings/edgar-search-assistance/accessing-edgar-data).

## Verification

```sh
go test -race ./...
go vet ./...
```

Tests cover real cached SEC filings, qualification boundaries, owner identity, plans and joint filings, holdings, persistence, legacy evidence enrichment, deduplication, index discovery, rate-limit stops, partial failures, and report-only operation.
