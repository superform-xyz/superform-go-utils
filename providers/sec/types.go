package sec

import (
	"context"
	"errors"
	"fmt"
)

var ErrTickerNotFound = errors.New("sec ticker not found")

// Client defines the SEC EDGAR API surface shared across backend services.
type Client interface {
	GetCompanyTickers(ctx context.Context) ([]CompanyTicker, error)
	ResolveCIK(ctx context.Context, ticker string) (uint64, error)
	GetCompanyFacts(ctx context.Context, cik uint64) (*CompanyFacts, error)
	GetEquityFacts(ctx context.Context, ticker string) (*EquityFacts, error)
	Close() error
}

// CompanyTicker maps an exchange ticker to its SEC Central Index Key.
type CompanyTicker struct {
	CIK    uint64 `json:"cik_str"`
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
}

// CompanyFacts is the SEC XBRL Company Facts response.
type CompanyFacts struct {
	CIK        uint64                            `json:"cik"`
	EntityName string                            `json:"entityName"`
	Facts      map[string]map[string]CompanyFact `json:"facts"`
}

// CompanyFact contains one XBRL fact and its values grouped by unit.
type CompanyFact struct {
	Label       string                 `json:"label"`
	Description string                 `json:"description"`
	Units       map[string][]FactValue `json:"units"`
}

// FactValue is one filed XBRL fact value.
type FactValue struct {
	Start        string  `json:"start,omitempty"`
	End          string  `json:"end"`
	Value        float64 `json:"val"`
	Accession    string  `json:"accn"`
	FiscalYear   *int    `json:"fy,omitempty"`
	FiscalPeriod string  `json:"fp,omitempty"`
	Form         string  `json:"form"`
	Filed        string  `json:"filed"`
	Frame        string  `json:"frame,omitempty"`
}

// EquityFacts contains the SEC facts used for equity supply calculations.
type EquityFacts struct {
	CIK                          uint64
	Ticker                       string
	EntityName                   string
	SharesOutstanding            *FactValue
	DilutedWeightedAverageShares *FactValue
	PublicFloatUSD               *FactValue
}

// HTTPError describes a non-success response from SEC EDGAR.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("sec status %d", e.StatusCode)
	}
	return fmt.Sprintf("sec status %d: %s", e.StatusCode, e.Body)
}
