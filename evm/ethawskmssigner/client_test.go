package ethawskmssigner_test

import (
	"context"
	"encoding/asn1"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"github.com/superform-xyz/superform-go-utils/evm/ethawskmssigner"
)

func TestInitKMSClientUsesCallerConfiguration(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	der, err := asn1.Marshal(struct {
		Algorithm struct{ ID, Curve asn1.ObjectIdentifier }
		Key       asn1.BitString
	}{
		Algorithm: struct{ ID, Curve asn1.ObjectIdentifier }{
			ID: asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}, Curve: asn1.ObjectIdentifier{1, 3, 132, 0, 10},
		},
		Key: asn1.BitString{Bytes: crypto.FromECDSAPub(&key.PublicKey), BitLength: 65 * 8},
	})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "TrentService.GetPublicKey" {
			t.Error("unexpected AWS operation")
			http.Error(w, "unexpected operation", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if err := json.NewEncoder(w).Encode(map[string]any{"PublicKey": der}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg := aws.Config{
		Region:     "us-east-1",
		HTTPClient: server.Client(),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		}),
	}
	keyID := t.Name() + crypto.PubkeyToAddress(key.PublicKey).Hex()
	client, publicKey, err := ethawskmssigner.InitKMSClient(t.Context(), keyID, cfg, func(opts *kms.Options) {
		opts.BaseEndpoint = &server.URL
	})
	require.NoError(t, err)
	require.Equal(t, server.URL, *client.Options().BaseEndpoint)
	require.Equal(t, crypto.FromECDSAPub(&key.PublicKey), crypto.FromECDSAPub(publicKey))
}
