package main

import (
	"encoding/csv"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	defaultStrategyDaysBack      = 60
	defaultClusterWindowDays     = 60
	defaultMinClusterExecutives  = 3
	defaultStrategyMinBuyUSD     = int64(10_000)
	defaultMeaningfulSellUSD     = int64(250_000)
	defaultMinOwnershipRatio     = 0.01
	defaultSECRequestDelayMS     = 250
	defaultSECRetryCount         = 4
	defaultSECRetryBackoffMS     = 2_000
	defaultSECRetryMaxBackoffMS  = 60_000
	defaultDownloadCacheDir      = "history"
	defaultStrategyRevenueStatus = "unknown"
)

func readTickersFromCSV(filePath string) ([]string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for i, record := range records {
		for _, field := range record {
			ticker := normalizeTickerSymbol(field)
			if ticker == "" || (i == 0 && strings.Contains(strings.ToLower(field), "symbol")) {
				continue
			}
			if len(ticker) <= 6 {
				seen[ticker] = struct{}{}
				break
			}
		}
	}
	return sortedKeys(seen), nil
}

func normalizeTickerSymbol(value string) string {
	cleaned := strings.ToUpper(strings.TrimSpace(value))
	cleaned = strings.Trim(cleaned, "\"' ")
	cleaned = strings.ReplaceAll(cleaned, ".", "-")
	if !regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,9}$`).MatchString(cleaned) {
		return ""
	}
	return cleaned
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
