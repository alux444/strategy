package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSECForbiddenStopsWithoutRepeatedRequests(t *testing.T) {
	previousDelay := secRequestDelay
	previousRetryCount := secRetryCount
	previousBackoff := secRetryBackoff
	previousMaxBackoff := secRetryMaxBackoff
	previousCache := downloadCacheDir
	setSECRequestDelay(0)
	setSECRetryPolicy(4, 0, 0)
	setDownloadCacheDir("")
	t.Cleanup(func() {
		setSECRequestDelay(previousDelay)
		setSECRetryPolicy(previousRetryCount, previousBackoff, previousMaxBackoff)
		setDownloadCacheDir(previousCache)
	})
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		response.WriteHeader(http.StatusForbidden)
		response.Write([]byte("<html><title>Your Request Originates from an Undeclared Automated Tool</title></html>"))
	}))
	defer server.Close()
	_, err := downloadText(server.URL)
	if !errors.Is(err, errSECBlocked) || requestCount.Load() != 1 || !strings.Contains(err.Error(), "rejected automated access") {
		t.Fatalf("forbidden request should stop immediately: requests=%d, error=%v", requestCount.Load(), err)
	}
	setDownloadCacheDir(t.TempDir())
	writeDownloadCache(server.URL, "previously cached filing")
	if cached, err := downloadText(server.URL); err != nil || cached != "previously cached filing" || requestCount.Load() != 1 {
		t.Fatalf("test did not establish a cached response: %q, %v", cached, err)
	}
	_, err = (secDownloader{Fresh: true}).DownloadText(server.URL)
	if !errors.Is(err, errSECBlocked) || requestCount.Load() != 2 {
		t.Fatalf("fresh check trusted cache and hid the access failure: requests=%d, error=%v", requestCount.Load(), err)
	}
}
