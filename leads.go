package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func buildLeadReport(reader transactionReader, now time.Time) (leadReport, error) {
	end := newYorkDay(now)
	start := end.AddDate(0, 0, -(defaultLeadWindowDays - 1))
	report := leadReport{
		GeneratedAt: now,
		WindowStart: start.Format("2006-01-02"),
		WindowEnd:   end.Format("2006-01-02"),
		Leads:       make([]investmentLead, 0),
		Limitations: []string{
			"The shortlist covers stored transactions only; missing filings and purchases below the collection floor can affect results.",
			"Ranks summarize insider buying evidence; valuation and business fundamentals are not assessed.",
			"P includes open-market and private purchases; review source filings and footnotes.",
			"Holding increases compare a direct purchase with its reported post-transaction holding, not total personal wealth.",
			"Planned purchases, amendments, and joint reporting do not qualify as buying signals. Historical rows without source evidence also do not qualify.",
			"Amendments do not automatically remove previously collected original transactions; review source filings.",
		},
	}
	records, err := reader.ReadTransactions(report.WindowStart, report.WindowEnd)
	if err != nil {
		return report, err
	}
	byTicker := make(map[string][]transactionRecord)
	for _, record := range records {
		if record.TransactionCode == "P" && record.FilingURL == "" {
			report.UnverifiedPurchases++
		}
		byTicker[record.Ticker] = append(byTicker[record.Ticker], record)
	}
	for ticker, transactions := range byTicker {
		lead, qualified := evaluateLead(ticker, transactions)
		if qualified {
			report.Leads = append(report.Leads, lead)
		}
	}
	sort.Slice(report.Leads, func(left, right int) bool {
		if report.Leads[left].Score != report.Leads[right].Score {
			return report.Leads[left].Score > report.Leads[right].Score
		}
		if report.Leads[left].PurchaseUSD != report.Leads[right].PurchaseUSD {
			return report.Leads[left].PurchaseUSD > report.Leads[right].PurchaseUSD
		}
		return report.Leads[left].Ticker < report.Leads[right].Ticker
	})
	return report, nil
}

func evaluateLead(ticker string, transactions []transactionRecord) (investmentLead, bool) {
	lead := investmentLead{Ticker: ticker, Reasons: []string{}, Purchases: []transactionRecord{}, ContextSales: []transactionRecord{}}
	buyers := make(map[string]struct{})
	for _, transaction := range transactions {
		if transaction.TransactionCode == "S" {
			lead.ContextSales = append(lead.ContextSales, transaction)
			continue
		}
		if !qualifyingPurchase(transaction) {
			continue
		}
		lead.Purchases = append(lead.Purchases, transaction)
		lead.PurchaseUSD += transaction.Amount
		buyers[purchaseOwnerIdentity(transaction)] = struct{}{}
	}
	lead.BuyerCount = len(buyers)
	if lead.BuyerCount >= minimumClusterBuyers && lead.PurchaseUSD >= minimumClusterUSD {
		lead.Score += clusterRankPoints
		lead.Reasons = append(lead.Reasons, fmt.Sprintf("%d distinct insiders bought %s within %d calendar days", lead.BuyerCount, formatAmount(lead.PurchaseUSD), defaultLeadWindowDays))
	}
	addCommitmentReasons(&lead)
	if len(lead.Reasons) == 0 {
		return lead, false
	}
	addRoleAndRepeatReasons(&lead)
	return lead, true
}

func qualifyingPurchase(transaction transactionRecord) bool {
	owner := normalizedOwnerName(transaction.OwnerName)
	return transaction.TransactionCode == "P" && transaction.Amount >= float64(defaultMinimumBuyUSD) &&
		transaction.Shares > 0 && transaction.Price > 0 && owner != "" && owner != "unknown owner" &&
		transaction.FilingURL != "" && !transaction.Is10b51 && !transaction.IsAmendment && !transaction.JointReporting
}

func normalizedOwnerName(owner string) string {
	return strings.ToLower(strings.Join(strings.Fields(owner), " "))
}

func purchaseOwnerIdentity(purchase transactionRecord) string {
	if purchase.OwnerKey != "" && normalizeCIK(purchase.OwnerKey) != "0" {
		return "cik:" + normalizeCIK(purchase.OwnerKey)
	}
	return "name:" + normalizedOwnerName(purchase.OwnerName)
}

func addCommitmentReasons(lead *investmentLead) {
	largePurchase := false
	meaningfulCommitment := false
	for _, purchase := range lead.Purchases {
		if purchase.Amount >= minimumLargeBuyUSD {
			largePurchase = true
		}
		increasedHolding := purchase.HoldingIncrease != nil && *purchase.HoldingIncrease >= minimumHoldingIncrease
		if purchase.Amount < minimumCommitmentUSD || (!purchase.NewPosition && !increasedHolding) {
			continue
		}
		meaningfulCommitment = true
		if purchase.NewPosition {
			lead.Reasons = append(lead.Reasons, fmt.Sprintf("%s bought %s to establish a reported direct holding", purchase.OwnerName, formatAmount(purchase.Amount)))
			continue
		}
		lead.Reasons = append(lead.Reasons, fmt.Sprintf("%s bought %s, increasing the reported direct holding by %.1f%%", purchase.OwnerName, formatAmount(purchase.Amount), *purchase.HoldingIncrease*100))
	}
	if meaningfulCommitment {
		lead.Score += commitmentRankPoints
	}
	if largePurchase {
		lead.Score += largePurchaseRankPoints
		lead.Reasons = append(lead.Reasons, "At least one individual purchase of $250,000 or more")
	}
}

func addRoleAndRepeatReasons(lead *investmentLead) {
	buyerDates := make(map[string]map[string]struct{})
	seniorBuyer := false
	directorBuyer := false
	for _, purchase := range lead.Purchases {
		position := strings.ToLower(purchase.Position)
		for _, title := range []string{"chief executive", "chief financial", "ceo", "cfo"} {
			seniorBuyer = seniorBuyer || strings.Contains(position, title)
		}
		directorBuyer = directorBuyer || hasDirectorPosition(purchase.Position)
		owner := purchaseOwnerIdentity(purchase)
		if buyerDates[owner] == nil {
			buyerDates[owner] = make(map[string]struct{})
		}
		buyerDates[owner][purchase.TransactionDate] = struct{}{}
	}
	if seniorBuyer {
		lead.Score += seniorBuyerRankPoints
		lead.Reasons = append(lead.Reasons, "CEO or CFO participated in the qualifying purchases")
	}
	if directorBuyer {
		lead.Score += directorRankPoints
		lead.Reasons = append(lead.Reasons, "A director participated in the qualifying purchases")
	}
	for _, dates := range buyerDates {
		if len(dates) >= 2 {
			lead.Score += repeatBuyerRankPoints
			lead.Reasons = append(lead.Reasons, "An insider bought on multiple distinct dates")
			return
		}
	}
}

func hasDirectorPosition(position string) bool {
	for _, title := range strings.Split(position, ",") {
		if strings.EqualFold(strings.TrimSpace(title), "director") {
			return true
		}
	}
	return false
}
