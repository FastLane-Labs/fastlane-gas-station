package config

import (
	"crypto/ecdsa"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
)

type GasStationConfig struct {
	EthRpcUrl            string
	GasStationPk         *ecdsa.PrivateKey
	RecheckInterval      time.Duration
	PrometheusRegisterer prometheus.Registerer
}

type CarConfig struct {
	Addr            common.Address
	IdealBalance    *big.Int
	ToleranceInBips int64
}
