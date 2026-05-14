package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var debugEnabled = defaultDebug

func setDebug(enabled bool) {
	debugEnabled = enabled
}

var verboseDebug = false

func setVerbose(enabled bool) {
	verboseDebug = enabled
}

func logDebug(message string) {
	if debugEnabled {
		fmt.Println("DEBUG: " + message)
	}
}

func logTrace(message string) {
	if verboseDebug {
		fmt.Println("TRACE: " + message)
	}
}

func parseInt64(value string, fallback int64) int64 {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		return parsed
	}
	return fallback
}

func parseInt(value string, fallback int) int {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		return parsed
	}
	return fallback
}

func parseBool(value string, fallback bool) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	s := strings.ToLower(strings.TrimSpace(value))
	return !(s == "false" || s == "0" || s == "no" || s == "off")
}

func newYorkLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.FixedZone("EST", -5*60*60)
	}
	return loc
}

func shouldSuppressErrorNotification(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "No Form 4 filings found") ||
		strings.Contains(msg, "No large insider transactions found") ||
		strings.Contains(msg, "No valid CIKs found")
}
