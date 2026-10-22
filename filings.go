package main

import (
	"errors"
	"fmt"
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
	scan := filingScan{URLs: []string{}, Failures: []string{}}
	seen := make(map[string]struct{})
	end := newYorkDay(now)
	for date := end; !date.Before(end.AddDate(0, 0, -lookback)); date = date.AddDate(0, 0, -1) {
		if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
			continue
		}
		quarter := (int(date.Month())-1)/3 + 1
		indexURL := fmt.Sprintf("%sedgar/daily-index/%d/QTR%d/master.%s.idx", secBase, date.Year(), quarter, date.Format("20060102"))
		content, err := downloader.DownloadText(indexURL)
		if errors.Is(err, errHTTPNotFound) {
			continue
		}
		if err != nil {
			scan.Failures = append(scan.Failures, fmt.Sprintf("index %s: %v", date.Format("2006-01-02"), err))
			if errors.Is(err, errSECBlocked) {
				scan.Blocked = true
				return scan
			}
			continue
		}
		if !strings.Contains(content, "CIK|Company Name|Form Type|Date Filed|Filename") {
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
