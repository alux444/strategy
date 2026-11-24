package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

func runTracker(opts map[string]string, cfg settings, downloader filingDownloader, now time.Time, output io.Writer) error {
	if cfg.Debug != nil {
		setDebug(*cfg.Debug)
	}
	verbose := cfg.Verbose != nil && *cfg.Verbose
	setVerbose(parseBool(opts["verbose"], verbose))
	configureSECFromSettings(opts, cfg)
	if parseBool(opts["check-sec"], false) {
		return checkSECAccess(downloader, now, output)
	}
	databasePath := firstNonBlank(opts["transactions-file"], opts["db"], cfg.TransactionsFile, defaultTransactionsFile)
	reportOnly := parseBool(opts["report-only"], false)
	failures := []string{}
	collectionStatus := collectionReportOnly
	if !reportOnly {
		collectedFailures, err := ingestTransactions(opts, cfg, downloader, now, output, databasePath)
		if err != nil {
			return err
		}
		failures = collectedFailures
		collectionStatus = collectionComplete
		if len(failures) > 0 {
			collectionStatus = collectionPartial
		}
	}

	report, err := buildLeadReport(sqliteTransactionReader{Path: databasePath}, now)
	if err != nil {
		return fmt.Errorf("build lead report: %w", err)
	}
	report.CollectionWarnings = failures
	report.CollectionStatus = collectionStatus
	reportPath := firstNonBlank(opts["leads-file"], cfg.LeadsFile, defaultLeadsFile)
	archivePath, err := saveLeadReport(reportPath, report)
	if err != nil {
		return fmt.Errorf("save lead report: %w", err)
	}
	summary := renderLeadSummary(report)
	if _, err := fmt.Fprintf(output, "%s\nFull report: %s\nArchived report: %s\n", summary, reportPath, archivePath); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("collection incomplete: %d failures; see collectionWarnings in %s", len(failures), reportPath)
	}
	notify := cfg.Notify != nil && *cfg.Notify
	if parseBool(opts["notify"], notify) && !reportOnly && len(report.Leads) > 0 {
		sendNotification(summary)
	}
	return nil
}

func checkSECAccess(downloader filingDownloader, now time.Time, output io.Writer) error {
	date := newYorkDay(now).AddDate(0, 0, -1)
	listing := discoverPublishedIndexes(downloader, date.AddDate(0, 0, -defaultMaxLookbackDays), date)
	if listing.Blocked {
		return fmt.Errorf("SEC access check failed: %w: %s", errSECBlocked, strings.Join(listing.Failures, "; "))
	}
	if len(listing.Failures) > 0 || len(listing.URLs) == 0 {
		return fmt.Errorf("SEC access check: no usable published index: %s", strings.Join(listing.Failures, "; "))
	}
	indexURL := listing.URLs[0]
	fmt.Fprintf(output, "Checking latest published SEC index: %s\n", indexURL)
	content, err := downloader.DownloadText(indexURL)
	if err != nil {
		return fmt.Errorf("SEC access check failed: %w", err)
	}
	if !isMasterIndex(content) {
		return fmt.Errorf("SEC access check: response is not a master index: %s", indexURL)
	}
	filingURLs := parseMasterIdx(content, nil)
	fmt.Fprintf(output, "Index OK: %d distinct Form 4 filings.\n", len(filingURLs))
	if len(filingURLs) == 0 {
		fmt.Fprintln(output, "No Form 4 filings in this index; filing download access was not checked. No database changes.")
		return nil
	}
	payload, err := downloader.DownloadText(filingURLs[0])
	if err != nil {
		return fmt.Errorf("SEC filing access check failed: %w", err)
	}
	if _, err := parseForm4WithThresholds(payload, defaultMinimumBuyUSD, minimumLargeBuyUSD, nil); err != nil {
		return fmt.Errorf("SEC filing parse check failed: %w", err)
	}
	fmt.Fprintln(output, "Filing download and ownership XML parsing OK. No database changes.")
	return nil
}

func ingestTransactions(opts map[string]string, cfg settings, downloader filingDownloader, now time.Time, output io.Writer, databasePath string) ([]string, error) {
	universe, err := resolveUniverse(opts, cfg)
	if err != nil {
		return nil, err
	}
	minimumBuyUSD := defaultMinimumBuyUSD
	if cfg.MinimumBuyUSD != nil {
		minimumBuyUSD = *cfg.MinimumBuyUSD
	}
	minimumBuyUSD = parseInt64(opts["min-buy-usd"], minimumBuyUSD)
	minimumSellUSD := int64(200_000)
	if cfg.ThresholdUSD != nil {
		minimumSellUSD = *cfg.ThresholdUSD
	}
	minimumSellUSD = parseInt64(opts["minusd"], minimumSellUSD)
	lookback := defaultMaxLookbackDays
	if cfg.LookbackDays != nil {
		lookback = *cfg.LookbackDays
	}
	lookback = parseInt(opts["lookback"], lookback)
	if minimumBuyUSD < defaultMinimumBuyUSD || minimumSellUSD <= 0 || lookback < 0 {
		return nil, fmt.Errorf("require min-buy-usd >= %d, minusd > 0, and lookback >= 0", defaultMinimumBuyUSD)
	}
	fmt.Fprintf(output, "Scanning Form 4 filings: allIssuers=%t, buyFloor=$%d, saleContextFloor=$%d, lookback=%d days\n", universe.AllIssuers, minimumBuyUSD, minimumSellUSD, lookback)
	scan := scanFilingIndexes(downloader, universe, now, lookback)
	if scan.IndexCount == 0 {
		return nil, fmt.Errorf("no daily indexes available; collection did not run: %s", strings.Join(scan.Failures, "; "))
	}
	transactions, failures := collectFilingTransactions(downloader, scan, universe, minimumBuyUSD, minimumSellUSD)
	written, err := appendTransactionRecords(databasePath, transactions, now.In(newYorkLocation()).Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("save transactions: %w", err)
	}
	fmt.Fprintf(output, "Read %d daily indexes; discovered %d filings; saved %d new transactions; failures=%d\n", scan.IndexCount, len(scan.URLs), written, len(failures))
	if len(failures) == 0 {
		logPath := firstNonBlank(opts["ingest-log-file"], cfg.IngestLogFile, defaultIngestLogFile)
		if err := appendIngestRunLog(logPath, written, databasePath); err != nil {
			return failures, err
		}
	}
	return failures, nil
}

func renderLeadSummary(report leadReport) string {
	var summary strings.Builder
	fmt.Fprintf(&summary, "Insider purchase shortlist: %s through %s\n", report.WindowStart, report.WindowEnd)
	if len(report.CollectionWarnings) > 0 {
		fmt.Fprintf(&summary, "Collection incomplete: %d failures.\n", len(report.CollectionWarnings))
	}
	if report.UnverifiedPurchases > 0 {
		fmt.Fprintf(&summary, "%d historical purchase rows lack source evidence and need rescanning before they can qualify.\n", report.UnverifiedPurchases)
	}
	if len(report.Leads) == 0 {
		summary.WriteString("No companies met the purchase signal rules in the collected data.\n")
	}
	for _, lead := range report.Leads {
		fmt.Fprintf(&summary, "\n%s | rank points %d | %s bought | %d buyers\n", lead.Ticker, lead.Score, formatAmount(lead.PurchaseUSD), lead.BuyerCount)
		for _, reason := range lead.Reasons {
			fmt.Fprintf(&summary, "  - %s\n", reason)
		}
		if len(lead.ContextSales) > 0 {
			fmt.Fprintf(&summary, "  Context: %d sale transactions in the same window; see report.\n", len(lead.ContextSales))
		}
	}
	summary.WriteString("\nRank points measure buying signals, not expected returns. Review the source filings, valuation, and business.")
	return summary.String()
}
