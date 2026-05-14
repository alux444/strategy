package main

import (
	"encoding/xml"
	"strings"
)

type AlertEntry struct {
	ownerName        string
	position         string
	typeLabel        string
	shares           int64
	price            float64
	amount           float64
	is10b51          bool
	transactionDate  string
	sharesOwnedAfter int64
}

type MasterIndex struct {
	indexDate string
	content   string
}

type settings struct {
	Tickers      []string `json:"tickers"`
	ThresholdUSD *int64   `json:"thresholdUsd"`
	LookbackDays *int     `json:"lookbackDays"`
	Debug        *bool    `json:"debug"`
	SettingsFile string   `json:"-"`
}

type flexibleText string

func (f *flexibleText) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var data struct {
		Text  string `xml:",chardata"`
		Value string `xml:"value"`
	}
	if err := d.DecodeElement(&data, &start); err != nil {
		return err
	}
	value := strings.TrimSpace(data.Value)
	if value == "" {
		value = strings.TrimSpace(data.Text)
	}
	*f = flexibleText(value)
	return nil
}

func (f flexibleText) String() string {
	return string(f)
}

// Ownership document structures
type ownershipDocument struct {
	XMLName        xml.Name       `xml:"ownershipDocument"`
	Issuer         issuer         `xml:"issuer"`
	ReportingOwner reportingOwner `xml:"reportingOwner"`
	NonDerivTable  nonDerivTable  `xml:"nonDerivativeTable"`
}

type issuer struct {
	IssuerCIK           flexibleText `xml:"issuerCik"`
	IssuerCIKAlt        flexibleText `xml:"issuerCIK"`
	IssuerTradingSymbol string       `xml:"issuerTradingSymbol"`
}

type reportingOwner struct {
	ReportingOwnerID           reportingOwnerID           `xml:"reportingOwnerId"`
	ReportingOwnerRelationship reportingOwnerRelationship `xml:"reportingOwnerRelationship"`
}

type reportingOwnerID struct {
	RptOwnerName  string `xml:"rptOwnerName"`
	RptOwnerTitle string `xml:"rptOwnerTitle"`
}

type reportingOwnerRelationship struct {
	IsDirector    string `xml:"isDirector"`
	IsOfficer     string `xml:"isOfficer"`
	OfficerTitle  string `xml:"officerTitle"`
	DirectorTitle string `xml:"directorTitle"`
	OtherTitle    string `xml:"otherTitle"`
}

type nonDerivTable struct {
	Transactions []nonDerivativeTransaction `xml:"nonDerivativeTransaction"`
}

type nonDerivativeTransaction struct {
	TransactionCoding       transactionCoding      `xml:"transactionCoding"`
	TransactionAmounts      transactionAmounts     `xml:"transactionAmounts"`
	TransactionDate         flexibleText           `xml:"transactionDate"`
	SecurityTitle           flexibleText           `xml:"securityTitle"`
	PostTransactionAmounts  postTransactionAmounts `xml:"postTransactionAmounts"`
	SharesOwnedFollowingTxn flexibleText           `xml:"sharesOwnedFollowingTransaction"`
}

type transactionCoding struct {
	TransactionCode    string `xml:"transactionCode"`
	Is10b51Transaction string `xml:"is10b51Transaction"`
}

type transactionAmounts struct {
	TransactionShares        flexibleText `xml:"transactionShares"`
	TransactionPricePerShare flexibleText `xml:"transactionPricePerShare"`
}

type postTransactionAmounts struct {
	SharesOwnedFollowingTransaction flexibleText `xml:"sharesOwnedFollowingTransaction"`
}
