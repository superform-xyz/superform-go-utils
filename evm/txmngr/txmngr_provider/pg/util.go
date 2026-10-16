package pg

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/pkg/errors"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

func transactionToTxModel(tx txmngr_provider.Transaction) (map[string]interface{}, error) {
	value := tx.Value
	if value == nil {
		value = new(big.Int)
	}
	txFields := map[string]interface{}{
		"id":                    tx.ID,
		"chain_id":              tx.ChainID,
		"intent_id":             tx.IntentID,
		"operation_key":         tx.OperationKey,
		"payload_hash":          tx.PayloadHash,
		"sender":                nullableAddress(tx.Sender),
		"address":               tx.Address.Hex(),
		"value":                 value.String(),
		"data":                  tx.Data,
		"gas_limit":             tx.GasLimit,
		"assigned_nonce":        tx.AssignedNonce,
		"status":                string(tx.Status),
		"msg":                   tx.Msg,
		"next_action_at":        tx.NextActionAt,
		"broadcast_pending":     tx.BroadcastPending,
		"rejected_attempt_hash": tx.RejectedAttemptHash,
		"nonce_consumed_at":     tx.NonceConsumedAt,
		"notified_at":           tx.NotifiedAt,
		"notification_attempts": tx.NotificationAttempts,
		"next_notification_at":  tx.NextNotificationAt,
		"notification_error":    tx.NotificationError,
		"updated_at":            tx.UpdatedAt,
		"created_at":            tx.CreatedAt,
	}

	if tx.RawAuthorizations != nil {
		rawTx, err := json.Marshal(tx.RawAuthorizations)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal raw authorizations")
		}

		txFields["raw_authorizations"] = string(rawTx)
	}

	if tx.Tx != nil {
		rawTx, err := tx.Tx.MarshalJSON()
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal raw tx")
		}

		txFields["raw_tx"] = string(rawTx)
	}

	if tx.Receipt != nil {
		rawReceipt, err := tx.Receipt.MarshalJSON()
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal raw receipt")
		}

		txFields["raw_receipt"] = string(rawReceipt)
	}

	return txFields, nil
}

func txModelToTransaction(tx txModel) (*txmngr_provider.Transaction, error) {
	var (
		ethTx          *types.Transaction
		receipt        *types.Receipt
		authorizations []types.SetCodeAuthorization
	)

	if tx.RawAuthorizations != nil && *tx.RawAuthorizations != "" {
		if err := json.Unmarshal([]byte(*tx.RawAuthorizations), &authorizations); err != nil {
			return nil, errors.Wrap(err, "failed to unmarshal raw authorizations")
		}
	}

	if tx.RawTx != "{}" && tx.RawTx != "" {
		ethTx = new(types.Transaction)
		if err := ethTx.UnmarshalJSON([]byte(tx.RawTx)); err != nil {
			return nil, errors.Wrap(err, "failed to unmarshal raw tx")
		}
	}

	if tx.RawReceipt != "{}" && tx.RawReceipt != "" {
		receipt = new(types.Receipt)
		if err := receipt.UnmarshalJSON([]byte(tx.RawReceipt)); err != nil {
			return nil, errors.Wrap(err, "failed to unmarshal raw receipt")
		}
	}

	value, valueOk := big.NewInt(0).SetString(tx.Value, 10)
	if !valueOk {
		return nil, errors.New("failed to parse value")
	}

	return &txmngr_provider.Transaction{
		ID:                   tx.ID,
		ChainID:              tx.ChainID,
		IntentID:             tx.IntentID,
		OperationKey:         tx.OperationKey,
		PayloadHash:          tx.PayloadHash,
		Sender:               common.HexToAddress(pointerValue(tx.Sender)),
		Address:              common.HexToAddress(tx.Address),
		Value:                value,
		Data:                 tx.Data,
		GasLimit:             tx.GasLimit,
		RawAuthorizations:    authorizations,
		AssignedNonce:        tx.AssignedNonce,
		Tx:                   ethTx,
		Receipt:              receipt,
		Status:               tx.Status,
		Msg:                  tx.Msg,
		NextActionAt:         tx.NextActionAt,
		BroadcastPending:     tx.BroadcastPending,
		RejectedAttemptHash:  normalizedHash(tx.RejectedAttemptHash),
		NonceConsumedAt:      tx.NonceConsumedAt,
		NotifiedAt:           tx.NotifiedAt,
		NotificationAttempts: tx.NotificationAttempts,
		NextNotificationAt:   tx.NextNotificationAt,
		NotificationError:    tx.NotificationError,
		UpdatedAt:            tx.UpdatedAt,
		CreatedAt:            tx.CreatedAt,
	}, nil
}

// normalizedHash canonicalizes a persisted hex hash so comparisons against
// common.Hash.Hex() survive a hand-written repair that used different casing or
// omitted padding. A mismatch would otherwise park the row as unprocessable.
func normalizedHash(value *string) *string {
	if value == nil {
		return nil
	}
	original := *value
	digits := original
	if strings.HasPrefix(digits, "0x") || strings.HasPrefix(digits, "0X") {
		digits = digits[2:]
	}
	if len(digits) == 0 || len(digits) > common.HashLength*2 {
		return &original
	}
	if len(digits)%2 != 0 {
		digits = "0" + digits
	}
	decoded, err := hex.DecodeString(digits)
	if err != nil {
		return &original
	}
	canonical := common.BytesToHash(decoded).Hex()
	return &canonical
}

func nullableAddress(address common.Address) *string {
	if address == (common.Address{}) {
		return nil
	}
	value := address.Hex()
	return &value
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func attemptToModel(attempt txmngr_provider.TransactionAttempt) (map[string]interface{}, error) {
	if attempt.Tx == nil {
		return nil, errors.New("transaction attempt is missing raw transaction")
	}
	rawTx, err := attempt.Tx.MarshalJSON()
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal transaction attempt")
	}
	return map[string]interface{}{
		"transaction_id": attempt.TransactionID,
		"tx_hash":        attempt.Hash.Hex(),
		"nonce":          attempt.Nonce,
		"raw_tx":         string(rawTx),
		"created_at":     attempt.CreatedAt,
	}, nil
}

// attemptModelsToAttempts converts a scanned attempt batch, failing the whole
// batch if any row cannot be decoded: a partially decoded attempt history would
// understate the fees already broadcast for a nonce.
func attemptModelsToAttempts(models []attemptModel) ([]txmngr_provider.TransactionAttempt, error) {
	attempts := make([]txmngr_provider.TransactionAttempt, 0, len(models))
	for _, model := range models {
		attempt, err := attemptModelToAttempt(model)
		if err != nil {
			return nil, fmt.Errorf("converting transaction attempt %s: %w", model.TxHash, err)
		}
		attempts = append(attempts, *attempt)
	}
	return attempts, nil
}

// qualifyColumns prefixes each column with its table, for queries that join and
// would otherwise be ambiguous.
func qualifyColumns(table string, columns []string) []string {
	qualified := make([]string, len(columns))
	for i, column := range columns {
		qualified[i] = table + "." + column
	}
	return qualified
}

func attemptModelToAttempt(model attemptModel) (*txmngr_provider.TransactionAttempt, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalJSON([]byte(model.RawTx)); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal transaction attempt")
	}
	return &txmngr_provider.TransactionAttempt{
		TransactionID: model.TransactionID,
		Hash:          common.HexToHash(model.TxHash),
		Nonce:         model.Nonce,
		Tx:            tx,
		CreatedAt:     model.CreatedAt,
	}, nil
}
