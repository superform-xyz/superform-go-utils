package hypercore

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	sharedutils "github.com/superform-xyz/superform-go-utils/utils"
)

// CoreWriterRawActionTopic is the event topic for RawAction(address,bytes).
var CoreWriterRawActionTopic = crypto.Keccak256Hash([]byte("RawAction(address,bytes)"))

var (
	coreWriterRawActionData = abi.Arguments{{Type: sharedutils.BytesType()}}
	usdClassTransferData    = abi.Arguments{
		{Type: sharedutils.Uint64Type()},
		{Type: sharedutils.BoolType()},
	}
	addAPIWalletData = abi.Arguments{
		{Type: sharedutils.AddressType()},
		{Type: sharedutils.StringType()},
	}
	approveBuilderFeeData = abi.Arguments{
		{Type: sharedutils.Uint64Type()},
		{Type: sharedutils.AddressType()},
	}
	sendAssetData = abi.Arguments{
		{Type: sharedutils.AddressType()},
		{Type: sharedutils.AddressType()},
		{Type: sharedutils.Uint32Type()},
		{Type: sharedutils.Uint32Type()},
		{Type: sharedutils.Uint64Type()},
		{Type: sharedutils.Uint64Type()},
	}
)

// CoreWriterAction is a decoded version-one CoreWriter action.
//
// Unknown action IDs are represented by UnknownCoreWriterAction so callers can
// retain evidence emitted after the protocol adds a new action.
type CoreWriterAction interface {
	CoreWriterActionID() uint32
}

// USDClassTransferAction is CoreWriter action 7.
type USDClassTransferAction struct {
	Ntl    uint64
	ToPerp bool
}

// CoreWriterActionID returns the CoreWriter action ID.
func (USDClassTransferAction) CoreWriterActionID() uint32 { return ActionUsdClassTransfer }

// AddAPIWalletAction is CoreWriter action 9.
type AddAPIWalletAction struct {
	Agent common.Address
	Name  string
}

// CoreWriterActionID returns the CoreWriter action ID.
func (AddAPIWalletAction) CoreWriterActionID() uint32 { return ActionAddApiWallet }

// ApproveBuilderFeeAction is CoreWriter action 12.
type ApproveBuilderFeeAction struct {
	MaxFeeRate uint64
	Builder    common.Address
}

// CoreWriterActionID returns the CoreWriter action ID.
func (ApproveBuilderFeeAction) CoreWriterActionID() uint32 { return ActionApproveBuilderFee }

// SendAssetAction is CoreWriter action 13.
type SendAssetAction struct {
	Destination    common.Address
	Subaccount     common.Address
	SourceDex      uint32
	DestinationDex uint32
	Token          uint64
	Wei            uint64
}

// CoreWriterActionID returns the CoreWriter action ID.
func (SendAssetAction) CoreWriterActionID() uint32 { return ActionSendAsset }

// UnknownCoreWriterAction preserves the action-specific payload of an unknown
// version-one CoreWriter action.
type UnknownCoreWriterAction struct {
	ID      uint32
	Payload []byte
}

// CoreWriterActionID returns the unknown CoreWriter action ID.
func (a UnknownCoreWriterAction) CoreWriterActionID() uint32 { return a.ID }

// DecodedCoreWriterAction is the immutable evidence extracted from one
// CoreWriter RawAction log.
type DecodedCoreWriterAction struct {
	User            common.Address
	Version         byte
	ActionID        uint32
	RawAction       []byte
	Action          CoreWriterAction
	TransactionHash common.Hash
	BlockHash       common.Hash
	BlockNumber     uint64
	LogIndex        uint
}

// DecodeCoreWriterAction decodes the bytes passed to sendRawAction. Supported
// action payloads must be canonical Solidity ABI encodings. Unknown version-one
// action IDs are retained rather than rejected.
func DecodeCoreWriterAction(raw []byte) (CoreWriterAction, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("CoreWriter action is %d bytes; need at least the 4-byte header", len(raw))
	}
	if raw[0] != CoreWriterEncodingVersion {
		return nil, fmt.Errorf("unsupported CoreWriter encoding version %d", raw[0])
	}

	actionID := uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	payload := raw[4:]

	switch actionID {
	case ActionUsdClassTransfer:
		values, err := unpackCanonicalAction(actionID, usdClassTransferData, payload)
		if err != nil {
			return nil, err
		}
		return USDClassTransferAction{Ntl: values[0].(uint64), ToPerp: values[1].(bool)}, nil
	case ActionAddApiWallet:
		values, err := unpackCanonicalAction(actionID, addAPIWalletData, payload)
		if err != nil {
			return nil, err
		}
		return AddAPIWalletAction{Agent: values[0].(common.Address), Name: values[1].(string)}, nil
	case ActionApproveBuilderFee:
		values, err := unpackCanonicalAction(actionID, approveBuilderFeeData, payload)
		if err != nil {
			return nil, err
		}
		return ApproveBuilderFeeAction{MaxFeeRate: values[0].(uint64), Builder: values[1].(common.Address)}, nil
	case ActionSendAsset:
		values, err := unpackCanonicalAction(actionID, sendAssetData, payload)
		if err != nil {
			return nil, err
		}
		return SendAssetAction{
			Destination:    values[0].(common.Address),
			Subaccount:     values[1].(common.Address),
			SourceDex:      values[2].(uint32),
			DestinationDex: values[3].(uint32),
			Token:          values[4].(uint64),
			Wei:            values[5].(uint64),
		}, nil
	default:
		return UnknownCoreWriterAction{ID: actionID, Payload: bytes.Clone(payload)}, nil
	}
}

func unpackCanonicalAction(actionID uint32, arguments abi.Arguments, payload []byte) ([]any, error) {
	values, err := arguments.Unpack(payload)
	if err != nil {
		return nil, fmt.Errorf("decoding CoreWriter action %d payload: %w", actionID, err)
	}
	repacked, err := arguments.Pack(values...)
	if err != nil {
		return nil, fmt.Errorf("re-encoding CoreWriter action %d payload: %w", actionID, err)
	}
	if !bytes.Equal(repacked, payload) {
		return nil, fmt.Errorf("CoreWriter action %d payload is not canonical ABI encoding", actionID)
	}
	return values, nil
}

// DecodeCoreWriterRawActionLog validates and decodes one CoreWriter RawAction
// log. It rejects removed logs because they are not durable execution evidence.
func DecodeCoreWriterRawActionLog(log *types.Log) (DecodedCoreWriterAction, error) {
	if log == nil {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter log is nil")
	}
	if log.Address != CoreWriterAddress {
		return DecodedCoreWriterAction{}, fmt.Errorf("log address %s is not CoreWriter %s", log.Address, CoreWriterAddress)
	}
	if log.Removed {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter log %d was removed", log.Index)
	}
	if len(log.Topics) != 2 {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter RawAction log has %d topics; want 2", len(log.Topics))
	}
	if log.Topics[0] != CoreWriterRawActionTopic {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter log topic %s is not RawAction %s", log.Topics[0], CoreWriterRawActionTopic)
	}
	if !bytes.Equal(log.Topics[1].Bytes()[:12], make([]byte, 12)) {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter RawAction user topic is not a canonical address")
	}

	values, err := coreWriterRawActionData.Unpack(log.Data)
	if err != nil {
		return DecodedCoreWriterAction{}, fmt.Errorf("decoding CoreWriter RawAction event data: %w", err)
	}
	if len(values) != 1 {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter RawAction event decoded to %d values; want 1", len(values))
	}
	raw, ok := values[0].([]byte)
	if !ok {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter RawAction event data has type %T; want []byte", values[0])
	}
	repacked, err := coreWriterRawActionData.Pack(raw)
	if err != nil {
		return DecodedCoreWriterAction{}, fmt.Errorf("re-encoding CoreWriter RawAction event data: %w", err)
	}
	if !bytes.Equal(repacked, log.Data) {
		return DecodedCoreWriterAction{}, fmt.Errorf("CoreWriter RawAction event data is not canonical ABI encoding")
	}

	action, err := DecodeCoreWriterAction(raw)
	if err != nil {
		return DecodedCoreWriterAction{}, err
	}

	return DecodedCoreWriterAction{
		User:            common.BytesToAddress(log.Topics[1].Bytes()[12:]),
		Version:         raw[0],
		ActionID:        action.CoreWriterActionID(),
		RawAction:       bytes.Clone(raw),
		Action:          action,
		TransactionHash: log.TxHash,
		BlockHash:       log.BlockHash,
		BlockNumber:     log.BlockNumber,
		LogIndex:        log.Index,
	}, nil
}

// DecodeCoreWriterRawActions decodes all CoreWriter logs in a successful
// receipt. It returns valid actions alongside a joined error for malformed
// CoreWriter logs so one bad occurrence does not discard other evidence.
func DecodeCoreWriterRawActions(receipt *types.Receipt) ([]DecodedCoreWriterAction, error) {
	if receipt == nil {
		return nil, fmt.Errorf("receipt is nil")
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("receipt %s did not succeed", receipt.TxHash)
	}
	hasCoreWriterLog := false
	for _, log := range receipt.Logs {
		if log != nil && log.Address == CoreWriterAddress {
			hasCoreWriterLog = true
			break
		}
	}
	if !hasCoreWriterLog {
		return nil, nil
	}
	if receipt.TxHash == (common.Hash{}) {
		return nil, fmt.Errorf("successful receipt has no transaction hash")
	}
	if receipt.BlockNumber == nil {
		return nil, fmt.Errorf("successful receipt %s has no block number", receipt.TxHash)
	}
	if !receipt.BlockNumber.IsUint64() {
		return nil, fmt.Errorf("successful receipt %s has invalid block number %s", receipt.TxHash, receipt.BlockNumber)
	}

	actions := make([]DecodedCoreWriterAction, 0)
	errs := make([]error, 0)
	for _, log := range receipt.Logs {
		if log == nil {
			continue
		}
		if log.Address != CoreWriterAddress {
			continue
		}
		if log.TxHash != (common.Hash{}) && log.TxHash != receipt.TxHash {
			errs = append(errs, fmt.Errorf("CoreWriter log %d transaction hash %s does not match receipt %s", log.Index, log.TxHash, receipt.TxHash))
			continue
		}
		if receipt.BlockNumber != nil && log.BlockNumber != 0 && receipt.BlockNumber.Uint64() != log.BlockNumber {
			errs = append(errs, fmt.Errorf("CoreWriter log %d block %d does not match receipt block %s", log.Index, log.BlockNumber, receipt.BlockNumber))
			continue
		}
		if log.BlockHash != (common.Hash{}) && receipt.BlockHash != (common.Hash{}) && log.BlockHash != receipt.BlockHash {
			errs = append(errs, fmt.Errorf("CoreWriter log %d block hash %s does not match receipt %s", log.Index, log.BlockHash, receipt.BlockHash))
			continue
		}

		decoded, err := DecodeCoreWriterRawActionLog(log)
		if err != nil {
			errs = append(errs, fmt.Errorf("decoding CoreWriter log %d: %w", log.Index, err))
			continue
		}
		if decoded.TransactionHash == (common.Hash{}) {
			decoded.TransactionHash = receipt.TxHash
		}
		if decoded.BlockNumber == 0 {
			decoded.BlockNumber = receipt.BlockNumber.Uint64()
		}
		if decoded.BlockHash == (common.Hash{}) {
			decoded.BlockHash = receipt.BlockHash
		}
		actions = append(actions, decoded)
	}

	return actions, errors.Join(errs...)
}
