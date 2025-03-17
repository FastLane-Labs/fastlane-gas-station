package station

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	Enabled bool

	LowBalanceAccounts *prometheus.GaugeVec

	RefillTxSent      *prometheus.CounterVec
	RefillTxReverted  *prometheus.CounterVec
	RefillTxSuccess   *prometheus.CounterVec
	RefillTxNotLanded *prometheus.CounterVec
	RefillTxRetries   *prometheus.CounterVec
}

func NewMetrics(reg prometheus.Registerer, enabled bool) *Metrics {
	m := &Metrics{
		Enabled: enabled,
	}

	if !enabled {
		return m
	}

	m.LowBalanceAccounts = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gasstation_low_balance_accounts",
		Help: "Number of low balance accounts",
	}, []string{"chainId"})

	m.RefillTxSent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gasstation_refill_tx_sent",
		Help: "Number of refill tx sent",
	}, []string{"chainId"})

	m.RefillTxReverted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gasstation_refill_tx_reverted",
		Help: "Number of refill tx reverted",
	}, []string{"chainId"})

	m.RefillTxSuccess = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gasstation_refill_tx_success",
		Help: "Number of refill tx success",
	}, []string{"chainId"})

	m.RefillTxNotLanded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gasstation_refill_tx_not_landed",
		Help: "Number of refill tx not landed",
	}, []string{"chainId"})

	m.RefillTxRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gasstation_refill_tx_retries",
		Help: "Number of refill tx retries",
	}, []string{"chainId"})

	reg.MustRegister(
		m.LowBalanceAccounts,
		m.RefillTxSent,
		m.RefillTxReverted,
		m.RefillTxSuccess,
		m.RefillTxNotLanded,
		m.RefillTxRetries,
	)

	return m
}
