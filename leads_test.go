package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func testPurchase(owner string, amount float64) transactionRecord {
	return transactionRecord{
		Ticker: "TEST", OwnerName: owner, Position: "Director", TransactionCode: "P",
		TransactionDate: "2026-10-20", Amount: amount, Shares: 1000, Price: amount / 1000,
		FilingURL: "https://www.sec.gov/Archives/edgar/data/1/purchase.txt",
	}
}

func TestLeadQualification(t *testing.T) {
	increase := 0.10
	belowIncrease := 0.099
	commitment := testPurchase("Buyer One", minimumCommitmentUSD)
	commitment.HoldingIncrease = &increase
	belowCommitment := commitment
	belowCommitment.HoldingIncrease = &belowIncrease
	newHolding := commitment
	newHolding.HoldingIncrease = nil
	newHolding.NewPosition = true
	planned := testPurchase("Buyer One", 1_000_000)
	planned.Is10b51 = true
	joint := planned
	joint.Is10b51 = false
	joint.JointReporting = true
	amendment := joint
	amendment.JointReporting = false
	amendment.IsAmendment = true
	legacy := testPurchase("Buyer One", 1_000_000)
	legacy.FilingURL = ""
	sale := testPurchase("Seller", 1_000_000)
	sale.TransactionCode = "S"

	tests := []struct {
		name      string
		purchases []transactionRecord
		qualified bool
	}{
		{"small isolated buy", []transactionRecord{testPurchase("Buyer One", 50_000)}, false},
		{"cluster boundary", []transactionRecord{testPurchase("Buyer One", 50_000), testPurchase("Buyer Two", 50_000)}, true},
		{"cluster too small", []transactionRecord{testPurchase("Buyer One", 49_999), testPurchase("Buyer Two", 50_000)}, false},
		{"same buyer is not a cluster", []transactionRecord{testPurchase(" Buyer ONE ", 50_000), testPurchase("buyer one", 50_000)}, false},
		{"large individual purchase", []transactionRecord{testPurchase("Buyer One", minimumLargeBuyUSD)}, true},
		{"commitment boundary", []transactionRecord{commitment}, true},
		{"below commitment", []transactionRecord{belowCommitment}, false},
		{"new direct holding", []transactionRecord{newHolding}, true},
		{"planned purchase", []transactionRecord{planned}, false},
		{"joint reporting", []transactionRecord{joint}, false},
		{"amendment", []transactionRecord{amendment}, false},
		{"legacy without evidence", []transactionRecord{legacy}, false},
		{"sales alone", []transactionRecord{sale}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, qualified := evaluateLead("TEST", test.purchases)
			if qualified != test.qualified {
				t.Fatalf("qualified=%t, want %t", qualified, test.qualified)
			}
		})
	}
}

func TestRoleAndRepeatRanking(t *testing.T) {
	first := testPurchase("Executive", 300_000)
	first.Position = "Chief Financial Officer"
	second := first
	second.TransactionDate = "2026-10-21"
	sale := testPurchase("Seller", 900_000)
	sale.TransactionCode = "S"
	lead, qualified := evaluateLead("TEST", []transactionRecord{first, second, sale})
	if !qualified || lead.Score != 50 || lead.BuyerCount != 1 || len(lead.ContextSales) != 1 {
		t.Fatalf("unexpected lead: %+v", lead)
	}
	second.TransactionDate = first.TransactionDate
	sameDayLead, _ := evaluateLead("TEST", []transactionRecord{first, second})
	if sameDayLead.Score != 40 {
		t.Fatalf("same-day rows earned repeat bonus: %+v", sameDayLead)
	}
}

type memoryTransactionReader struct {
	records []transactionRecord
	err     error
	start   string
	end     string
}

func (reader *memoryTransactionReader) ReadTransactions(startDate, endDate string) ([]transactionRecord, error) {
	reader.start, reader.end = startDate, endDate
	return reader.records, reader.err
}

func TestReportWindowAndDeterministicRanking(t *testing.T) {
	first := testPurchase("Buyer", 300_000)
	first.Ticker = "ZZZ"
	second := first
	second.Ticker = "AAA"
	reader := &memoryTransactionReader{records: []transactionRecord{first, second}}
	now := time.Date(2026, time.October, 22, 1, 0, 0, 0, time.UTC)
	report, err := buildLeadReport(reader, now)
	if err != nil {
		t.Fatal(err)
	}
	if reader.start != "2026-10-08" || reader.end != "2026-10-21" {
		t.Fatalf("wrong New York window: %s through %s", reader.start, reader.end)
	}
	if len(report.Leads) != 2 || report.Leads[0].Ticker != "AAA" {
		t.Fatalf("unstable ranking: %+v", report.Leads)
	}
	reader.err = fmt.Errorf("database unavailable")
	if _, err := buildLeadReport(reader, now); err == nil {
		t.Fatal("reader failure was hidden")
	}
}

func ownershipFixture(owner, ownerCIK, code, shares, price, after, nature, plan, documentType string) string {
	return fmt.Sprintf(`<ownershipDocument><documentType>%s</documentType><aff10b5One>%s</aff10b5One>
	<issuer><issuerCik>0000000001</issuerCik><issuerTradingSymbol>TEST</issuerTradingSymbol></issuer>
	<reportingOwner><reportingOwnerId><rptOwnerCik>%s</rptOwnerCik><rptOwnerName>%s</rptOwnerName></reportingOwnerId>
	<reportingOwnerRelationship><isDirector>1</isDirector></reportingOwnerRelationship></reportingOwner>
	<nonDerivativeTable><nonDerivativeTransaction><transactionDate><value>2026-10-20</value></transactionDate>
	<transactionCoding><transactionCode>%s</transactionCode></transactionCoding>
	<transactionAmounts><transactionShares><value>%s</value></transactionShares>
	<transactionPricePerShare><value>%s</value></transactionPricePerShare></transactionAmounts>
	<postTransactionAmounts><sharesOwnedFollowingTransaction><value>%s</value></sharesOwnedFollowingTransaction></postTransactionAmounts>
	<ownershipNature><directOrIndirectOwnership><value>%s</value></directOrIndirectOwnership></ownershipNature>
	</nonDerivativeTransaction></nonDerivativeTable></ownershipDocument>`, documentType, plan, ownerCIK, owner, code, shares, price, after, nature)
}

func TestOwnershipEvidenceParsing(t *testing.T) {
	tests := []struct {
		name          string
		after         string
		nature        string
		knownIncrease bool
		newHolding    bool
	}{
		{"direct holding", "11000", "D", true, false},
		{"new holding", "1000", "D", false, true},
		{"missing holding", "", "D", false, false},
		{"inconsistent holding", "999", "D", false, false},
		{"indirect holding", "11000", "I", false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := ownershipFixture("Buyer", "2", "P", "1000", "100", test.after, test.nature, "1", "4/A")
			parsed, err := parseForm4WithThresholds(fixture, 25_000, 200_000, nil)
			if err != nil || len(parsed["TEST"]) != 1 {
				t.Fatalf("parse failed: %v, %+v", err, parsed)
			}
			entry := parsed["TEST"][0]
			if (entry.holdingIncrease != nil) != test.knownIncrease || entry.newPosition != test.newHolding || !entry.is10b51 || !entry.isAmendment || entry.position != "Director" {
				t.Fatalf("unexpected evidence: %+v", entry)
			}
		})
	}
}

func TestPurchaseAndSaleFloorsAndJointOwners(t *testing.T) {
	buy := ownershipFixture("Buyer", "2", "P", "5000", "10", "55000", "D", "0", "4")
	sale := strings.Replace(buy, "<transactionCode>P", "<transactionCode>S", 1)
	parsedBuy, _ := parseForm4WithThresholds(buy, 25_000, 200_000, nil)
	parsedSale, _ := parseForm4WithThresholds(sale, 25_000, 200_000, nil)
	if len(parsedBuy["TEST"]) != 1 || len(parsedSale["TEST"]) != 0 {
		t.Fatalf("purchase and context floors not independent: %+v, %+v", parsedBuy, parsedSale)
	}
	joint := strings.Replace(buy, "</reportingOwner>", "</reportingOwner><reportingOwner><reportingOwnerId><rptOwnerName>Other Owner</rptOwnerName></reportingOwnerId></reportingOwner>", 1)
	parsedJoint, err := parseForm4WithThresholds(joint, 25_000, 200_000, nil)
	if err != nil || len(parsedJoint["TEST"]) != 1 || !parsedJoint["TEST"][0].jointReporting {
		t.Fatalf("joint filing should be retained once and marked: %+v, %v", parsedJoint, err)
	}
	if _, err := parseForm4WithThresholds("<html>Access Denied</html>", 25_000, 200_000, nil); err == nil {
		t.Fatal("invalid filing was silently accepted")
	}
}

func TestRealSECFilings(t *testing.T) {
	tests := []struct {
		path    string
		ticker  string
		count   int
		planned bool
		joint   bool
	}{
		{"testdata/sec/fcnca-0001193125-26-220236.txt", "FCNCA", 2, false, false},
		{"testdata/sec/aaoi-0001683168-26-003812.xml", "AAOI", 1, true, false},
		{"testdata/sec/jhg-0001193125-26-224632.xml", "JHG", 1, false, true},
	}
	for _, test := range tests {
		t.Run(test.ticker, func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseForm4WithThresholds(string(content), 25_000, 200_000, nil)
			if err != nil || len(parsed[test.ticker]) != test.count {
				t.Fatalf("real filing parse failed: %+v, %v", parsed, err)
			}
			for _, entry := range parsed[test.ticker] {
				if entry.is10b51 != test.planned || entry.jointReporting != test.joint || entry.ownerKey == "" {
					t.Fatalf("real filing evidence wrong: %+v", entry)
				}
			}
		})
	}
}

func TestOwnerCIKPreventsFalseClustersFromNameChanges(t *testing.T) {
	first := testPurchase("Buyer Full Name", 60_000)
	first.OwnerKey = "0000012345"
	second := testPurchase("Buyer F. Name", 60_000)
	second.OwnerKey = "12345"
	lead, qualified := evaluateLead("TEST", []transactionRecord{first, second})
	if qualified || lead.BuyerCount != 1 {
		t.Fatalf("same CIK counted as separate insiders: %+v", lead)
	}
}

func TestDirectorRankUsesBoardRole(t *testing.T) {
	owner := reportingOwner{ReportingOwnerRelationship: reportingOwnerRelationship{
		IsDirector: "true", IsOfficer: "true", OfficerTitle: "Chief Executive Officer",
	}}
	position := extractPosition(owner)
	if !hasDirectorPosition(position) || !strings.Contains(position, "Chief Executive Officer") {
		t.Fatalf("dual role lost: %s", position)
	}
	if hasDirectorPosition("Director of Engineering") {
		t.Fatal("job title was mistaken for board membership")
	}
}
