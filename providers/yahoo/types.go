package yahoo

import "time"

type ChartRequest struct {
	Symbol         string
	Range          string
	Interval       string
	Start          time.Time
	End            time.Time
	IncludePrePost bool
	Events         string
}

type PriceHistoryRequest struct {
	Symbol         string
	Range          string
	Granularity    string
	Start          time.Time
	End            time.Time
	IncludePrePost bool
	Events         string
}

type StockDetail struct {
	Symbol           string
	ShortName        string
	LongName         string
	Currency         string
	Price            *float64
	MarketTime       *time.Time
	MarketVolume     *int64
	Open             *float64
	FiftyTwoWeekHigh *float64
	FiftyTwoWeekLow  *float64
}

type PriceBar struct {
	Symbol    string
	Interval  string
	Timestamp time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	AdjClose  *float64
	Volume    int64
}

type YahooError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

func (e YahooError) Error() string {
	if e.Code == "" {
		return e.Description
	}
	if e.Description == "" {
		return e.Code
	}
	return e.Code + ": " + e.Description
}

type ChartResponse struct {
	Chart ChartPayload `json:"chart"`
}

type ChartPayload struct {
	Result []ChartResult `json:"result"`
	Error  *YahooError   `json:"error"`
}

type ChartResult struct {
	Meta       ChartMeta       `json:"meta"`
	Timestamp  []int64         `json:"timestamp"`
	Indicators ChartIndicators `json:"indicators"`
}

type ChartMeta struct {
	Currency             string        `json:"currency"`
	Symbol               string        `json:"symbol"`
	ExchangeName         string        `json:"exchangeName"`
	FullExchangeName     string        `json:"fullExchangeName"`
	InstrumentType       string        `json:"instrumentType"`
	FirstTradeDate       int64         `json:"firstTradeDate"`
	RegularMarketTime    int64         `json:"regularMarketTime"`
	GMTOffset            int64         `json:"gmtoffset"`
	Timezone             string        `json:"timezone"`
	ExchangeTimezoneName string        `json:"exchangeTimezoneName"`
	RegularMarketPrice   *float64      `json:"regularMarketPrice"`
	FiftyTwoWeekHigh     *float64      `json:"fiftyTwoWeekHigh"`
	FiftyTwoWeekLow      *float64      `json:"fiftyTwoWeekLow"`
	RegularMarketDayHigh *float64      `json:"regularMarketDayHigh"`
	RegularMarketDayLow  *float64      `json:"regularMarketDayLow"`
	RegularMarketVolume  *int64        `json:"regularMarketVolume"`
	LongName             string        `json:"longName"`
	ShortName            string        `json:"shortName"`
	ChartPreviousClose   *float64      `json:"chartPreviousClose"`
	PreviousClose        *float64      `json:"previousClose"`
	CurrentTradingPeriod TradingPeriod `json:"currentTradingPeriod"`
}

type TradingPeriod struct {
	Pre     TradingPeriodWindow `json:"pre"`
	Regular TradingPeriodWindow `json:"regular"`
	Post    TradingPeriodWindow `json:"post"`
}

type TradingPeriodWindow struct {
	Timezone  string `json:"timezone"`
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	GMTOffset int64  `json:"gmtoffset"`
}

type ChartIndicators struct {
	Quote    []ChartQuote    `json:"quote"`
	AdjClose []ChartAdjClose `json:"adjclose"`
}

type ChartQuote struct {
	Open   []*float64 `json:"open"`
	High   []*float64 `json:"high"`
	Low    []*float64 `json:"low"`
	Close  []*float64 `json:"close"`
	Volume []*int64   `json:"volume"`
}

type ChartAdjClose struct {
	AdjClose []*float64 `json:"adjclose"`
}
