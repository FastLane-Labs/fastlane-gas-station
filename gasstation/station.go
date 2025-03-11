package gasstation

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/FastLane-Labs/fastlane-gas-station/config"
	"github.com/FastLane-Labs/fastlane-gas-station/contract/multicall"
	"github.com/FastLane-Labs/fastlane-gas-station/events"
	"github.com/FastLane-Labs/fastlane-gas-station/metrics"
	"github.com/FastLane-Labs/fastlane-gas-station/txsender"
	"github.com/FastLane-Labs/fastlane-gas-station/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/prometheus/client_golang/prometheus"
)

type GasStation struct {
	ethClient       *events.EthClientConnection
	carConfigs      []*config.CarConfig
	recheckInterval time.Duration
	txSender        *txsender.TxSender

	chainId *big.Int

	metrics *metrics.Metrics
}

func NewGasStation(config *config.GasStationConfig, carConfigs []*config.CarConfig) (*GasStation, error) {
	ethClient, err := events.NewEthClientConnection(config.EthRpcUrl)
	if err != nil {
		return nil, err
	}

	chainId, err := ethClient.Client.ChainID(context.Background())
	if err != nil {
		return nil, err
	}

	metrics := metrics.NewMetrics(prometheus.DefaultRegisterer, config.MetricsEnabled)

	txSender, err := txsender.NewTxSender(ethClient.Client, config.GasStationPk, metrics)
	if err != nil {
		return nil, err
	}

	return &GasStation{
		ethClient:       ethClient,
		txSender:        txSender,
		carConfigs:      carConfigs,
		recheckInterval: config.RecheckInterval,
		metrics:         metrics,
		chainId:         chainId,  
	}, nil
}

func (s *GasStation) Start() {
	log.Info("Starting gas station", "filler", s.txSender.Address())
	s.ethClient.Start()

	go func() {
		time.Sleep(s.recheckInterval)
		s.refill()
	}()
}

func (s *GasStation) refill() {
	balances, err := s.batchGetBalances(s.ethClient.Client, s.carConfigs)
	if err != nil {
		log.Error("Failed to get balances", "error", err)
		return
	}

	log.Debug("gas station: balances fetched", "num", len(balances))

	accountsToRefill := make(map[common.Address]*big.Int)
	totalRefillAmount := big.NewInt(0)

	for _, carConfig := range s.carConfigs {
		if bal, ok := balances[carConfig.Addr]; ok {
			balanceTolerance := new(big.Int).Mul(carConfig.IdealBalance, big.NewInt(carConfig.ToleranceInBips))
			balanceTolerance.Div(balanceTolerance, big.NewInt(10000))

			minBalance := new(big.Int).Sub(carConfig.IdealBalance, balanceTolerance)
			if bal.Cmp(minBalance) < 0 {
				shortfall := new(big.Int).Sub(minBalance, bal)
				accountsToRefill[carConfig.Addr] = shortfall
				totalRefillAmount.Add(totalRefillAmount, shortfall)
			}
		}
	}

	if s.metrics.Enabled {
		s.metrics.LowBalanceAccounts.WithLabelValues(s.chainId.String()).Set(float64(len(accountsToRefill)))
	}

	if len(accountsToRefill) == 0 {
		log.Debug("gas station: no accounts to refill")
		return
	}

	log.Debug("gas station: accounts to refill", "num", len(accountsToRefill))

	stationBalance, err := s.ethClient.Client.BalanceAt(context.Background(), s.txSender.Address(), nil)
	if err != nil {
		log.Error("Failed to get station balance", "error", err)
		return
	}

	if totalRefillAmount.Cmp(stationBalance) > 0 {
		log.Error("gas station: not enough balance", "balance", stationBalance, "required", totalRefillAmount)
		return
	}

	for addr, amount := range accountsToRefill {
		txHash, err := s.txSender.Send(addr, amount, []byte{}, 50_000)
		if err != nil {
			log.Error("failed to send tx", "error", err)
		}

		log.Info("sent gas-refill tx", "addr", addr, "amount", amount, "txHash", txHash)
	}
}

func (s *GasStation) batchGetBalances(client *ethclient.Client, carConfigs []*config.CarConfig) (map[common.Address]*big.Int, error) {
	balances := make(map[common.Address]*big.Int)
	mu := sync.Mutex{}

	multicallAddr := common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11")
	abi, err := multicall.MulticallMetaData.GetAbi()
	if err != nil {
		return nil, fmt.Errorf("error getting abi: %w", err)
	}

	indices := make([]int, len(carConfigs))
	for i := range carConfigs {
		indices[i] = i
	}

	calldataBatchGenerator := func(idx int) ([]multicall.Multicall3Call, error) {
		calldata, err := abi.Pack("getEthBalance", carConfigs[idx].Addr)
		if err != nil {
			return nil, fmt.Errorf("error packing calldata: %w", err)
		}
		return []multicall.Multicall3Call{
			{
				Target:   multicallAddr,
				CallData: calldata,
			},
		}, nil
	}

	returnDataBatchHandler := func(idx int, returnDatas [][]byte) error {
		if len(returnDatas) != 1 {
			return fmt.Errorf("expected 1 return data, got %d", len(returnDatas))
		}
		balance, err := abi.Unpack("getEthBalance", returnDatas[0])
		if err != nil {
			return fmt.Errorf("error unpacking return data: %w", err)
		}
		mu.Lock()
		balances[carConfigs[idx].Addr] = balance[0].(*big.Int)
		mu.Unlock()
		return nil
	}

	err = utils.Multicall(client,
		1,
		1000,
		calldataBatchGenerator,
		returnDataBatchHandler,
		indices,
	)

	if err != nil {
		return nil, fmt.Errorf("error multicalling: %w", err)
	}
	return balances, nil
}
