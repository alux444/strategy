package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

func downloadText(rawURL string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return "", err
		}
		userAgent := firstNonBlank(os.Getenv("SEC_USER_AGENT"), defaultSECUserAgent)
		contactEmail := firstNonBlank(os.Getenv("SEC_CONTACT_EMAIL"), defaultSECContactEmail)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("From", contactEmail)

		client := &http.Client{Timeout: httpTimeout}
		resp, err := client.Do(req)
		cancel()
		if err != nil {
			lastErr = err
			if attempt < 3 {
				time.Sleep(time.Second)
			}
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < 3 {
				time.Sleep(time.Second)
			}
			continue
		}

		switch resp.StatusCode {
		case http.StatusOK:
			if len(body) == 0 {
				return "", fmt.Errorf("empty response from %s", rawURL)
			}
			return string(body), nil
		case http.StatusForbidden, http.StatusNotFound:
			return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
		default:
			lastErr = fmt.Errorf("HTTP %d for %s (attempt %d)", resp.StatusCode, rawURL, attempt)
			if attempt < 3 {
				time.Sleep(2 * time.Second)
			}
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("failed to download %s after 3 attempts", rawURL)
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
		if !strings.HasPrefix(formType, "4") {
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

func downloadTickerMapping() map[string]string {
	mapping := make(map[string]string)
	content, err := downloadText(tickerURL)
	if err == nil && strings.TrimSpace(content) != "" {
		for _, line := range strings.Split(content, "\n") {
			parts := strings.Split(strings.TrimSpace(line), "\t")
			if len(parts) == 2 {
				mapping[strings.ToUpper(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
			}
		}
	}
	if len(mapping) == 0 {
		for k, v := range fallbackTickerMap {
			mapping[k] = v
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

func fetchForm4URLsFromEdgarBrowse(ciks map[string]struct{}, maxLookbackDays int) []string {
	urls := make([]string, 0)
	seen := make(map[string]struct{})
	for cik := range ciks {
		browseURL := fmt.Sprintf("https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=4&owner=include&count=100&output=atom", cik)
		atomXML, err := downloadText(browseURL)
		if err != nil || strings.TrimSpace(atomXML) == "" {
			fmt.Fprintf(os.Stderr, "Warning: browse-edgar fallback failed for CIK %s\n", cik)
			continue
		}
		for _, u := range parseBrowseEdgarAtom(atomXML, maxLookbackDays) {
			if _, exists := seen[u]; !exists {
				seen[u] = struct{}{}
				urls = append(urls, u)
			}
		}
	}
	return urls
}

func parseBrowseEdgarAtom(atomXML string, maxLookbackDays int) []string {
	urls := make([]string, 0)
	seen := make(map[string]struct{})
	threshold := time.Now().In(newYorkLocation()).Truncate(24*time.Hour).AddDate(0, 0, -maxLookbackDays)
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
		keep := !parsedDate.Before(threshold)
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
