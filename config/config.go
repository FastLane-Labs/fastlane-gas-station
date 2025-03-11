package config

import (
	"crypto/ecdsa"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type GasStationConfig struct {
	EthRpcUrl       string
	GasStationPk    *ecdsa.PrivateKey
	RecheckInterval time.Duration
	MetricsEnabled  bool
}

type CarConfig struct {
	Addr            common.Address
	IdealBalance    *big.Int
	ToleranceInBips int64
}
