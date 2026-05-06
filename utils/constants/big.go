package constants

import "github.com/ethereum/go-ethereum/common"

var (
	// Big0 is the big integer value 0
	Big0 = common.Big0
	// Big1 is the big integer value 1
	Big1 = common.Big1
	// Big2 is the big integer value 2
	Big2 = common.Big2
	// Big3 is the big integer value 3
	Big3 = common.Big3

	// Big0Hash is the big integer value 0 as a hash
	Big0Hash = common.BigToHash(Big0)
	// Big1Hash is the big integer value 1 as a hash
	Big1Hash = common.BigToHash(Big1)
	// Big2Hash is the big integer value 2 as a hash
	Big2Hash = common.BigToHash(Big2)
	// Big3Hash is the big integer value 3 as a hash
	Big3Hash = common.BigToHash(Big3)
)
