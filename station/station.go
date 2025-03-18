package station

import (
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/FastLane-Labs/blockchain-rpc-go/eth"
	"github.com/FastLane-Labs/fastlane-gas-station/contract/multicall"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	tagCar                = "car"
	tagStation            = "station"
	getEthBalanceFunction = "getEthBalance"
)

var (
	multicallAddr   = common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11")
	multicallAbi, _ = multicall.MulticallMetaData.GetAbi()
)

type Transactor interface {
	ChainId() *big.Int
	Address() common.Address
	GetBalance() *big.Int
	EthClient() eth.IEthClient
	TransferEthCost() (*big.Int, error)

	// Importing package must handle whole transaction flow, including gas estimation, tx replacement, etc.
	Transfer(value *big.Int, to common.Address)
}

type Car struct {
	Transactor                         Transactor
	TransactorBalanceThreshold         *big.Int       // Balance exceeding this threshold will transfered out to TransactorExcessBalanceBeneficiary
	TransactorBalanceTarget            *big.Int       // Transfers down to this target
	TransactorExcessBalanceBeneficiary common.Address // Address to send excess balance to

	Wheels                 []common.Address
	WheelsBalanceThreshold *big.Int // Refills are triggered when balance is below this threshold
	WheelsBalanceTarget    *big.Int // Refills up to this target

	TaskInterval time.Duration
}

type GasStation struct {
	cars map[uint64]*Car

	metrics *Metrics
	logger  log.Logger

	shutdown chan struct{}
}

func NewGasStation(cars []*Car, prometheusRegisterer prometheus.Registerer) (*GasStation, error) {
	var (
		chainIds = make(map[uint64]struct{})
		_cars    = make(map[uint64]*Car)
	)

	for _, car := range cars {
		chainId := car.Transactor.ChainId().Uint64()

		if _, ok := chainIds[chainId]; ok {
			return nil, fmt.Errorf("duplicate chainId: %d", chainId)
		}

		chainIds[chainId] = struct{}{}
		_cars[chainId] = car
	}

	return &GasStation{
		cars:     _cars,
		metrics:  NewMetrics(prometheusRegisterer, prometheusRegisterer != nil),
		logger:   log.Root().New("service", "gas-station"),
		shutdown: make(chan struct{}),
	}, nil
}

func (s *GasStation) Start() {
	for chainId, car := range s.cars {
		go s.start(chainId, car)
	}
}

func (s *GasStation) start(chainId uint64, car *Car) {
	s.logger.Info("starting gas station", "chainId", chainId)

	_chainId := strconv.FormatUint(chainId, 10)

	for {
		s.runTask(_chainId, car)

		select {
		case <-s.shutdown:
			return
		case <-time.After(car.TaskInterval):
		}
	}
}

func (s *GasStation) Stop() {
	s.logger.Info("stopping gas station")
	close(s.shutdown)
}

func (s *GasStation) runTask(chainId string, car *Car) {
	// 1. Transfer excess balance if any
	transactorBalance := car.Transactor.GetBalance()

	if s.metrics.Enabled {
		f, _ := transactorBalance.Float64()
		s.metrics.Balance.WithLabelValues(chainId, car.Transactor.Address().Hex(), tagStation).Set(f)
	}

	if transactorBalance.Cmp(car.TransactorBalanceThreshold) > 0 {
		excessBalance := new(big.Int).Sub(transactorBalance, car.TransactorBalanceTarget)
		car.Transactor.Transfer(excessBalance, car.TransactorExcessBalanceBeneficiary)
	}

	// 2. Refill wheels
	wheelsBalances, err := s.batchGetBalances(car.Transactor.EthClient(), car.Wheels)
	if err != nil {
		s.logger.Error("failed to get balances", "error", err)
		return
	}

	if s.metrics.Enabled {
		for account, balance := range wheelsBalances {
			f, _ := balance.Float64()
			s.metrics.Balance.WithLabelValues(chainId, account.Hex(), tagCar).Set(f)
		}
	}

	transferEthCost, err := car.Transactor.TransferEthCost()
	if err != nil {
		s.logger.Error("failed to get transfer eth cost", "error", err)
		return
	}

	var (
		availableFunds   = car.Transactor.GetBalance()
		accountsToRefill = make(map[common.Address]*big.Int)
	)

	for _, account := range car.Wheels {
		balance, ok := wheelsBalances[account]
		if !ok {
			s.logger.Error("balance not found", "account", account)
			continue
		}

		if balance.Cmp(car.WheelsBalanceThreshold) >= 0 {
			// Car does not need refill
			continue
		}

		availableFunds.Sub(availableFunds, transferEthCost)

		if availableFunds.Cmp(common.Big0) <= 0 {
			// No more funds available
			break
		}

		refillAmount := new(big.Int).Sub(car.WheelsBalanceTarget, balance)
		if refillAmount.Cmp(availableFunds) > 0 {
			refillAmount = availableFunds
		}

		accountsToRefill[account] = refillAmount
		availableFunds.Sub(availableFunds, refillAmount)
	}

	if len(accountsToRefill) == 0 {
		// No accounts to refill
		return
	}

	for addr, amount := range accountsToRefill {
		car.Transactor.Transfer(amount, addr)
		if s.metrics.Enabled {
			s.metrics.RefillTxSent.WithLabelValues(chainId).Inc()
		}
	}
}

func (s *GasStation) batchGetBalances(client eth.IEthClient, accounts []common.Address) (map[common.Address]*big.Int, error) {
	var (
		balances = make(map[common.Address]*big.Int)
		indices  = make([]int, len(accounts))
		mu       sync.Mutex
	)

	for i := range accounts {
		indices[i] = i
	}

	calldataBatchGenerator := func(idx int) ([]multicall.Multicall3Call, error) {
		calldata, err := multicallAbi.Pack(getEthBalanceFunction, accounts[idx])
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

		balance, err := multicallAbi.Unpack(getEthBalanceFunction, returnDatas[0])
		if err != nil {
			return fmt.Errorf("error unpacking return data: %w", err)
		}

		mu.Lock()
		balances[accounts[idx]] = balance[0].(*big.Int)
		mu.Unlock()

		return nil
	}

	if err := Multicall(client, calldataBatchGenerator, returnDataBatchHandler, indices, s.logger); err != nil {
		return nil, fmt.Errorf("error multicalling: %w", err)
	}

	return balances, nil
}
