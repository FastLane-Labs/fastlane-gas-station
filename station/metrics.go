package station

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	Enabled bool

	Balance      *prometheus.GaugeVec
	RefillTxSent *prometheus.CounterVec
}

func NewMetrics(reg prometheus.Registerer, enabled bool) *Metrics {
	m := &Metrics{
		Enabled: enabled,
	}

	if !enabled {
		return m
	}

	m.Balance = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gasstation_balance",
		Help: "Balance of the gas station",
	}, []string{"chainId", "transactor", "tag"})

	m.RefillTxSent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gasstation_refill_tx_sent",
		Help: "Number of refill tx sent",
	}, []string{"chainId"})

	reg.MustRegister(
		m.Balance,
		m.RefillTxSent,
	)

	return m
}
