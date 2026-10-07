package rhinestone

import "encoding/json"

type GetOnrampOptionsRequest struct {
	Provider string
	Kind     string
	// Country is forwarded as x-user-country when set. It is an identity
	// assertion: callers must establish trust before supplying this value.
	Country string
}

// GetOnrampOptionsResponse preserves absent/null fields and explicit empty
// lists so the caller can distinguish unavailable evidence from no support.
type GetOnrampOptionsResponse struct {
	Provider string         `json:"provider"`
	Kind     string         `json:"kind"`
	Country  *string        `json:"country"`
	Methods  []OnrampMethod `json:"methods"`
}

type OnrampMethod struct {
	Method     string   `json:"method"`
	Currencies []string `json:"currencies"`
	// Swapped's fiat limits are EUR-denominated regardless of payment currency.
	// Pointers preserve unknown limits versus zero; json.Number retains decimals.
	MinAmount *json.Number `json:"minAmount"`
	MaxAmount *json.Number `json:"maxAmount"`
}

type Chain struct {
	Destination bool `json:"destination"`
	// SupportedTokens can be "all" or a list of Token objects. Interpretation
	// and output-asset restrictions belong to the consumer.
	SupportedTokens json.RawMessage `json:"supportedTokens"`
}

type Token struct {
	Address string `json:"address"`
}
