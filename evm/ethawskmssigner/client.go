package ethawskmssigner

import (
	"context"
	"crypto/ecdsa"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// InitKMSClient creates a KMS client using the caller's AWS configuration and
// retrieves its signing public key. Credential setup and retry policy belong to
// the caller and the supplied AWS configuration.
func InitKMSClient(ctx context.Context, keyID string, cfg aws.Config, options ...func(*kms.Options)) (*kms.Client, *ecdsa.PublicKey, error) {
	client := kms.NewFromConfig(cfg, options...)
	publicKey, err := GetPubKey(ctx, client, keyID)
	if err != nil {
		return nil, nil, err
	}
	return client, publicKey, nil
}
