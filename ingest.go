package main

import (
	"fmt"
	"os"
	"time"
)

const (
	defaultMainSECRequestDelayMS    = 1000
	defaultMainSECRetryCount        = 1
	defaultMainSECRetryBackoffMS    = 5000
	defaultMainSECRetryMaxBackoffMS = 120000
)

func configureSECFromSettings(opts map[string]string, cfg settings) {
	secUserAgent := firstNonBlank(opts["sec-user-agent"], cfg.SECUserAgent, os.Getenv("SEC_USER_AGENT"), defaultSECUserAgent)
	secContactEmail := firstNonBlank(opts["sec-contact-email"], cfg.SECContactEmail, os.Getenv("SEC_CONTACT_EMAIL"), defaultSECContactEmail)
	setSECIdentity(secUserAgent, secContactEmail)

	secRequestDelayMS := defaultMainSECRequestDelayMS
	if cfg.SECRequestDelayMS != nil {
		secRequestDelayMS = *cfg.SECRequestDelayMS
	}
	secRequestDelayMS = parseInt(firstNonBlank(opts["sec-request-delay-ms"], opts["request-delay-ms"], ""), secRequestDelayMS)
	setSECRequestDelay(time.Duration(secRequestDelayMS) * time.Millisecond)

	secRetryCount := defaultMainSECRetryCount
	if cfg.SECRetryCount != nil {
		secRetryCount = *cfg.SECRetryCount
	}
	secRetryCount = parseInt(firstNonBlank(opts["sec-retry-count"], opts["retry-count"], ""), secRetryCount)

	secRetryBackoffMS := defaultMainSECRetryBackoffMS
	if cfg.SECRetryBackoffMS != nil {
		secRetryBackoffMS = *cfg.SECRetryBackoffMS
	}
	secRetryBackoffMS = parseInt(firstNonBlank(opts["sec-retry-backoff-ms"], opts["retry-backoff-ms"], ""), secRetryBackoffMS)

	secRetryMaxBackoffMS := defaultMainSECRetryMaxBackoffMS
	if cfg.SECRetryMaxBackoffMS != nil {
		secRetryMaxBackoffMS = *cfg.SECRetryMaxBackoffMS
	}
	secRetryMaxBackoffMS = parseInt(firstNonBlank(opts["sec-retry-max-backoff-ms"], opts["retry-max-backoff-ms"], ""), secRetryMaxBackoffMS)
	setSECRetryPolicy(secRetryCount, time.Duration(secRetryBackoffMS)*time.Millisecond, time.Duration(secRetryMaxBackoffMS)*time.Millisecond)

	cacheDir := firstNonBlank(opts["cache-dir"], opts["history-dir"], cfg.CacheDir, defaultDownloadCacheDir)
	setDownloadCacheDir(cacheDir)
	logDebug(fmt.Sprintf("SEC fetch settings: requestDelay=%dms retries=%d cacheDir=%s", secRequestDelayMS, secRetryCount, cacheDir))
}

func mainTickerMapping(opts map[string]string, cfg settings) (map[string]string, error) {
	filePath := firstNonBlank(opts["ticker-map-file"], opts["ticker-mapping-file"], cfg.TickerMapFile)
	if filePath != "" {
		mapping, err := loadTickerMappingFromFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("ticker map file %s is configured but could not be loaded: %w", filePath, err)
		}
		logDebug(fmt.Sprintf("Loaded %d ticker mappings from %s", len(mapping), filePath))
		return mapping, nil
	}
	return downloadTickerMapping(), nil
}
