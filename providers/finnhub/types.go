package finnhub

import "encoding/json"

type CompanyProfile struct {
	Country              string   `json:"country"`
	Currency             string   `json:"currency"`
	Exchange             string   `json:"exchange"`
	FinnhubIndustry      string   `json:"finnhubIndustry"`
	IPO                  string   `json:"ipo"`
	Logo                 string   `json:"logo"`
	MarketCapitalization *float64 `json:"marketCapitalization"`
	Name                 string   `json:"name"`
	Phone                string   `json:"phone"`
	ShareOutstanding     *float64 `json:"shareOutstanding"`
	Ticker               string   `json:"ticker"`
	WebURL               string   `json:"weburl"`
}

type BasicFinancialsRequest struct {
	Symbol string
	Metric string
}

type BasicFinancials struct {
	Symbol     string                             `json:"symbol"`
	MetricType string                             `json:"metricType"`
	Metric     map[string]json.RawMessage         `json:"metric"`
	Series     map[string]map[string]MetricSeries `json:"series,omitempty"`
}

func (b BasicFinancials) MetricValue(name string) (float64, bool) {
	raw, ok := b.Metric[name]
	if !ok || len(raw) == 0 {
		return 0, false
	}
	var value *float64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return 0, false
	}
	return *value, true
}

func (b BasicFinancials) MetricString(name string) (string, bool) {
	raw, ok := b.Metric[name]
	if !ok || len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", false
	}
	return value, true
}

type MetricSeries []MetricPoint

type MetricPoint struct {
	Period string   `json:"period"`
	Value  *float64 `json:"v"`
}

type StockFundamentals struct {
	Symbol               string
	Name                 string
	Currency             string
	Exchange             string
	FinnhubIndustry      string
	MarketCapitalization *float64
	ShareOutstanding     *float64
	PERatio              *float64
	PERatioMetric        string
	BasicFinancials      BasicFinancials
	CompanyProfile       CompanyProfile
}
