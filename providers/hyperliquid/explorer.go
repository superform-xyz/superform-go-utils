package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

const MainnetL1ExplorerURL = "https://rpc.hyperliquid.xyz/explorer"

// L1Action is a typed action returned by the Hyperliquid L1 Explorer API.
type L1Action interface {
	L1ActionType() string
}

// L1USDClassTransferAction is the L1 execution record for CoreWriter action 7.
type L1USDClassTransferAction struct {
	Type   string `json:"type"`
	Ntl    uint64 `json:"ntl"`
	ToPerp bool   `json:"toPerp"`
}

// L1ActionType returns the explorer action discriminator.
func (a L1USDClassTransferAction) L1ActionType() string { return a.Type }

// L1ApproveAgentAction is the L1 execution record for CoreWriter action 9.
type L1ApproveAgentAction struct {
	Type             string         `json:"type"`
	SignatureChainID string         `json:"signatureChainId"`
	HyperliquidChain string         `json:"hyperliquidChain"`
	AgentAddress     common.Address `json:"agentAddress"`
	AgentName        string         `json:"agentName"`
	Nonce            uint64         `json:"nonce"`
}

// L1ActionType returns the explorer action discriminator.
func (a L1ApproveAgentAction) L1ActionType() string { return a.Type }

// L1ApproveBuilderFeeAction is the L1 execution record for CoreWriter action
// 12.
type L1ApproveBuilderFeeAction struct {
	Type       string         `json:"type"`
	MaxFeeRate uint64         `json:"maxFeeRate"`
	Builder    common.Address `json:"builder"`
}

// L1ActionType returns the explorer action discriminator.
func (a L1ApproveBuilderFeeAction) L1ActionType() string { return a.Type }

// L1SendAssetAction is the L1 execution record for CoreWriter action 13. Nil
// optional fields are how the explorer represents the zero/sentinel values in
// the CoreWriter payload.
type L1SendAssetAction struct {
	Type                 string          `json:"type"`
	Destination          common.Address  `json:"destination"`
	FromSubAccount       *common.Address `json:"fromSubAccount"`
	SourceDexOrSpot      *uint32         `json:"sourceDexOrSpot"`
	DestinationDexOrSpot *uint32         `json:"destinationDexOrSpot"`
	Token                uint64          `json:"token"`
	Wei                  uint64          `json:"wei"`
}

// L1ActionType returns the explorer action discriminator.
func (a L1SendAssetAction) L1ActionType() string { return a.Type }

// UnknownL1Action preserves an explorer action not known by this package.
type UnknownL1Action struct {
	Type string
	Raw  json.RawMessage
}

// L1ActionType returns the unknown explorer action discriminator.
func (a UnknownL1Action) L1ActionType() string { return a.Type }

const (
	l1ActionUSDClassTransfer  = "SystemUsdClassTransferAction"
	l1ActionApproveAgent      = "approveAgent"
	l1ActionApproveBuilderFee = "SystemApproveBuilderFeeAction"
	l1ActionSendAsset         = "SystemSendAssetAction"
)

// L1Transaction is a typed L1 transaction returned by userDetails or
// txDetails. A non-nil Error proves rejection; some delayed actions also emit
// a nil-error enqueue record before their applied outcome.
type L1Transaction struct {
	Time   int64
	User   common.Address
	Action L1Action
	Block  uint64
	Hash   common.Hash
	Error  *string
}

// UnmarshalJSON decodes known system action variants and retains unknown ones.
func (t *L1Transaction) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("cannot unmarshal an L1 transaction into a nil receiver")
	}
	var wire struct {
		Time   *int64          `json:"time"`
		User   *common.Address `json:"user"`
		Action json.RawMessage `json:"action"`
		Block  *uint64         `json:"block"`
		Hash   *common.Hash    `json:"hash"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("decoding L1 transaction: %w", err)
	}
	if wire.Time == nil {
		return fmt.Errorf("L1 transaction has no time")
	}
	if *wire.Time < 0 {
		return fmt.Errorf("L1 transaction time must be non-negative, got %d", *wire.Time)
	}
	if wire.User == nil {
		return fmt.Errorf("L1 transaction has no user")
	}
	if wire.Block == nil {
		return fmt.Errorf("L1 transaction has no block")
	}
	if wire.Hash == nil || *wire.Hash == (common.Hash{}) {
		return fmt.Errorf("L1 transaction has no hash")
	}
	if len(wire.Action) == 0 || bytes.Equal(bytes.TrimSpace(wire.Action), []byte("null")) {
		return fmt.Errorf("L1 transaction %s has no action", *wire.Hash)
	}
	if len(wire.Error) == 0 {
		return fmt.Errorf("L1 transaction %s has no error result", *wire.Hash)
	}

	action, err := decodeL1Action(wire.Action)
	if err != nil {
		return fmt.Errorf("decoding L1 transaction %s action: %w", *wire.Hash, err)
	}
	var executionError *string
	if !bytes.Equal(bytes.TrimSpace(wire.Error), []byte("null")) {
		var message string
		if err := json.Unmarshal(wire.Error, &message); err != nil {
			return fmt.Errorf("decoding L1 transaction %s error result: %w", *wire.Hash, err)
		}
		executionError = &message
	}
	t.Time = *wire.Time
	t.User = *wire.User
	t.Action = action
	t.Block = *wire.Block
	t.Hash = *wire.Hash
	t.Error = executionError
	return nil
}

func decodeL1Action(data json.RawMessage) (L1Action, error) {
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return nil, fmt.Errorf("decoding L1 action type: %w", err)
	}
	if discriminator.Type == "" {
		return nil, fmt.Errorf("L1 action type is empty")
	}

	var target L1Action
	switch discriminator.Type {
	case l1ActionUSDClassTransfer:
		if err := requireNonNullJSONObjectFields(data, "ntl", "toPerp"); err != nil {
			return nil, err
		}
		target = new(L1USDClassTransferAction)
	case l1ActionApproveAgent:
		if err := requireNonNullJSONObjectFields(data, "signatureChainId", "hyperliquidChain", "agentAddress", "agentName", "nonce"); err != nil {
			return nil, err
		}
		target = new(L1ApproveAgentAction)
	case l1ActionApproveBuilderFee:
		if err := requireNonNullJSONObjectFields(data, "maxFeeRate", "builder"); err != nil {
			return nil, err
		}
		target = new(L1ApproveBuilderFeeAction)
	case l1ActionSendAsset:
		if err := requireNonNullJSONObjectFields(data, "destination", "token", "wei"); err != nil {
			return nil, err
		}
		if err := requireJSONObjectFields(data, "fromSubAccount", "sourceDexOrSpot", "destinationDexOrSpot"); err != nil {
			return nil, err
		}
		target = new(L1SendAssetAction)
	default:
		return UnknownL1Action{Type: discriminator.Type, Raw: bytes.Clone(data)}, nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("decoding %s L1 action: %w", discriminator.Type, err)
	}
	return target, nil
}

// L1UserDetails is the typed userDetails response.
type L1UserDetails struct {
	Transactions []L1Transaction
}

// L1TxDetails is the typed txDetails response.
type L1TxDetails struct {
	Transaction L1Transaction
}

// L1BlockDetails is a complete, internally consistent L1 Explorer block. Its
// transactions retain explorer order so callers can disambiguate repeated
// CoreWriter actions within one L1 block.
type L1BlockDetails struct {
	BlockTime       int64
	Hash            common.Hash
	Height          uint64
	NumTransactions uint64
	Transactions    []L1Transaction
}

// UnmarshalJSON rejects partial block snapshots. A partial transaction list
// must never be used to uniquely attribute a CoreWriter action.
func (b *L1BlockDetails) UnmarshalJSON(data []byte) error {
	if b == nil {
		return fmt.Errorf("cannot unmarshal L1 block details into a nil receiver")
	}
	var wire struct {
		BlockTime *int64          `json:"blockTime"`
		Hash      *common.Hash    `json:"hash"`
		Height    *uint64         `json:"height"`
		NumTxs    *uint64         `json:"numTxs"`
		Txs       json.RawMessage `json:"txs"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("decoding L1 block details: %w", err)
	}
	if wire.BlockTime == nil || *wire.BlockTime < 0 {
		return fmt.Errorf("L1 block has invalid blockTime")
	}
	if wire.Hash == nil || *wire.Hash == (common.Hash{}) {
		return fmt.Errorf("L1 block has no hash")
	}
	if wire.Height == nil {
		return fmt.Errorf("L1 block has no height")
	}
	if wire.NumTxs == nil {
		return fmt.Errorf("L1 block %d has no numTxs", *wire.Height)
	}
	if len(wire.Txs) == 0 || bytes.Equal(bytes.TrimSpace(wire.Txs), []byte("null")) {
		return fmt.Errorf("L1 block %d has no transaction list", *wire.Height)
	}

	var transactions []L1Transaction
	if err := json.Unmarshal(wire.Txs, &transactions); err != nil {
		return fmt.Errorf("decoding L1 block %d transactions: %w", *wire.Height, err)
	}
	candidate := L1BlockDetails{
		BlockTime:       *wire.BlockTime,
		Hash:            *wire.Hash,
		Height:          *wire.Height,
		NumTransactions: *wire.NumTxs,
		Transactions:    transactions,
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*b = candidate
	return nil
}

// Validate checks that block details are complete and internally consistent.
// Callers should also use it for blocks constructed without UnmarshalJSON.
func (b *L1BlockDetails) Validate() error {
	if b == nil {
		return fmt.Errorf("L1 block details are nil")
	}
	if b.BlockTime < 0 {
		return fmt.Errorf("L1 block has invalid blockTime")
	}
	if b.Hash == (common.Hash{}) {
		return fmt.Errorf("L1 block has no hash")
	}
	if b.Transactions == nil {
		return fmt.Errorf("L1 block %d has no transaction list", b.Height)
	}
	if uint64(len(b.Transactions)) != b.NumTransactions {
		return fmt.Errorf("L1 block %d contains %d transactions; numTxs is %d", b.Height, len(b.Transactions), b.NumTransactions)
	}
	seen := make(map[common.Hash]struct{}, len(b.Transactions))
	for index, transaction := range b.Transactions {
		if transaction.Hash == (common.Hash{}) {
			return fmt.Errorf("L1 block %d transaction %d has no hash", b.Height, index)
		}
		if transaction.Block != b.Height {
			return fmt.Errorf("L1 block %d transaction %d reports block %d", b.Height, index, transaction.Block)
		}
		if transaction.Time != b.BlockTime {
			return fmt.Errorf("L1 block %d transaction %d reports time %d; blockTime is %d", b.Height, index, transaction.Time, b.BlockTime)
		}
		if _, ok := seen[transaction.Hash]; ok {
			return fmt.Errorf("L1 block %d repeats transaction %s", b.Height, transaction.Hash)
		}
		seen[transaction.Hash] = struct{}{}
	}
	return nil
}

// L1ExplorerError is an error envelope returned with HTTP status 200 by the L1
// Explorer API.
type L1ExplorerError struct {
	Message string
}

func (e *L1ExplorerError) Error() string {
	return "Hyperliquid L1 Explorer error: " + e.Message
}

// L1ExplorerClient queries the Hyperliquid L1 Explorer API.
type L1ExplorerClient struct {
	transport *InfoClient
}

// NewL1ExplorerClient creates a bounded, single-attempt L1 Explorer client.
// Callers inject transport timeouts and instrumentation through HTTPDoer.
func NewL1ExplorerClient(endpoint string, client HTTPDoer, opts ...InfoOption) *L1ExplorerClient {
	if endpoint == "" {
		endpoint = MainnetL1ExplorerURL
	}
	return &L1ExplorerClient{
		transport: NewInfoClient(endpoint, client, opts...),
	}
}

// UserDetails returns recent L1 transactions attributed to user.
func (c *L1ExplorerClient) UserDetails(ctx context.Context, user common.Address) (*L1UserDetails, error) {
	request := struct {
		Type string `json:"type"`
		User string `json:"user"`
	}{
		Type: "userDetails",
		User: strings.ToLower(user.Hex()),
	}
	var response struct {
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Txs     []L1Transaction `json:"txs"`
	}
	if err := c.post(ctx, request.Type, request, &response); err != nil {
		return nil, fmt.Errorf("querying L1 Explorer user details for %s: %w", user, err)
	}
	if err := validateL1ExplorerResponseType(response.Type, request.Type, response.Message); err != nil {
		return nil, err
	}
	for i := range response.Txs {
		if response.Txs[i].User != user {
			return nil, fmt.Errorf("L1 Explorer userDetails transaction %s belongs to %s, want %s", response.Txs[i].Hash, response.Txs[i].User, user)
		}
	}
	return &L1UserDetails{Transactions: response.Txs}, nil
}

// TxDetails returns the L1 transaction identified by hash.
func (c *L1ExplorerClient) TxDetails(ctx context.Context, hash common.Hash) (*L1TxDetails, error) {
	request := struct {
		Type string `json:"type"`
		Hash string `json:"hash"`
	}{
		Type: "txDetails",
		Hash: hash.Hex(),
	}
	var response struct {
		Type    string         `json:"type"`
		Message string         `json:"message"`
		Tx      *L1Transaction `json:"tx"`
	}
	if err := c.post(ctx, request.Type, request, &response); err != nil {
		return nil, fmt.Errorf("querying L1 Explorer transaction %s: %w", hash, err)
	}
	if err := validateL1ExplorerResponseType(response.Type, request.Type, response.Message); err != nil {
		return nil, err
	}
	if response.Tx == nil {
		return nil, fmt.Errorf("L1 Explorer txDetails response for %s has no transaction", hash)
	}
	if response.Tx.Hash != hash {
		return nil, fmt.Errorf("L1 Explorer returned transaction %s for requested hash %s", response.Tx.Hash, hash)
	}
	return &L1TxDetails{Transaction: *response.Tx}, nil
}

// BlockDetails returns a complete L1 block identified by height. The response
// is rejected if the explorer returns a partial or internally inconsistent
// transaction list.
func (c *L1ExplorerClient) BlockDetails(ctx context.Context, height uint64) (*L1BlockDetails, error) {
	request := struct {
		Type   string `json:"type"`
		Height uint64 `json:"height"`
	}{
		Type:   "blockDetails",
		Height: height,
	}
	var response struct {
		Type         string          `json:"type"`
		Message      string          `json:"message"`
		BlockDetails *L1BlockDetails `json:"blockDetails"`
	}
	if err := c.post(ctx, request.Type, request, &response); err != nil {
		return nil, fmt.Errorf("querying L1 Explorer block %d: %w", height, err)
	}
	if err := validateL1ExplorerResponseType(response.Type, request.Type, response.Message); err != nil {
		return nil, err
	}
	if response.BlockDetails == nil {
		return nil, fmt.Errorf("L1 Explorer blockDetails response for %d has no block", height)
	}
	if response.BlockDetails.Height != height {
		return nil, fmt.Errorf("L1 Explorer returned block %d for requested height %d", response.BlockDetails.Height, height)
	}
	return response.BlockDetails, nil
}

func validateL1ExplorerResponseType(got string, want string, message string) error {
	if got == "error" {
		if message == "" {
			message = "unknown error"
		}
		return &L1ExplorerError{Message: message}
	}
	if got != want {
		return fmt.Errorf("L1 Explorer response type is %q; want %q", got, want)
	}
	return nil
}

func (c *L1ExplorerClient) post(ctx context.Context, requestType string, request any, out any) error {
	if c == nil || c.transport == nil {
		return fmt.Errorf("posting %s request: L1 Explorer client is not configured", requestType)
	}
	if _, err := c.transport.Post(ctx, request, out); err != nil {
		var requestErr *RequestError
		if errors.As(err, &requestErr) && requestErr.Kind == ErrorDecode {
			return fmt.Errorf("decoding %s response: %w", requestType, err)
		}
		return fmt.Errorf("posting %s request: %w", requestType, err)
	}
	return nil
}
