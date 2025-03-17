package gasstation

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/FastLane-Labs/blockchain-rpc-go/eth"
	"github.com/FastLane-Labs/blockchain-rpc-go/rpc"
	"github.com/FastLane-Labs/fastlane-gas-station/config"
	"github.com/FastLane-Labs/fastlane-gas-station/contract/multicall"
	"github.com/FastLane-Labs/fastlane-gas-station/metrics"
	"github.com/FastLane-Labs/fastlane-gas-station/txsender"
	"github.com/FastLane-Labs/fastlane-gas-station/utils"
	"github.com/ethereum/go-ethereum/common"
	gethEthClient "github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	gethRpc "github.com/ethereum/go-ethereum/rpc"
)

type GasStation struct {
	ethClient       eth.IEthClient
	carConfigs      []*config.CarConfig
	recheckInterval time.Duration
	txSender        *txsender.TxSender

	chainId *big.Int

	metrics *metrics.Metrics
	logger  log.Logger

	shutdown chan struct{}
}

func NewGasStation(config *config.GasStationConfig, carConfigs []*config.CarConfig) (*GasStation, error) {
	logger := log.Root().New("service", "gas-station")

	var ethClient eth.IEthClient
	switch v := config.RpcClient.(type) {
	case *eth.EthClient:
		ethClient = v
	case *rpc.RpcClient:
		ethClient = eth.NewClient(v)
	case *gethRpc.Client:
		ethClient = eth.NewClient(v)
	case *gethEthClient.Client:
		ethClient = eth.NewClient(v.Client())
	default:
		return nil, fmt.Errorf("unsupported rpc client type: %T", v)
	}

	chainId, err := ethClient.ChainID(context.Background())
	if err != nil {
		return nil, err
	}

	metrics := metrics.NewMetrics(config.PrometheusRegisterer, config.PrometheusRegisterer != nil)

	txSender, err := txsender.NewTxSender(ethClient, config.GasStationPk, metrics, logger)
	if err != nil {
		return nil, err
	}

	return &GasStation{
		ethClient:       ethClient,
		txSender:        txSender,
		carConfigs:      carConfigs,
		recheckInterval: config.RecheckInterval,
		metrics:         metrics,
		logger:          logger,
		chainId:         chainId,
		shutdown:        make(chan struct{}),
	}, nil
}

func (s *GasStation) Start() {
	s.logger.Info("starting gas station", "filler", s.txSender.Address())

	for {
		s.refill()

		select {
		case <-s.shutdown:
			return
		case <-time.After(s.recheckInterval):
		}
	}
}

func (s *GasStation) Stop() {
	close(s.shutdown)
}

func (s *GasStation) refill() {
	balances, err := s.batchGetBalances(s.ethClient, s.carConfigs)
	if err != nil {
		s.logger.Error("failed to get balances", "error", err)
		return
	}

	s.logger.Debug("balances fetched", "num", len(balances))

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
		s.logger.Debug("no accounts to refill")
		return
	}

	s.logger.Debug("accounts to refill", "num", len(accountsToRefill))

	stationBalance, err := s.ethClient.BalanceAt(context.Background(), s.txSender.Address(), nil)
	if err != nil {
		s.logger.Error("failed to get station balance", "error", err)
		return
	}

	if totalRefillAmount.Cmp(stationBalance) > 0 {
		s.logger.Error("not enough balance", "balance", stationBalance, "required", totalRefillAmount)
		return
	}

	for addr, amount := range accountsToRefill {
		txHash, err := s.txSender.Send(addr, amount, []byte{}, 50_000)
		if err != nil {
			s.logger.Error("failed to send tx", "error", err)
		}

		s.logger.Info("sent gas-refill tx", "addr", addr, "amount", amount, "txHash", txHash)
	}
}

func (s *GasStation) batchGetBalances(client eth.IEthClient, carConfigs []*config.CarConfig) (map[common.Address]*big.Int, error) {
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
		s.logger,
	)

	if err != nil {
		return nil, fmt.Errorf("error multicalling: %w", err)
	}
	return balances, nil
}
