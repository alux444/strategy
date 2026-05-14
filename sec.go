package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var secRequestDelay = 200 * time.Millisecond
var secRetryCount = 3
var secRetryBackoff = time.Second
var secRetryMaxBackoff = 30 * time.Second
var downloadCacheDir = "history"
var secUserAgent = defaultSECUserAgent
var secContactEmail = defaultSECContactEmail
var errHTTPNotFound = errors.New("HTTP 404")

func setSECRequestDelay(delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	secRequestDelay = delay
}

func setSECRetryPolicy(retryCount int, initialBackoff, maxBackoff time.Duration) {
	if retryCount < 1 {
		retryCount = 1
	}
	if initialBackoff < 0 {
		initialBackoff = 0
	}
	if maxBackoff < initialBackoff {
		maxBackoff = initialBackoff
	}
	secRetryCount = retryCount
	secRetryBackoff = initialBackoff
	secRetryMaxBackoff = maxBackoff
}

func setDownloadCacheDir(cacheDir string) {
	downloadCacheDir = strings.TrimSpace(cacheDir)
}

func setSECIdentity(userAgent, contactEmail string) {
	secUserAgent = firstNonBlank(userAgent, os.Getenv("SEC_USER_AGENT"), defaultSECUserAgent)
	secContactEmail = firstNonBlank(contactEmail, os.Getenv("SEC_CONTACT_EMAIL"), defaultSECContactEmail)
}

func downloadText(rawURL string) (string, error) {
	if cached, ok := readDownloadCache(rawURL); ok {
		logTrace("Cache hit: " + rawURL)
		return cached, nil
	}

	var lastErr error
	for attempt := 1; attempt <= secRetryCount; attempt++ {
		if secRequestDelay > 0 {
			time.Sleep(secRequestDelay)
		}
		logTrace(fmt.Sprintf("HTTP GET attempt %d/%d: %s", attempt, secRetryCount, rawURL))
		ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return "", err
		}
		req.Header.Set("User-Agent", secUserAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("From", secContactEmail)

		client := &http.Client{Timeout: httpTimeout}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			sleepBeforeRetry(attempt, lastErr)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if readErr != nil {
			lastErr = readErr
			sleepBeforeRetry(attempt, lastErr)
			continue
		}

		switch resp.StatusCode {
		case http.StatusOK:
			if len(body) == 0 {
				return "", fmt.Errorf("empty response from %s", rawURL)
			}
			if isSECRateLimitBody(string(body)) {
				lastErr = fmt.Errorf("SEC rate limit response for %s (attempt %d)", rawURL, attempt)
				sleepBeforeRetry(attempt, lastErr)
				continue
			}
			text := string(body)
			writeDownloadCache(rawURL, text)
			return text, nil
		case http.StatusNotFound:
			return "", fmt.Errorf("%w for %s", errHTTPNotFound, rawURL)
		case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
			lastErr = fmt.Errorf("HTTP %d for %s (attempt %d)", resp.StatusCode, rawURL, attempt)
			sleepBeforeRetry(attempt, lastErr)
		default:
			lastErr = fmt.Errorf("HTTP %d for %s (attempt %d)", resp.StatusCode, rawURL, attempt)
			sleepBeforeRetry(attempt, lastErr)
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("failed to download %s after %d attempts", rawURL, secRetryCount)
}

func readDownloadCache(rawURL string) (string, bool) {
	if strings.TrimSpace(downloadCacheDir) == "" {
		return "", false
	}
	contentPath, _ := downloadCachePaths(rawURL)
	content, err := os.ReadFile(contentPath)
	if err != nil || len(content) == 0 {
		return "", false
	}
	if isSECRateLimitBody(string(content)) {
		logTrace("Ignoring cached SEC block page: " + rawURL)
		_ = os.Remove(contentPath)
		return "", false
	}
	return string(content), true
}

func writeDownloadCache(rawURL, content string) {
	if strings.TrimSpace(downloadCacheDir) == "" || strings.TrimSpace(content) == "" || isSECRateLimitBody(content) {
		return
	}
	contentPath, urlPath := downloadCachePaths(rawURL)
	if err := os.MkdirAll(filepath.Dir(contentPath), 0755); err != nil {
		logTrace(fmt.Sprintf("Failed to create cache dir %s: %v", filepath.Dir(contentPath), err))
		return
	}
	if err := os.WriteFile(contentPath, []byte(content), 0644); err != nil {
		logTrace(fmt.Sprintf("Failed to write cache file %s: %v", contentPath, err))
		return
	}
	_ = os.WriteFile(urlPath, []byte(rawURL+"\n"), 0644)
}

func downloadCachePaths(rawURL string) (string, string) {
	sum := sha256.Sum256([]byte(rawURL))
	key := hex.EncodeToString(sum[:])
	return filepath.Join(downloadCacheDir, key+".body"), filepath.Join(downloadCacheDir, key+".url")
}

func sleepBeforeRetry(attempt int, err error) {
	if attempt >= secRetryCount {
		return
	}
	delay := secRetryBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= secRetryMaxBackoff {
			delay = secRetryMaxBackoff
			break
		}
	}
	if delay > 0 {
		logDebug(fmt.Sprintf("%v; backing off for %s before retry %d/%d", err, delay, attempt+1, secRetryCount))
		time.Sleep(delay)
	}
}

func isSECRateLimitBody(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "request rate threshold exceeded") ||
		strings.Contains(lower, "undeclared automated tool") ||
		strings.Contains(lower, "please declare your traffic") ||
		strings.Contains(lower, "automated access to our sites must comply") ||
		strings.Contains(lower, "current guidelines limit users")
}

func findMasterIndex(startDate time.Time, maxLookbackDays int) *MasterIndex {
	date := startDate.In(newYorkLocation()).Truncate(24 * time.Hour)
	startDateOnly := date
	thresholdDate := startDateOnly.AddDate(0, 0, -maxLookbackDays)
	var combined strings.Builder
	var foundDate time.Time
	for current := startDateOnly; !current.Before(thresholdDate); current = current.AddDate(0, 0, -1) {
		dateStr := current.Format("20060102")
		quarter := (int(current.Month())-1)/3 + 1
		url := fmt.Sprintf("%sedgar/daily-index/%d/QTR%d/master.%s.idx", secBase, current.Year(), quarter, dateStr)
		logTrace("Attempting master index: " + url)
		content, err := downloadText(url)
		if err == nil && strings.TrimSpace(content) != "" {
			logDebug("Using SEC index: " + url)
			logTrace(fmt.Sprintf("Master index fetched: date=%s size=%d", dateStr, len(content)))
			combined.WriteString(content)
			if foundDate.IsZero() {
				foundDate = current
			}
		} else {
			if err != nil {
				logTrace(fmt.Sprintf("Master index fetch failed: %s -> %v", url, err))
			} else {
				logTrace(fmt.Sprintf("Master index empty: %s", url))
			}
		}
	}
	if combined.Len() > 0 && !foundDate.IsZero() {
		return &MasterIndex{indexDate: foundDate.Format("20060102"), content: combined.String()}
	}
	return nil
}

func parseMasterIdx(content string, ciks map[string]struct{}) []string {
	cleanCiks := make(map[string]struct{}, len(ciks))
	for cik := range ciks {
		cleanCiks[normalizeCIK(cik)] = struct{}{}
	}
	urls := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "CIK|") || strings.HasPrefix(line, "-----") {
			continue
		}
		parts := strings.SplitN(line, "|", 6)
		if len(parts) < 5 {
			continue
		}
		fileCIK := normalizeCIK(strings.TrimSpace(parts[0]))
		formType := strings.TrimSpace(parts[2])
		if !isForm4Type(formType) {
			continue
		}
		if _, ok := cleanCiks[fileCIK]; ok {
			filename := strings.TrimSpace(parts[4])
			if filename != "" {
				fullURL := secBase + filename
				if _, exists := seen[fullURL]; !exists {
					seen[fullURL] = struct{}{}
					urls = append(urls, fullURL)
				}
			}
		}
	}
	return urls
}

func isForm4Type(formType string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(formType))
	return normalized == "4" || normalized == "4/A"
}

func downloadTickerMapping() map[string]string {
	mapping := make(map[string]string)
	content, err := downloadText(tickerURL)
	if err == nil && strings.TrimSpace(content) != "" {
		mapping = parseTickerMappingContent(content)
	}

	logDebug(fmt.Sprintf("Loaded %d SEC ticker mappings from %s", len(mapping), tickerURL))
	if len(mapping) == 0 {
		for k, v := range fallbackTickerMap {
			mapping[k] = v
		}
		logDebug(fmt.Sprintf("Using %d fallback ticker mappings", len(mapping)))
	}
	return mapping
}

func loadTickerMappingFromFile(filePath string) (map[string]string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	mapping := parseTickerMappingContent(string(content))
	if len(mapping) == 0 {
		return nil, fmt.Errorf("no ticker mappings found in %s", filePath)
	}
	return mapping, nil
}

func parseTickerMappingContent(content string) map[string]string {
	mapping := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) != 2 {
			continue
		}
		ticker := normalizeTickerSymbol(parts[0])
		cik := sanitizeNumeric(parts[1])
		if ticker != "" && cik != "" {
			mapping[ticker] = cik
		}
	}
	return mapping
}

func findCIKForTicker(ticker string, tickerToCIK map[string]string) string {
	cleanInput := cleanTickerKey(ticker)
	if cleanInput == "" {
		return ""
	}
	for key, value := range tickerToCIK {
		if cleanTickerKey(key) == cleanInput {
			return value
		}
	}
	for key, value := range fallbackTickerMap {
		if cleanTickerKey(key) == cleanInput {
			return value
		}
	}
	return ""
}

func cleanTickerKey(ticker string) string {
	return regexp.MustCompile(`[^A-Z0-9]`).ReplaceAllString(strings.ToUpper(ticker), "")
}

func normalizeCIK(cik string) string {
	normalized := strings.TrimLeft(strings.TrimSpace(cik), "0")
	if normalized == "" {
		return "0"
	}
	return normalized
}

func fetchForm4URLsFromEdgarBrowse(ciks map[string]struct{}, endDate time.Time, maxLookbackDays int) []string {
	urls := make([]string, 0)
	seen := make(map[string]struct{})
	for cik := range ciks {
		browseURL := fmt.Sprintf("https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=4&owner=include&count=100&output=atom", cik)
		atomXML, err := downloadText(browseURL)
		if err != nil || strings.TrimSpace(atomXML) == "" {
			fmt.Fprintf(os.Stderr, "Warning: browse-edgar fallback failed for CIK %s\n", cik)
			continue
		}
		for _, u := range parseBrowseEdgarAtom(atomXML, endDate, maxLookbackDays) {
			if _, exists := seen[u]; !exists {
				seen[u] = struct{}{}
				urls = append(urls, u)
			}
		}
	}
	return urls
}

func parseBrowseEdgarAtom(atomXML string, endDate time.Time, maxLookbackDays int) []string {
	urls := make([]string, 0)
	seen := make(map[string]struct{})
	ny := newYorkLocation()
	end := endDate.In(ny).Truncate(24 * time.Hour)
	threshold := end.AddDate(0, 0, -maxLookbackDays)
	entryPattern := regexp.MustCompile(`(?is)<entry>(.*?)</entry>`)
	datePattern := regexp.MustCompile(`(?is)<filing-date>(.*?)</filing-date>`)
	hrefPattern := regexp.MustCompile(`(?is)<filing-href>(.*?)</filing-href>`)
	for _, entry := range entryPattern.FindAllStringSubmatch(atomXML, -1) {
		if len(entry) < 2 {
			continue
		}
		entryBody := entry[1]
		dateMatch := datePattern.FindStringSubmatch(entryBody)
		hrefMatch := hrefPattern.FindStringSubmatch(entryBody)
		if len(dateMatch) < 2 || len(hrefMatch) < 2 {
			continue
		}
		filingDate := strings.TrimSpace(dateMatch[1])
		filingHref := strings.TrimSpace(hrefMatch[1])
		logTrace(fmt.Sprintf("Atom entry found: date=%s href=%s", filingDate, filingHref))
		parsedDate, err := time.ParseInLocation("2006-01-02", filingDate, newYorkLocation())
		if err != nil {
			logTrace(fmt.Sprintf("Atom entry date parse failed: %s -> %v", filingDate, err))
			continue
		}
		keep := !parsedDate.Before(threshold) && !parsedDate.After(end)
		logTrace(fmt.Sprintf("Atom entry decision: parsed=%s threshold=%s keep=%t", parsedDate.Format("2006-01-02"), threshold.Format("2006-01-02"), keep))
		if !keep {
			continue
		}
		xmlURL := findForm4XMLURLFromIndexPage(filingHref)
		logTrace(fmt.Sprintf("Resolved xmlURL: %s -> %s", filingHref, xmlURL))
		if xmlURL != "" {
			if _, exists := seen[xmlURL]; !exists {
				seen[xmlURL] = struct{}{}
				urls = append(urls, xmlURL)
			}
		}
	}
	return urls
}

func findForm4XMLURLFromIndexPage(indexURL string) string {
	html, err := downloadText(indexURL)
	if err != nil || strings.TrimSpace(html) == "" {
		return ""
	}
	// First try: look specifically for form4.xml links
	xmlLinkPattern := regexp.MustCompile(`(?i)href="([^"]*?/form4\.xml)"`)
	matches := xmlLinkPattern.FindAllStringSubmatch(html, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		relative := strings.TrimSpace(match[1])
		fullURL := relative
		if !strings.HasPrefix(strings.ToLower(relative), "http") {
			fullURL = "https://www.sec.gov" + relative
		}
		return fullURL
	}

	// Fallback: find any .xml link in the index page and prefer ones with form4/ownership keywords
	xmlAnyPattern := regexp.MustCompile(`(?i)href="([^"]*?\.xml)"`)
	anyMatches := xmlAnyPattern.FindAllStringSubmatch(html, -1)
	var firstCandidate string
	for _, m := range anyMatches {
		if len(m) < 2 {
			continue
		}
		rel := strings.TrimSpace(m[1])
		if strings.HasPrefix(strings.ToLower(rel), "http") {
			// use as-is
		} else {
			rel = "https://www.sec.gov" + rel
		}
		lower := strings.ToLower(rel)
		// skip XSL transformations and obvious non-data files
		if strings.Contains(lower, "xsl") || strings.Contains(lower, "_styles") || strings.Contains(lower, ".xsd") {
			continue
		}
		if firstCandidate == "" {
			firstCandidate = rel
		}
		if strings.Contains(lower, "form4") || strings.Contains(lower, "ownership") || strings.Contains(lower, "nonderivative") || strings.Contains(lower, "transaction") {
			return rel
		}
	}

	// Heuristic: some index pages list a filing directory; try to derive xml by replacing -index.htm with .xml
	if strings.HasSuffix(strings.ToLower(indexURL), "-index.htm") {
		attempt := strings.TrimSuffix(indexURL, "-index.htm") + ".xml"
		if content, _ := downloadText(attempt); content != "" {
			return attempt
		}
	}

	return firstCandidate
}
