package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func resolveUniverse(opts map[string]string, cfg settings) (tickerUniverse, error) {
	universe := tickerUniverse{CIKs: make(map[string]struct{}), TickerByCIK: make(map[string]string)}
	tickersArg := firstNonBlank(opts["tickers"], joinTickers(cfg.Tickers))
	allIssuers := true
	if cfg.AllIssuers != nil {
		allIssuers = *cfg.AllIssuers
	}
	universe.AllIssuers = parseBool(opts["all-issuers"], allIssuers)
	if tickersArg != "" || opts["holdings-file"] != "" {
		universe.AllIssuers = false
	}
	if universe.AllIssuers {
		return universe, nil
	}
	universe.Tickers = parseTickers(tickersArg)
	if len(universe.Tickers) == 0 {
		loaded, err := readTickersFromCSV(firstNonBlank(opts["holdings-file"], cfg.HoldingsFile))
		if err != nil {
			return universe, err
		}
		universe.Tickers = loaded
	}
	mapping, err := mainTickerMapping(opts, cfg)
	if err != nil {
		return universe, err
	}
	for _, ticker := range universe.Tickers {
		cik := findCIKForTicker(ticker, mapping)
		if cik == "" {
			return universe, fmt.Errorf("no CIK mapping for requested ticker %s", ticker)
		}
		normalized := normalizeCIK(cik)
		universe.CIKs[normalized] = struct{}{}
		universe.TickerByCIK[normalized] = ticker
	}
	if len(universe.CIKs) == 0 {
		return universe, fmt.Errorf("no tickers configured for restricted scan")
	}
	return universe, nil
}

func scanFilingIndexes(downloader filingDownloader, universe tickerUniverse, now time.Time, lookback int) filingScan {
	end := newYorkDay(now).AddDate(0, 0, -1)
	listing := discoverPublishedIndexes(downloader, end.AddDate(0, 0, -lookback), end)
	scan := filingScan{URLs: []string{}, Failures: listing.Failures, Blocked: listing.Blocked}
	if scan.Blocked {
		return scan
	}
	seen := make(map[string]struct{})
	for _, indexURL := range listing.URLs {
		content, err := downloader.DownloadText(indexURL)
		if err != nil {
			scan.Failures = append(scan.Failures, fmt.Sprintf("index %s: %v", indexURL, err))
			if errors.Is(err, errSECBlocked) {
				scan.Blocked = true
				return scan
			}
			continue
		}
		if !isMasterIndex(content) {
			scan.Failures = append(scan.Failures, "invalid master index: "+indexURL)
			continue
		}
		scan.IndexCount++
		ciks := universe.CIKs
		if universe.AllIssuers {
			ciks = nil
		}
		for _, filingURL := range parseMasterIdx(content, ciks) {
			if _, exists := seen[filingURL]; exists {
				continue
			}
			seen[filingURL] = struct{}{}
			scan.URLs = append(scan.URLs, filingURL)
		}
	}
	return scan
}

func masterIndexDirectoryURL(date time.Time) string {
	quarter := (int(date.Month())-1)/3 + 1
	return fmt.Sprintf("%sedgar/daily-index/%d/QTR%d/", secBase, date.Year(), quarter)
}

type publishedIndexes struct {
	URLs     []string
	Failures []string
	Blocked  bool
}

type secDirectoryListing struct {
	Directory *secDirectory `json:"directory"`
}

type secDirectory struct {
	Items []secDirectoryItem `json:"item"`
}

type secDirectoryItem struct {
	Name string `json:"name"`
}

func discoverPublishedIndexes(downloader filingDownloader, start, end time.Time) publishedIndexes {
	listing := publishedIndexes{URLs: []string{}, Failures: []string{}}
	seenDirectories := make(map[string]struct{})
	for date := end; !date.Before(start); date = date.AddDate(0, 0, -1) {
		directoryURL := masterIndexDirectoryURL(date)
		if _, seen := seenDirectories[directoryURL]; seen {
			continue
		}
		seenDirectories[directoryURL] = struct{}{}
		content, err := downloader.DownloadText(directoryURL + "index.json")
		if err != nil {
			listing.Failures = append(listing.Failures, fmt.Sprintf("directory %s: %v", directoryURL, err))
			if errors.Is(err, errSECBlocked) {
				listing.Blocked = true
				return listing
			}
			continue
		}
		urls, err := publishedMasterIndexURLs(content, directoryURL, start, end)
		if err != nil {
			listing.Failures = append(listing.Failures, fmt.Sprintf("directory %s: %v", directoryURL, err))
			continue
		}
		listing.URLs = append(listing.URLs, urls...)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(listing.URLs)))
	return listing
}

func publishedMasterIndexURLs(content, directoryURL string, start, end time.Time) ([]string, error) {
	var listing secDirectoryListing
	if err := json.Unmarshal([]byte(content), &listing); err != nil {
		return nil, err
	}
	if listing.Directory == nil {
		return nil, fmt.Errorf("response is not a SEC directory listing")
	}
	seen := make(map[string]struct{})
	for _, item := range listing.Directory.Items {
		if !strings.HasPrefix(item.Name, "master.") || !strings.HasSuffix(item.Name, ".idx") {
			continue
		}
		dateText := strings.TrimSuffix(strings.TrimPrefix(item.Name, "master."), ".idx")
		date, err := time.ParseInLocation("20060102", dateText, newYorkLocation())
		if err != nil || date.Before(start) || date.After(end) {
			continue
		}
		seen[directoryURL+item.Name] = struct{}{}
	}
	return sortedKeys(seen), nil
}

func isMasterIndex(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		normalized := strings.ToLower(strings.Join(strings.Fields(line), ""))
		if normalized == "cik|companyname|formtype|datefiled|filename" {
			return true
		}
	}
	return false
}

func collectFilingTransactions(downloader filingDownloader, scan filingScan, universe tickerUniverse, minimumBuyUSD, minimumSellUSD int64) (map[string][]AlertEntry, []string) {
	transactions := make(map[string][]AlertEntry)
	failures := append([]string{}, scan.Failures...)
	if scan.Blocked {
		return transactions, failures
	}
	for _, filingURL := range scan.URLs {
		content, err := downloader.DownloadText(filingURL)
		if err != nil {
			failures = append(failures, fmt.Sprintf("filing %s: %v", filingURL, err))
			if errors.Is(err, errSECBlocked) {
				return transactions, failures
			}
			continue
		}
		parsed, err := parseForm4WithThresholds(content, minimumBuyUSD, minimumSellUSD, universe.TickerByCIK)
		if err != nil {
			failures = append(failures, fmt.Sprintf("parse %s: %v", filingURL, err))
			continue
		}
		for ticker, entries := range parsed {
			for _, entry := range entries {
				entry.filingURL = filingURL
				transactions[ticker] = append(transactions[ticker], entry)
			}
		}
	}
	return transactions, failures
}
