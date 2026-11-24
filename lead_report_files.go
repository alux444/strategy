package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func saveLeadReport(path string, report leadReport) (string, error) {
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	content = append(content, '\n')
	historyDirectory := strings.TrimSuffix(path, filepath.Ext(path)) + "-history"
	if err := archivePreviousLeadReport(path, historyDirectory); err != nil {
		return "", fmt.Errorf("archive previous report: %w", err)
	}
	archivePath, err := archiveLeadReport(historyDirectory, report.GeneratedAt, content)
	if err != nil {
		return "", fmt.Errorf("archive current report: %w", err)
	}
	return archivePath, writeLeadReportFile(path, content)
}

func archivePreviousLeadReport(path, historyDirectory string) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var metadata struct {
		GeneratedAt time.Time `json:"generatedAt"`
	}
	if err := json.Unmarshal(content, &metadata); err != nil {
		return fmt.Errorf("read report timestamp from %s: %w", path, err)
	}
	if metadata.GeneratedAt.IsZero() {
		return fmt.Errorf("report %s is missing generatedAt", path)
	}
	_, err = archiveLeadReport(historyDirectory, metadata.GeneratedAt, content)
	return err
}

func archiveLeadReport(directory string, generatedAt time.Time, content []byte) (string, error) {
	stamp := generatedAt.UTC().Format("2006-01-02T150405.000000000Z")
	digest := sha256.Sum256(content)
	path := filepath.Join(directory, fmt.Sprintf("%s-%x.json", stamp, digest[:8]))
	_, err := os.Stat(path)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, writeLeadReportFile(path, content)
}

func writeLeadReportFile(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".leads-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
