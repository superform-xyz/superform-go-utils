package txmngr_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr"
)

type signingKMS struct{ key *ecdsa.PrivateKey }

func (s signingKMS) GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := asn1.Marshal(struct {
		Algorithm struct{ ID, Curve asn1.ObjectIdentifier }
		Key       asn1.BitString
	}{
		Algorithm: struct{ ID, Curve asn1.ObjectIdentifier }{
			ID: asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}, Curve: asn1.ObjectIdentifier{1, 3, 132, 0, 10},
		},
		Key: asn1.BitString{Bytes: crypto.FromECDSAPub(&s.key.PublicKey), BitLength: 65 * 8},
	})
	return &kms.GetPublicKeyOutput{PublicKey: der}, err
}

func (s signingKMS) Sign(ctx context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, sigS, err := ecdsa.Sign(rand.Reader, s.key, input.Message)
	if err != nil {
		return nil, err
	}
	der, err := asn1.Marshal(struct{ R, S *big.Int }{r, sigS})
	return &kms.SignOutput{Signature: der}, err
}

func TestTxAuthSignsForRequestedChain(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	for _, useKMS := range []bool{false, true} {
		t.Run(map[bool]string{false: "private key", true: "KMS"}[useKMS], func(t *testing.T) {
			address, auth, err := txmngr.GetTxAuthFromPrivateKey(hex.EncodeToString(crypto.FromECDSA(key)))
			if useKMS {
				address, auth, err = txmngr.GetTxAuthFromKMS(t.Context(), signingKMS{key}, t.Name()+crypto.PubkeyToAddress(key.PublicKey).Hex())
			}
			require.NoError(t, err)
			require.Equal(t, crypto.PubkeyToAddress(key.PublicKey), address)
			for _, chainID := range []uint64{1, 8453} {
				opts, err := auth(chainID)
				require.NoError(t, err)
				tx := types.NewTx(&types.DynamicFeeTx{ChainID: new(big.Int).SetUint64(chainID), Gas: 21_000})
				signed, err := opts.Signer(address, tx)
				require.NoError(t, err)
				sender, err := types.Sender(types.LatestSignerForChainID(new(big.Int).SetUint64(chainID)), signed)
				require.NoError(t, err)
				require.Equal(t, address, sender)
			}
		})
	}
}

func TestKMSTxAuthRespectsSigningCancellation(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	address, auth, err := txmngr.GetTxAuthFromKMS(ctx, signingKMS{key}, t.Name()+crypto.PubkeyToAddress(key.PublicKey).Hex())
	require.NoError(t, err)
	opts, err := auth(1)
	require.NoError(t, err)
	cancel()
	_, err = opts.Signer(address, types.NewTx(&types.LegacyTx{Gas: 21_000}))
	require.ErrorIs(t, err, context.Canceled)
}
