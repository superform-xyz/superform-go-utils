package hyperliquid

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Decimal is an exact decimal value returned by a HyperCore JSON API. The APIs
// have returned both JSON strings and numbers for these fields over time, so it
// accepts both representations and retains their decimal text without a
// floating-point conversion.
type Decimal string

// String returns the API's decimal representation.
func (d Decimal) String() string { return string(d) }

// Rat returns the decimal as an exact rational number.
func (d Decimal) Rat() (*big.Rat, error) {
	return ParseAmount(string(d))
}

// UnmarshalJSON accepts a decimal JSON string or number.
func (d *Decimal) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("cannot unmarshal a HyperCore decimal into a nil receiver")
	}

	value, err := decodeDecimalText(data)
	if err != nil {
		return err
	}
	if _, err := ParseAmount(value); err != nil {
		return err
	}
	*d = Decimal(value)
	return nil
}

// decodeDecimalText retains strings verbatim and numbers without a floating
// point conversion. Semantic amount validation belongs to Decimal.Rat or the
// typed decoder, not the compatibility wire view.
func decodeDecimalText(data []byte) (string, error) {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return "", fmt.Errorf("decoding quoted HyperCore decimal: %w", err)
		}
		return value, nil
	}
	if len(data) == 0 || (data[0] != '-' && (data[0] < '0' || data[0] > '9')) || !json.Valid(data) {
		return "", fmt.Errorf("decoding numeric HyperCore decimal: expected one JSON number")
	}
	return string(data), nil
}

type decimalWireText string

func (d *decimalWireText) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*d = ""
		return nil
	}
	text, err := decodeDecimalText(data)
	if err != nil {
		return err
	}
	*d = decimalWireText(text)
	return nil
}

// LedgerDeltaType identifies a non-funding ledger update variant.
type LedgerDeltaType string

// Ledger delta type values returned by the non-funding ledger API.
const (
	LedgerDeltaDeposit               LedgerDeltaType = "deposit"
	LedgerDeltaWithdraw              LedgerDeltaType = "withdraw"
	LedgerDeltaInternalTransfer      LedgerDeltaType = "internalTransfer"
	LedgerDeltaSubAccountTransfer    LedgerDeltaType = "subAccountTransfer"
	LedgerDeltaLiquidation           LedgerDeltaType = "liquidation"
	LedgerDeltaVaultCreate           LedgerDeltaType = "vaultCreate"
	LedgerDeltaVaultDeposit          LedgerDeltaType = "vaultDeposit"
	LedgerDeltaVaultDistribution     LedgerDeltaType = "vaultDistribution"
	LedgerDeltaVaultWithdraw         LedgerDeltaType = "vaultWithdraw"
	LedgerDeltaVaultLeaderCommission LedgerDeltaType = "vaultLeaderCommission"
	LedgerDeltaSpotTransfer          LedgerDeltaType = "spotTransfer"
	LedgerDeltaAccountClassTransfer  LedgerDeltaType = "accountClassTransfer"
	LedgerDeltaSpotGenesis           LedgerDeltaType = "spotGenesis"
	LedgerDeltaRewardsClaim          LedgerDeltaType = "rewardsClaim"
	// LedgerDeltaSend is the current mainnet representation of a spot send,
	// including CoreWriter action 13 and HyperEVM-to-HyperCore gateway credits.
	LedgerDeltaSend                 LedgerDeltaType = "send"
	LedgerDeltaAccountActivationGas LedgerDeltaType = "accountActivationGas"
	LedgerDeltaCStakingTransfer     LedgerDeltaType = "cStakingTransfer"
)

// NonFundingLedgerDelta is a typed non-funding ledger update payload.
type NonFundingLedgerDelta interface {
	ledgerDeltaType() LedgerDeltaType
}

// LedgerDeltaKind returns the API discriminator for a typed ledger delta.
func LedgerDeltaKind(delta NonFundingLedgerDelta) LedgerDeltaType {
	if delta == nil {
		return ""
	}
	return delta.ledgerDeltaType()
}

// DepositLedgerDelta records a USDC deposit.
type DepositLedgerDelta struct {
	Type LedgerDeltaType `json:"type"`
	USDC Decimal         `json:"usdc"`
}

func (d DepositLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// WithdrawLedgerDelta records a USDC withdrawal.
type WithdrawLedgerDelta struct {
	Type  LedgerDeltaType `json:"type"`
	USDC  Decimal         `json:"usdc"`
	Nonce uint64          `json:"nonce"`
	Fee   Decimal         `json:"fee"`
}

func (d WithdrawLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// InternalTransferLedgerDelta records an internal USDC transfer.
type InternalTransferLedgerDelta struct {
	Type        LedgerDeltaType `json:"type"`
	USDC        Decimal         `json:"usdc"`
	User        common.Address  `json:"user"`
	Destination common.Address  `json:"destination"`
	Fee         Decimal         `json:"fee"`
}

func (d InternalTransferLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// SubAccountTransferLedgerDelta records a master/subaccount USDC transfer.
type SubAccountTransferLedgerDelta struct {
	Type        LedgerDeltaType `json:"type"`
	USDC        Decimal         `json:"usdc"`
	User        common.Address  `json:"user"`
	Destination common.Address  `json:"destination"`
}

func (d SubAccountTransferLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// LiquidatedPosition is one position in a liquidation ledger update.
type LiquidatedPosition struct {
	Coin string  `json:"coin"`
	Szi  Decimal `json:"szi"`
}

// LiquidationLedgerDelta records a liquidation.
type LiquidationLedgerDelta struct {
	Type                LedgerDeltaType      `json:"type"`
	AccountValue        Decimal              `json:"accountValue"`
	LeverageType        string               `json:"leverageType"`
	LiquidatedPositions []LiquidatedPosition `json:"liquidatedPositions"`
}

func (d LiquidationLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// VaultLedgerDelta records vault creation, deposit, or distribution.
type VaultLedgerDelta struct {
	Type  LedgerDeltaType `json:"type"`
	Vault common.Address  `json:"vault"`
	USDC  Decimal         `json:"usdc"`
}

func (d VaultLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// VaultWithdrawalLedgerDelta records a vault withdrawal.
type VaultWithdrawalLedgerDelta struct {
	Type            LedgerDeltaType `json:"type"`
	Vault           common.Address  `json:"vault"`
	User            common.Address  `json:"user"`
	RequestedUSD    Decimal         `json:"requestedUsd"`
	Commission      Decimal         `json:"commission"`
	ClosingCost     Decimal         `json:"closingCost"`
	Basis           Decimal         `json:"basis"`
	NetWithdrawnUSD Decimal         `json:"netWithdrawnUsd"`
}

func (d VaultWithdrawalLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// VaultLeaderCommissionLedgerDelta records commission paid by a vault.
type VaultLeaderCommissionLedgerDelta struct {
	Type LedgerDeltaType `json:"type"`
	User common.Address  `json:"user"`
	USDC Decimal         `json:"usdc"`
}

func (d VaultLeaderCommissionLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// SpotTransferLedgerDelta is the documented legacy spot transfer shape.
type SpotTransferLedgerDelta struct {
	Type        LedgerDeltaType `json:"type"`
	Token       string          `json:"token"`
	Amount      Decimal         `json:"amount"`
	USDCValue   Decimal         `json:"usdcValue"`
	User        common.Address  `json:"user"`
	Destination common.Address  `json:"destination"`
	Fee         Decimal         `json:"fee"`
}

func (d SpotTransferLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// SendLedgerDelta is the observed mainnet spot send shape. SourceDex and
// DestinationDex are "spot", a HIP-3 dex name, or empty for an account side.
type SendLedgerDelta struct {
	Type           LedgerDeltaType `json:"type"`
	User           common.Address  `json:"user"`
	Destination    common.Address  `json:"destination"`
	SourceDex      string          `json:"sourceDex"`
	DestinationDex string          `json:"destinationDex"`
	Token          string          `json:"token"`
	Amount         Decimal         `json:"amount"`
	USDCValue      Decimal         `json:"usdcValue"`
	Fee            Decimal         `json:"fee"`
	NativeTokenFee Decimal         `json:"nativeTokenFee"`
	Nonce          uint64          `json:"nonce"`
	FeeToken       string          `json:"feeToken"`
}

func (d SendLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// AccountClassTransferLedgerDelta records a spot/perpetual USDC transfer.
type AccountClassTransferLedgerDelta struct {
	Type   LedgerDeltaType `json:"type"`
	USDC   Decimal         `json:"usdc"`
	ToPerp bool            `json:"toPerp"`
}

func (d AccountClassTransferLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// SpotGenesisLedgerDelta records a genesis spot allocation.
type SpotGenesisLedgerDelta struct {
	Type   LedgerDeltaType `json:"type"`
	Token  string          `json:"token"`
	Amount Decimal         `json:"amount"`
}

func (d SpotGenesisLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// RewardsClaimLedgerDelta records a rewards claim.
type RewardsClaimLedgerDelta struct {
	Type   LedgerDeltaType `json:"type"`
	Amount Decimal         `json:"amount"`
}

func (d RewardsClaimLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// UnknownLedgerDelta retains an update variant not known by this package.
type UnknownLedgerDelta struct {
	Type LedgerDeltaType
	Raw  json.RawMessage
}

func (d UnknownLedgerDelta) ledgerDeltaType() LedgerDeltaType { return d.Type }

// DecodeLedgerDelta decodes the provider's typed discriminated union while
// preserving unknown variants verbatim. It does not require an update hash or
// infer an execution outcome, capital effect or PnL treatment.
func DecodeLedgerDelta(data json.RawMessage) (NonFundingLedgerDelta, error) {
	var discriminator struct {
		Type LedgerDeltaType `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return nil, fmt.Errorf("decoding ledger delta type: %w", err)
	}
	if discriminator.Type == "" {
		return nil, fmt.Errorf("ledger delta type is empty")
	}

	var target NonFundingLedgerDelta
	switch discriminator.Type {
	case LedgerDeltaDeposit:
		target = new(DepositLedgerDelta)
	case LedgerDeltaWithdraw:
		target = new(WithdrawLedgerDelta)
	case LedgerDeltaInternalTransfer:
		target = new(InternalTransferLedgerDelta)
	case LedgerDeltaSubAccountTransfer:
		target = new(SubAccountTransferLedgerDelta)
	case LedgerDeltaLiquidation:
		target = new(LiquidationLedgerDelta)
	case LedgerDeltaVaultCreate, LedgerDeltaVaultDeposit, LedgerDeltaVaultDistribution:
		target = new(VaultLedgerDelta)
	case LedgerDeltaVaultWithdraw:
		target = new(VaultWithdrawalLedgerDelta)
	case LedgerDeltaVaultLeaderCommission:
		target = new(VaultLeaderCommissionLedgerDelta)
	case LedgerDeltaSpotTransfer:
		target = new(SpotTransferLedgerDelta)
	case LedgerDeltaSend:
		if err := requireNonNullJSONObjectFields(
			data,
			"user", "destination", "sourceDex", "destinationDex", "token", "amount",
			"usdcValue", "fee", "nativeTokenFee", "nonce", "feeToken",
		); err != nil {
			return nil, err
		}
		target = new(SendLedgerDelta)
	case LedgerDeltaAccountClassTransfer:
		if err := requireNonNullJSONObjectFields(data, "usdc", "toPerp"); err != nil {
			return nil, err
		}
		target = new(AccountClassTransferLedgerDelta)
	case LedgerDeltaSpotGenesis:
		target = new(SpotGenesisLedgerDelta)
	case LedgerDeltaRewardsClaim:
		target = new(RewardsClaimLedgerDelta)
	default:
		return UnknownLedgerDelta{Type: discriminator.Type, Raw: bytes.Clone(data)}, nil
	}

	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("decoding %s ledger delta: %w", discriminator.Type, err)
	}
	return target, nil
}
