package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
)

const createTransactionEvidence = `CREATE TABLE IF NOT EXISTS transaction_evidence (
	transaction_id TEXT PRIMARY KEY,
	owner_key TEXT NOT NULL,
	filing_url TEXT NOT NULL,
	is_amendment INTEGER NOT NULL,
	joint_reporting INTEGER NOT NULL,
	is_10b51 INTEGER NOT NULL,
	holding_increase REAL,
	new_position INTEGER NOT NULL
)`

func saveTransactionEvidence(transaction *sql.Tx, record transactionRecord) error {
	_, err := transaction.Exec(`INSERT INTO transaction_evidence
		(transaction_id, owner_key, filing_url, is_amendment, joint_reporting, is_10b51, holding_increase, new_position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(transaction_id) DO UPDATE SET
		owner_key = excluded.owner_key, filing_url = excluded.filing_url,
		is_amendment = excluded.is_amendment, joint_reporting = excluded.joint_reporting,
		is_10b51 = excluded.is_10b51, holding_increase = excluded.holding_increase,
		new_position = excluded.new_position WHERE transaction_evidence.filing_url = ''`, record.ID, record.OwnerKey, record.FilingURL,
		record.IsAmendment, record.JointReporting, record.Is10b51, record.HoldingIncrease, record.NewPosition)
	return err
}

type sqliteTransactionReader struct {
	Path string
}

func (reader sqliteTransactionReader) ReadTransactions(startDate, endDate string) ([]transactionRecord, error) {
	absolutePath, err := filepath.Abs(reader.Path)
	if err != nil {
		return nil, err
	}
	databaseURL := url.URL{Scheme: "file", Path: absolutePath, RawQuery: "mode=ro"}
	database, err := sql.Open("sqlite3", databaseURL.String())
	if err != nil {
		return nil, err
	}
	defer database.Close()

	var evidenceExists bool
	err = database.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='transaction_evidence')`).Scan(&evidenceExists)
	if err != nil {
		return nil, err
	}
	return queryLeadTransactions(database, evidenceExists, startDate, endDate)
}

func queryLeadTransactions(database *sql.DB, evidenceExists bool, startDate, endDate string) ([]transactionRecord, error) {
	evidenceColumns := `'', '', 0, 0, NULL, 0`
	evidenceJoin := ""
	planColumn := "t.is10b51"
	if evidenceExists {
		evidenceColumns = `COALESCE(e.owner_key, ''), COALESCE(e.filing_url, ''),
			COALESCE(e.is_amendment, 0), COALESCE(e.joint_reporting, 0), e.holding_increase, COALESCE(e.new_position, 0)`
		evidenceJoin = `LEFT JOIN transaction_evidence e ON e.transaction_id = t.id`
		planColumn = "COALESCE(e.is_10b51, t.is10b51)"
	}
	query := fmt.Sprintf(`SELECT t.id, t.scan_date, t.ticker, t.owner_name, t.position, t.type,
		t.transaction_code, t.transaction_date, t.shares, t.price, t.amount, %s,
		t.shares_owned_after, %s FROM transactions t %s
		WHERE t.transaction_date >= ? AND t.transaction_date <= ?
		ORDER BY t.ticker, t.transaction_date, t.id`, planColumn, evidenceColumns, evidenceJoin)
	rows, err := database.Query(query, startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]transactionRecord, 0)
	for rows.Next() {
		var record transactionRecord
		err := rows.Scan(&record.ID, &record.ScanDate, &record.Ticker, &record.OwnerName,
			&record.Position, &record.Type, &record.TransactionCode, &record.TransactionDate,
			&record.Shares, &record.Price, &record.Amount, &record.Is10b51, &record.SharesOwnedAfter,
			&record.OwnerKey, &record.FilingURL, &record.IsAmendment, &record.JointReporting,
			&record.HoldingIncrease, &record.NewPosition)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
