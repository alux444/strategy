package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testMasterIndex = "CIK|Company Name|Form Type|Date Filed|Filename\n" +
	"1|Test Company|4|2026-10-20|edgar/data/1/first.txt\n" +
	"2|Test Owner|4|2026-10-20|edgar/data/1/first.txt\n" +
	"1|Test Company|4|2026-10-20|edgar/data/1/second.txt\n" +
	"1|Test Company|10-K|2026-10-20|edgar/data/1/annual.txt\n"

type memoryDownloader struct {
	responses map[string]string
	failures  map[string]error
	requests  []string
}

func (downloader *memoryDownloader) DownloadText(rawURL string) (string, error) {
	downloader.requests = append(downloader.requests, rawURL)
	if err, found := downloader.failures[rawURL]; found {
		return "", err
	}
	if response, found := downloader.responses[rawURL]; found {
		return response, nil
	}
	return "", errHTTPNotFound
}

func TestAllIssuerDiscoveryAndCorrectNewYorkDate(t *testing.T) {
	indexURL := secBase + "edgar/daily-index/2026/QTR4/master.20261020.idx"
	downloader := &memoryDownloader{responses: map[string]string{indexURL: testMasterIndex}}
	now := time.Date(2026, time.October, 21, 1, 0, 0, 0, time.UTC)
	scan := scanFilingIndexes(downloader, tickerUniverse{AllIssuers: true}, now, 0)
	if scan.IndexCount != 1 || len(scan.URLs) != 2 || len(scan.Failures) != 0 || downloader.requests[0] != indexURL {
		t.Fatalf("bad scan: %+v, requests=%v", scan, downloader.requests)
	}
	if len(parseMasterIdx(testMasterIndex, map[string]struct{}{"999": {}})) != 0 {
		t.Fatal("restricted ticker universe leaked unrelated filings")
	}
}

func TestRateLimitStopsRequestsAndFailuresRemainVisible(t *testing.T) {
	indexURL := secBase + "edgar/daily-index/2026/QTR4/master.20261020.idx"
	downloader := &memoryDownloader{failures: map[string]error{indexURL: errSECBlocked}}
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, newYorkLocation())
	scan := scanFilingIndexes(downloader, tickerUniverse{AllIssuers: true}, now, 7)
	if !scan.Blocked || len(downloader.requests) != 1 || len(scan.Failures) != 1 {
		t.Fatalf("scan continued after block: %+v, %v", scan, downloader.requests)
	}
	downloader = &memoryDownloader{failures: map[string]error{"first": errSECBlocked}}
	_, failures := collectFilingTransactions(downloader, filingScan{URLs: []string{"first", "second"}}, tickerUniverse{}, 25_000, 200_000)
	if len(failures) != 1 || len(downloader.requests) != 1 {
		t.Fatalf("filing requests continued after block: %v, %v", failures, downloader.requests)
	}
}

func TestTrackerPersistsEvidenceDeduplicatesAndWritesShortlist(t *testing.T) {
	directory := t.TempDir()
	falseValue := false
	lookback := 0
	cfg := settings{
		TransactionsFile: filepath.Join(directory, "transactions.db"),
		IngestLogFile:    filepath.Join(directory, "ingest.txt"),
		LeadsFile:        filepath.Join(directory, "leads.json"),
		Debug:            &falseValue, LookbackDays: &lookback,
	}
	indexURL := secBase + "edgar/daily-index/2026/QTR4/master.20261020.idx"
	downloader := &memoryDownloader{responses: map[string]string{
		indexURL:                            testMasterIndex,
		secBase + "edgar/data/1/first.txt":  ownershipFixture("Buyer One", "2", "P", "6000", "10", "66000", "D", "0", "4"),
		secBase + "edgar/data/1/second.txt": ownershipFixture("Buyer Two", "3", "P", "5000", "10", "55000", "D", "0", "4"),
	}}
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, newYorkLocation())
	var output bytes.Buffer
	for run := 0; run < 2; run++ {
		if err := runTracker(map[string]string{}, cfg, downloader, now, &output); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(cfg.LeadsFile)
	if err != nil {
		t.Fatal(err)
	}
	var report leadReport
	if err := json.Unmarshal(content, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Leads) != 1 || report.Leads[0].BuyerCount != 2 || report.Leads[0].PurchaseUSD != 110_000 {
		t.Fatalf("wrong shortlist: %s", content)
	}
	if len(report.Leads[0].Purchases) != 2 || report.Leads[0].Purchases[0].FilingURL == "" || report.Leads[0].Purchases[0].HoldingIncrease == nil {
		t.Fatalf("evidence did not round trip: %s", content)
	}
	if !strings.Contains(output.String(), "saved 0 new transactions") {
		t.Fatalf("overlapping scan duplicated records: %s", output.String())
	}
	downloader.failures = map[string]error{secBase + "edgar/data/1/second.txt": errors.New("network unavailable")}
	if err := runTracker(map[string]string{}, cfg, downloader, now, &output); err == nil {
		t.Fatal("partial collection exited successfully")
	}
	content, _ = os.ReadFile(cfg.LeadsFile)
	if err := json.Unmarshal(content, &report); err != nil || len(report.CollectionWarnings) != 1 {
		t.Fatalf("report lost collection failure: %s, %v", content, err)
	}
	beforeRequests := len(downloader.requests)
	if err := runTracker(map[string]string{"report-only": "true"}, cfg, downloader, now, &output); err != nil {
		t.Fatal(err)
	}
	if len(downloader.requests) != beforeRequests {
		t.Fatal("report-only made HTTP requests")
	}
}

func TestLegacyDatabaseReadIsReadOnlyAndDoesNotInventEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`CREATE TABLE transactions (
		id TEXT, scan_date TEXT, ticker TEXT, owner_name TEXT, position TEXT, type TEXT,
		transaction_code TEXT, transaction_date TEXT, shares INTEGER, price REAL,
		amount REAL, is10b51 INTEGER, shares_owned_after INTEGER);
		INSERT INTO transactions VALUES ('one','2026-10-20','TEST','Buyer','CEO','BUY','P','2026-10-20',10000,100,1000000,0,10000);`)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	reader := sqliteTransactionReader{Path: path}
	records, err := reader.ReadTransactions("2026-10-07", "2026-10-20")
	if err != nil || len(records) != 1 || records[0].HoldingIncrease != nil || records[0].NewPosition {
		t.Fatalf("legacy evidence was invented: %+v, %v", records, err)
	}
	_, qualified := evaluateLead("TEST", records)
	if qualified {
		t.Fatal("legacy row with unverified source qualified")
	}
	_, err = reader.ReadTransactions("2026-10-21", "2026-10-22")
	if err != nil {
		t.Fatal(err)
	}
	missing := sqliteTransactionReader{Path: filepath.Join(t.TempDir(), "missing.db")}
	if _, err := missing.ReadTransactions("2026-10-07", "2026-10-20"); err == nil {
		t.Fatal("missing database silently created")
	}
}

func TestMutableIndexesDoNotUsePermanentCache(t *testing.T) {
	previousCache := downloadCacheDir
	setDownloadCacheDir(t.TempDir())
	t.Cleanup(func() { setDownloadCacheDir(previousCache) })
	indexURL := secBase + "edgar/daily-index/2026/QTR4/master.20261020.idx"
	writeDownloadCache(indexURL, testMasterIndex)
	if _, found := readDownloadCache(indexURL); found {
		t.Fatal("daily index remained permanently cached")
	}
	filingURL := secBase + "edgar/data/1/first.txt"
	writeDownloadCache(filingURL, "immutable filing")
	if cached, found := readDownloadCache(filingURL); !found || cached != "immutable filing" {
		t.Fatal("immutable filing cache was discarded")
	}
}

func TestAllIssuerUniverseNeedsNoTickerMap(t *testing.T) {
	universe, err := resolveUniverse(map[string]string{}, settings{TickerMapFile: "/does/not/exist"})
	if err != nil || !universe.AllIssuers {
		t.Fatalf("all-issuer mode loaded ticker map: %+v, %v", universe, err)
	}
	_, err = resolveUniverse(map[string]string{"tickers": "TEST"}, settings{TickerMapFile: "/does/not/exist"})
	if err == nil || !strings.Contains(fmt.Sprint(err), "ticker map") {
		t.Fatalf("explicit ticker filter was ignored: %v", err)
	}
}

func TestRescanningLegacyRowsUsesVerifiedPlanFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transactions.db")
	entry := AlertEntry{
		ownerName: "Buyer", position: "CEO", typeLabel: "BUY", transactionCode: "P",
		transactionDate: "2026-10-20", shares: 10_000, price: 100, amount: 1_000_000,
	}
	transactions := map[string][]AlertEntry{"TEST": {entry}}
	if _, err := appendTransactionRecords(path, transactions, "2026-10-20"); err != nil {
		t.Fatal(err)
	}
	entry.is10b51 = true
	entry.filingURL = "https://www.sec.gov/Archives/edgar/data/1/planned.txt"
	transactions["TEST"] = []AlertEntry{entry}
	written, err := appendTransactionRecords(path, transactions, "2026-10-21")
	if err != nil || written != 0 {
		t.Fatalf("rescan duplicated legacy transaction: %d, %v", written, err)
	}
	records, err := (sqliteTransactionReader{Path: path}).ReadTransactions("2026-10-07", "2026-10-21")
	if err != nil || len(records) != 1 || !records[0].Is10b51 || records[0].FilingURL == "" {
		t.Fatalf("verified evidence did not replace missing legacy evidence: %+v, %v", records, err)
	}
	if _, qualified := evaluateLead("TEST", records); qualified {
		t.Fatal("reprocessed planned purchase qualified")
	}
}
