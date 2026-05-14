package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const defaultTransactionsFile = "history/insider_transactions.db"

type transactionRecord struct {
	ID               string  `json:"id"`
	ScanDate         string  `json:"scanDate"`
	Ticker           string  `json:"ticker"`
	OwnerName        string  `json:"ownerName"`
	Position         string  `json:"position"`
	Type             string  `json:"type"`
	TransactionCode  string  `json:"transactionCode"`
	TransactionDate  string  `json:"transactionDate"`
	Shares           int64   `json:"shares"`
	Price            float64 `json:"price"`
	Amount           float64 `json:"amount"`
	Is10b51          bool    `json:"is10b51"`
	SharesOwnedAfter int64   `json:"sharesOwnedAfter"`
}

func appendTransactionRecords(filePath string, alertsByTicker map[string][]AlertEntry, scanDate string) (int, error) {
	filePath = firstNonBlank(filePath, defaultTransactionsFile)

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return 0, err
	}

	db, err := sql.Open("sqlite3", filePath)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	// Use WAL mode for concurrency and speed
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		// not fatal
	}

	create := `CREATE TABLE IF NOT EXISTS transactions (
		id TEXT PRIMARY KEY,
		scan_date TEXT,
		ticker TEXT,
		owner_name TEXT,
		position TEXT,
		type TEXT,
		transaction_code TEXT,
		transaction_date TEXT,
		shares INTEGER,
		price REAL,
		amount REAL,
		is10b51 INTEGER,
		shares_owned_after INTEGER
	);`
	if _, err := db.Exec(create); err != nil {
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO transactions(
		id, scan_date, ticker, owner_name, position, type, transaction_code, transaction_date,
		shares, price, amount, is10b51, shares_owned_after
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	tickers := make([]string, 0, len(alertsByTicker))
	for ticker := range alertsByTicker {
		tickers = append(tickers, ticker)
	}
	sort.Strings(tickers)

	written := 0
	for _, ticker := range tickers {
		for _, entry := range alertsByTicker[ticker] {
			record := buildTransactionRecord(ticker, entry, scanDate)
			is10 := 0
			if record.Is10b51 {
				is10 = 1
			}
			res, err := stmt.Exec(record.ID, record.ScanDate, record.Ticker, record.OwnerName, record.Position,
				record.Type, record.TransactionCode, record.TransactionDate, record.Shares, record.Price,
				record.Amount, is10, record.SharesOwnedAfter)
			if err != nil {
				tx.Rollback()
				return written, err
			}
			if ra, _ := res.RowsAffected(); ra > 0 {
				written++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return written, err
	}
	return written, nil
}

func buildTransactionRecord(ticker string, entry AlertEntry, scanDate string) transactionRecord {
	record := transactionRecord{
		ScanDate:         scanDate,
		Ticker:           strings.ToUpper(strings.TrimSpace(ticker)),
		OwnerName:        entry.ownerName,
		Position:         entry.position,
		Type:             entry.typeLabel,
		TransactionCode:  entry.transactionCode,
		TransactionDate:  entry.transactionDate,
		Shares:           entry.shares,
		Price:            entry.price,
		Amount:           entry.amount,
		Is10b51:          entry.is10b51,
		SharesOwnedAfter: entry.sharesOwnedAfter,
	}
	record.ID = transactionRecordID(record)
	return record
}

func transactionRecordID(record transactionRecord) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%.4f|%.2f",
		record.Ticker,
		strings.ToLower(strings.TrimSpace(record.OwnerName)),
		record.TransactionDate,
		record.TransactionCode,
		record.Shares,
		record.Price,
		record.Amount,
	)
}

func todayScanDate() string {
	return time.Now().In(newYorkLocation()).Format("2006-01-02")
}
