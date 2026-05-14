package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	secBase                = "https://www.sec.gov/Archives/"
	tickerURL              = "https://www.sec.gov/include/ticker.txt"
	defaultSECUserAgent    = "SEC-Insider-Bot AdminContact@example.com"
	defaultSECContactEmail = "contact@example.com"
	defaultMinimumUSD      = int64(500_000)
	defaultMaxLookbackDays = 1
	defaultDebug           = true
	defaultSettingsFile    = "settings.json"
)

var httpTimeout = 30 * time.Second

// use the ticker url to get these mappings - usually blocked
var fallbackTickerMap = map[string]string{
	"AMD":   "0000002488",
	"AMZN":  "0001018724",
	"BRKB":  "1067983",
	"BRK-B": "1067983",
	"CLOV":  "0001801170",
	"GOOG":  "0001652044",
	"GOOGL": "0001652044",
	"GRAB":  "0001855612",
	"META":  "0001326801",
	"MLYS":  "0001818096",
	"MSFT":  "0000789019",
	"NOW":   "0001373715",
	"NVDA":  "0001045810",
	"SNOW":  "0001640147",
	"STZ":   "0001593873",
	"TEAM":  "0001650372",
	"ZTS":   "0001555285",
}

func parseForm4(rawXML string, minimumUSD int64, cikToRequestedTicker map[string]string) (map[string][]AlertEntry, error) {
	alerts := make(map[string][]AlertEntry)
	xmlPayload := extractXMLPayload(rawXML)
	if strings.TrimSpace(xmlPayload) == "" {
		logDebug("Skipping file: Could not extract valid XML payload.")
		return alerts, nil
	}
	if !strings.Contains(xmlPayload, "<ownershipDocument") {
		logTrace("Skipping file: XML payload is not an ownershipDocument.")
		return alerts, nil
	}

	var doc ownershipDocument
	if err := xml.Unmarshal([]byte(xmlPayload), &doc); err != nil {
		return alerts, err
	}

	rawXMLCIK := firstNonBlank(doc.Issuer.IssuerCIK.String(), doc.Issuer.IssuerCIKAlt.String(), "Unknown")
	normalizedXMLCIK := normalizeCIK(rawXMLCIK)
	ticker := cikToRequestedTicker[normalizedXMLCIK]
	if ticker == "" {
		ticker = firstNonBlank(doc.Issuer.IssuerTradingSymbol, "Unknown")
	}

	if !isOfficerOrDirector(doc.ReportingOwner) {
		logDebug("Skipping Form 4 for " + ticker + " - reporter is not an officer/director.")
		return alerts, nil
	}

	ownerName := firstNonBlank(doc.ReportingOwner.ReportingOwnerID.RptOwnerName, "Unknown Owner")
	position := extractPosition(doc.ReportingOwner)

	if len(doc.NonDerivTable.Transactions) == 0 {
		logDebug("No non-derivativeTable for " + ticker)
		alerts[ticker] = []AlertEntry{}
		return alerts, nil
	}

	for _, tx := range doc.NonDerivTable.Transactions {
		entry := processTransaction(tx, ownerName, position, minimumUSD)
		if entry != nil {
			alerts[ticker] = append(alerts[ticker], *entry)
		}
	}
	if _, ok := alerts[ticker]; !ok {
		alerts[ticker] = []AlertEntry{}
	}
	return alerts, nil
}

func isOfficerOrDirector(ro reportingOwner) bool {
	rel := ro.ReportingOwnerRelationship
	return truthy(rel.IsDirector) || truthy(rel.IsOfficer)
}

func truthy(value string) bool {
	s := strings.ToLower(strings.TrimSpace(value))
	return s == "true" || s == "1"
}

func extractPosition(ro reportingOwner) string {
	rel := ro.ReportingOwnerRelationship
	titles := make([]string, 0, 3)
	appendIfPresent := func(field string) {
		var value string
		switch field {
		case "officerTitle":
			value = rel.OfficerTitle
		case "directorTitle":
			value = rel.DirectorTitle
		case "otherTitle":
			value = rel.OtherTitle
		}
		if strings.TrimSpace(value) != "" {
			titles = append(titles, strings.TrimSpace(value))
		}
	}
	appendIfPresent("officerTitle")
	appendIfPresent("directorTitle")
	appendIfPresent("otherTitle")
	if len(titles) > 0 {
		return strings.Join(titles, ", ")
	}
	if strings.TrimSpace(ro.ReportingOwnerID.RptOwnerTitle) != "" {
		return strings.TrimSpace(ro.ReportingOwnerID.RptOwnerTitle)
	}
	return "Unknown Position"
}

func processTransaction(tx nonDerivativeTransaction, ownerName, position string, minimumUSD int64) *AlertEntry {
	code := strings.TrimSpace(tx.TransactionCoding.TransactionCode)
	if code != "P" && code != "S" {
		logDebug("Skipping transaction: code=" + code + " (not P/S)")
		return nil
	}

	shares := extractInt64(tx.TransactionAmounts.TransactionShares.String())
	price := extractFloat64(tx.TransactionAmounts.TransactionPricePerShare.String())
	if shares <= 0 || price <= 0 {
		logDebug(fmt.Sprintf("Skipping transaction: code=%s shares=%d price=%f", code, shares, price))
		return nil
	}

	amount := float64(shares) * price
	if amount < float64(minimumUSD) {
		logDebug(fmt.Sprintf("Skipping transaction: code=%s amount=%f < threshold=%d", code, amount, minimumUSD))
		return nil
	}

	typeLabel := "BUY"
	if code == "S" {
		typeLabel = "SELL"
	}
	isPlan := truthy(tx.TransactionCoding.Is10b51Transaction)

	transactionDate := strings.TrimSpace(tx.TransactionDate.String())
	if len(transactionDate) >= 10 {
		transactionDate = transactionDate[:10]
	}

	sharesOwnedAfter := extractInt64(tx.PostTransactionAmounts.SharesOwnedFollowingTransaction.String())
	if sharesOwnedAfter <= 0 {
		sharesOwnedAfter = extractInt64(tx.SharesOwnedFollowingTxn.String())
	}

	logDebug(fmt.Sprintf("Creating alert: %s %s %d shares at %f amount=%f date=%s ownedAfter=%d", ownerName, typeLabel, shares, price, amount, transactionDate, sharesOwnedAfter))
	return &AlertEntry{
		ownerName:        ownerName,
		position:         position,
		typeLabel:        typeLabel,
		transactionCode:  code,
		shares:           shares,
		price:            price,
		amount:           amount,
		is10b51:          isPlan,
		transactionDate:  transactionDate,
		sharesOwnedAfter: sharesOwnedAfter,
	}
}

func extractInt64(text string) int64 {
	cleaned := sanitizeNumeric(text)
	if cleaned == "" {
		return 0
	}
	if parsed, err := strconv.ParseFloat(cleaned, 64); err == nil {
		return int64(parsed)
	}
	return 0
}

func extractFloat64(text string) float64 {
	cleaned := sanitizeNumeric(text)
	if cleaned == "" {
		return 0
	}
	if parsed, err := strconv.ParseFloat(cleaned, 64); err == nil {
		return parsed
	}
	return 0
}

func sanitizeNumeric(text string) string {
	var b strings.Builder
	for _, r := range text {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func extractXMLPayload(rawText string) string {
	if rawText == "" {
		return ""
	}
	cleanXML := ""
	if xmlStart := strings.Index(rawText, "<XML>"); xmlStart >= 0 {
		if xmlEnd := strings.Index(rawText[xmlStart:], "</XML>"); xmlEnd > 0 {
			cleanXML = rawText[xmlStart+5 : xmlStart+xmlEnd]
		}
	}
	if strings.TrimSpace(cleanXML) == "" {
		re := regexp.MustCompile(`(?is)<ownershipDocument[^>]*>.*?</ownershipDocument>`)
		if match := re.FindString(rawText); match != "" {
			cleanXML = match
		}
	}
	if strings.TrimSpace(cleanXML) == "" {
		return ""
	}
	controlChars := regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F]`)
	cleanXML = controlChars.ReplaceAllString(cleanXML, "")
	// Escape stray ampersands by encoding all, then restore known entities and numeric entities.
	cleanXML = strings.ReplaceAll(cleanXML, "&", "&amp;")
	// Restore common named entities
	for _, ent := range []string{"amp", "apos", "quot", "lt", "gt"} {
		cleanXML = strings.ReplaceAll(cleanXML, "&amp;"+ent+";", "&"+ent+";")
	}
	// Restore numeric entities like &#1234;
	reNumeric := regexp.MustCompile(`&amp;#(\d+);`)
	cleanXML = reNumeric.ReplaceAllString(cleanXML, "&#$1;")
	cleanXML = regexp.MustCompile(`</\s+`).ReplaceAllString(cleanXML, "</")
	// collapse stray whitespace after '<' when followed by a name character
	reTag := regexp.MustCompile(`<\s+([a-zA-Z_/?!])`)
	cleanXML = reTag.ReplaceAllString(cleanXML, "<$1")
	// escape '<' when followed by a non-name character
	reBad := regexp.MustCompile(`<([^a-zA-Z_/?!])`)
	cleanXML = reBad.ReplaceAllString(cleanXML, "&lt;$1")
	return strings.TrimSpace(cleanXML)
}

func buildGroupedNotification(alertsByTicker map[string][]AlertEntry, orderedTickers []string, indexDate string) string {
	var msg strings.Builder
	msg.WriteString("⏰ Insider Alerts (" + indexDate + ")\n\n")
	if len(orderedTickers) == 0 {
		orderedTickers = make([]string, 0, len(alertsByTicker))
		for ticker := range alertsByTicker {
			orderedTickers = append(orderedTickers, ticker)
		}
	}

	firstTicker := true
	for _, ticker := range orderedTickers {
		entries := alertsByTicker[ticker]
		if !firstTicker {
			msg.WriteString("─────────────────────────────────\n\n")
		}
		firstTicker = false

		for _, e := range entries {
			planIcon := ""
			if e.is10b51 {
				planIcon = " 🏷️[10b5-1]"
			}
			date := e.transactionDate
			if strings.TrimSpace(date) == "" {
				date = "N/A"
			}
			sharesStr := formatNumber(e.shares)
			amountStr := formatAmount(e.amount)
			positionStr := "N/A"
			if e.sharesOwnedAfter > 0 {
				positionStr = formatNumber(e.sharesOwnedAfter)
			}
			actionIcon := "📉 SELL"
			if e.typeLabel == "BUY" {
				actionIcon = "📈 BUY"
				msg.WriteString("🔴 ")
			}
			msg.WriteString("**" + ticker + "** · " + actionIcon + " · **" + amountStr + "**\n")
			msg.WriteString("  " + date + " · " + e.ownerName + "\n")
			msg.WriteString("  " + e.position + planIcon + "\n")
			msg.WriteString(fmt.Sprintf("  %s @ **$%.2f** · positionStrength %s\n\n", sharesStr, e.price, positionStr))
		}
	}
	return strings.TrimSpace(msg.String())
}

func formatNumber(num int64) string {
	if num >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(num)/1_000_000.0)
	}
	if num >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(num)/1_000.0)
	}
	return strconv.FormatInt(num, 10)
}

func formatAmount(amount float64) string {
	if amount >= 1_000_000 {
		return fmt.Sprintf("$%.1fM", amount/1_000_000.0)
	}
	if amount >= 1_000 {
		return fmt.Sprintf("$%.1fK", amount/1_000.0)
	}
	return fmt.Sprintf("$%.0f", amount)
}

func buildMissingNotification(tickers []string, reason string) string {
	var msg strings.Builder
	msg.WriteString("🔔 Insider Alerts\n\n")
	for _, ticker := range tickers {
		msg.WriteString("▶ " + ticker + "\n  " + reason + "\n\n")
	}
	return strings.TrimSpace(msg.String())
}

func sendNotification(message string) bool {
	dingTalkURL := os.Getenv("DING_WEBHOOK_URL")
	if strings.TrimSpace(dingTalkURL) != "" {
		dingTalkSecret := os.Getenv("DING_WEBHOOK_SIGN")
		return sendDingTalkWebhook(dingTalkURL, dingTalkSecret, "Insider Alert", message)
	}

	// discordURL := os.Getenv("DISCORD_WEBHOOK_URL")
	// if strings.TrimSpace(discordURL) != "" {
	// 	return sendDiscordWebhook(discordURL, "Insider Alert", message)
	// }
	log.Println(message)
	return false
}

func sendDingTalkWebhook(webhookURL, secret, title, message string) bool {
	signedURL, err := buildDingTalkURL(webhookURL, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to build DingTalk URL: %v\n", err)
		return false
	}
	markdown := "### " + title + "\n\n" + message
	payload := fmt.Sprintf(`{"msgtype":"markdown","markdown":{"title":"%s","text":"%s"}}`, escapeJSON(title), escapeJSON(markdown))

	client := &http.Client{Timeout: httpTimeout}
	req, err := http.NewRequest(http.MethodPost, signedURL, strings.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create DingTalk request: %v\n", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to send DingTalk notification: %v\n", err)
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	success := resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.Contains(strings.ReplaceAll(string(body), " ", ""), `"errcode":0`)
	if !success {
		fmt.Fprintf(os.Stderr, "Warning: DingTalk notification failed. status=%d body=%s\n", resp.StatusCode, string(body))
	}
	return success
}

func buildDingTalkURL(webhookURL, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return webhookURL, nil
	}
	timestamp := time.Now().UnixMilli()
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(stringToSign)); err != nil {
		return "", err
	}
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	sep := "?"
	if strings.Contains(webhookURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%stimestamp=%d&sign=%s", webhookURL, sep, timestamp, sign), nil
}

func escapeJSON(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\r")
	return value
}

// helper functions moved to helpers.go

func main() {
	opts := parseOptions(os.Args[1:])
	settingsFile := firstNonBlank(opts["settings"], opts["config"], "")
	cfg := loadSettings(settingsFile)
	configureSECFromSettings(opts, cfg)

	// Determine tickers
	tickersArg := firstNonBlank(opts["tickers"], joinTickers(cfg.Tickers))
	var tickers []string
	if tickersArg != "" {
		tickers = parseTickers(tickersArg)
	} else if filePath := firstNonBlank(opts["holdings-file"], cfg.HoldingsFile); filePath != "" {
		loaded, err := readTickersFromCSV(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load holdings file %s: %v\n", filePath, err)
			os.Exit(1)
		}
		tickers = loaded
	}
	if len(tickers) == 0 {
		fmt.Fprintln(os.Stderr, "No tickers configured. Use --tickers, settings.json tickers, or holdingsFile.")
		os.Exit(1)
	}
	fmt.Println("Loaded " + strconv.Itoa(len(tickers)) + " tickers to monitor.")

	// Debug flags
	if cfg.Debug != nil {
		setDebug(*cfg.Debug)
	}
	if v, ok := opts["verbose"]; ok && (v == "true" || v == "1") {
		setVerbose(true)
	} else if cfg.Verbose != nil {
		setVerbose(*cfg.Verbose)
	}

	// Thresholds
	minimumUSD := defaultMinimumUSD
	if cfg.ThresholdUSD != nil {
		minimumUSD = *cfg.ThresholdUSD
	}
	if v, ok := opts["minusd"]; ok && v != "" {
		if parsed := parseInt64(v, minimumUSD); parsed > 0 {
			minimumUSD = parsed
		}
	}

	lookback := defaultMaxLookbackDays
	if cfg.LookbackDays != nil {
		lookback = *cfg.LookbackDays
	}
	if v, ok := opts["lookback"]; ok && v != "" {
		if p := parseInt(v, lookback); p > 0 {
			lookback = p
		}
	}

	tickerMap, err := mainTickerMapping(opts, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ciks := make(map[string]struct{})
	cikToRequested := make(map[string]string)
	for _, t := range tickers {
		cik := findCIKForTicker(t, tickerMap)
		if cik == "" {
			logTrace("No CIK found for ticker: " + t)
			continue
		}
		normalized := normalizeCIK(cik)
		ciks[normalized] = struct{}{}
		cikToRequested[normalized] = t
	}

	if len(ciks) == 0 {
		fmt.Fprintln(os.Stderr, "No valid CIKs found for provided tickers.")
		os.Exit(1)
	}

	// Try master index first
	start := time.Now().In(newYorkLocation())
	master := findMasterIndex(start, lookback)
	var formURLs []string
	if master != nil {
		formURLs = parseMasterIdx(master.content, ciks)
	}
	if len(formURLs) == 0 {
		// Fallback to browse-edgar
		formURLs = fetchForm4URLsFromEdgarBrowse(ciks, time.Now().In(newYorkLocation()), lookback)
	}

	alertsByTicker := make(map[string][]AlertEntry)
	for _, u := range formURLs {
		content, err := downloadText(u)
		if err != nil {
			logTrace(fmt.Sprintf("Failed to download %s: %v", u, err))
			continue
		}
		parsed, err := parseForm4(content, minimumUSD, cikToRequested)
		if err != nil {
			logTrace(fmt.Sprintf("Failed to parse Form4 from %s: %v", u, err))
			continue
		}
		for k, v := range parsed {
			if _, ok := alertsByTicker[k]; !ok {
				alertsByTicker[k] = []AlertEntry{}
			}
			alertsByTicker[k] = append(alertsByTicker[k], v...)
		}
	}

	transactionsFile := firstNonBlank(opts["transactions-file"], opts["db"], cfg.TransactionsFile, defaultTransactionsFile)
	written, err := appendTransactionRecords(transactionsFile, alertsByTicker, todayScanDate())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record transactions: %v\n", err)
	} else {
		logDebug(fmt.Sprintf("Recorded %d new transactions to %s", written, transactionsFile))
	}

	notify := true
	if cfg.Notify != nil {
		notify = *cfg.Notify
	}
	if v := firstNonBlank(opts["notify"], ""); v != "" {
		notify = parseBool(v, notify)
	}
	if !notify {
		return
	}

	// Build and send notification
	indexDate := time.Now().In(newYorkLocation()).Format("2006-01-02")
	// Order tickers as provided
	message := buildGroupedNotification(alertsByTicker, tickers, indexDate)
	if strings.TrimSpace(message) == "" {
		miss := buildMissingNotification(tickers, "No large insider transactions found")
		sendNotification(miss)
		os.Exit(0)
	}
	sendNotification(message)
}
