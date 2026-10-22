package main

import "time"

const (
	defaultMinimumBuyUSD    = int64(25_000)
	defaultLeadWindowDays   = 14
	minimumClusterBuyers    = 2
	minimumClusterUSD       = 100_000.0
	minimumCommitmentUSD    = 100_000.0
	minimumHoldingIncrease  = 0.10
	minimumLargeBuyUSD      = 250_000.0
	defaultLeadsFile        = "history/leads.json"
	clusterRankPoints       = 40
	commitmentRankPoints    = 30
	largePurchaseRankPoints = 25
	seniorBuyerRankPoints   = 15
	directorRankPoints      = 5
	repeatBuyerRankPoints   = 10
)

type transactionReader interface {
	ReadTransactions(startDate, endDate string) ([]transactionRecord, error)
}

type leadReport struct {
	GeneratedAt         time.Time        `json:"generatedAt"`
	WindowStart         string           `json:"windowStart"`
	WindowEnd           string           `json:"windowEnd"`
	Leads               []investmentLead `json:"leads"`
	Limitations         []string         `json:"limitations"`
	CollectionWarnings  []string         `json:"collectionWarnings"`
	UnverifiedPurchases int              `json:"unverifiedPurchases"`
}

type investmentLead struct {
	Ticker       string              `json:"ticker"`
	Score        int                 `json:"score"`
	Reasons      []string            `json:"reasons"`
	PurchaseUSD  float64             `json:"purchaseUsd"`
	BuyerCount   int                 `json:"buyerCount"`
	Purchases    []transactionRecord `json:"purchases"`
	ContextSales []transactionRecord `json:"contextSales"`
}

type tickerUniverse struct {
	AllIssuers  bool
	Tickers     []string
	CIKs        map[string]struct{}
	TickerByCIK map[string]string
}

type filingDownloader interface {
	DownloadText(rawURL string) (string, error)
}

type secDownloader struct{}

func (secDownloader) DownloadText(rawURL string) (string, error) {
	return downloadText(rawURL)
}

type filingScan struct {
	URLs       []string
	IndexCount int
	Failures   []string
	Blocked    bool
}
