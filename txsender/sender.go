package txsender

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"sync"
	"time"

	"github.com/FastLane-Labs/fastlane-gas-station/log"
	"github.com/FastLane-Labs/fastlane-gas-station/metrics"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	refreshDuration = 5 * time.Second
	waitDuration    = 30 * time.Second
)

type state struct {
	broadcastedTxs map[common.Hash]*broadcastedTx
	latestNonce    uint64
	mu             *sync.Mutex
	numRequests    int
}

type txRequest struct {
	requestId int
	to        common.Address
	value     *big.Int
	data      []byte
	gasLimit  uint64
	gasPrice  *big.Int
	nonce     uint64
}

type broadcastedTx struct {
	hash          common.Hash
	request       txRequest
	broadcastedAt time.Time
}

type TxSender struct {
	client *ethclient.Client
	pk     *ecdsa.PrivateKey

	chainId *big.Int

	state *state

	metrics *metrics.Metrics
}

func NewTxSender(client *ethclient.Client, pk *ecdsa.PrivateKey, metrics *metrics.Metrics) (*TxSender, error) {
	chainId, err := client.ChainID(context.Background())
	if err != nil {
		return nil, err
	}

	nonce, err := client.PendingNonceAt(context.Background(), crypto.PubkeyToAddress(pk.PublicKey))
	if err != nil {
		return nil, err
	}

	s := &TxSender{
		client:  client,
		pk:      pk,
		chainId: chainId,
		state: &state{
			broadcastedTxs: make(map[common.Hash]*broadcastedTx),
			latestNonce:    nonce,
			mu:             &sync.Mutex{},
			numRequests:    0,
		},
		metrics: metrics,
	}

	s.start()

	return s, nil
}

func (s *TxSender) start() {
	go func() {
		for {
			time.Sleep(refreshDuration)
			func() {
				s.state.mu.Lock()
				defer s.state.mu.Unlock()

				if len(s.state.broadcastedTxs) == 0 {
					return
				}

				pendingTxs := make(map[common.Hash]*broadcastedTx)
				for _, tx := range s.state.broadcastedTxs {
					if time.Since(tx.broadcastedAt) > waitDuration {
						pendingTxs[tx.hash] = tx
					}
				}

				if len(pendingTxs) == 0 {
					return
				}

				var wg sync.WaitGroup
				var mu sync.Mutex
				for _, tx := range pendingTxs {
					wg.Add(1)
					go func(broadcastTx *broadcastedTx) {
						defer wg.Done()

						receipt, err := s.client.TransactionReceipt(context.Background(), broadcastTx.hash)
						if err != nil {
							log.Info("gas station - tx not landed... will retry",
								"hash", broadcastTx.hash,
							)
							if s.metrics.Enabled {
								s.metrics.RefillTxNotLanded.WithLabelValues(s.chainId.String()).Inc()
							}

							suggestedGasPrice, err := s.client.SuggestGasPrice(context.Background())
							if err != nil {
								log.Error("gas station - retry tx suggest gas price failed",
									"hash", broadcastTx.hash,
									"error", err,
								)
								return
							}

							lastGasPrice := broadcastTx.request.gasPrice
							minNextGasPrice := big.NewInt(0).Mul(lastGasPrice, big.NewInt(110))
							minNextGasPrice = minNextGasPrice.Div(minNextGasPrice, big.NewInt(100))

							nextGasPrice := minNextGasPrice
							if suggestedGasPrice.Cmp(minNextGasPrice) > 0 {
								nextGasPrice = suggestedGasPrice
							}

							tx := types.NewTransaction(
								broadcastTx.request.nonce,
								broadcastTx.request.to,
								broadcastTx.request.value,
								broadcastTx.request.gasLimit,
								nextGasPrice,
								broadcastTx.request.data,
							)

							signedTx, err := types.SignTx(tx, types.NewEIP155Signer(s.chainId), s.pk)
							if err != nil {
								log.Error("gas station - retry tx sign failed",
									"hash", broadcastTx.hash,
									"error", err,
								)
								return
							}

							if err := s.client.SendTransaction(context.Background(), signedTx); err != nil {
								log.Error("gas station - retry tx send failed",
									"hash", broadcastTx.hash,
									"error", err,
								)
								return
							}

							mu.Lock()
							s.state.broadcastedTxs[signedTx.Hash()] = &broadcastedTx{
								hash: signedTx.Hash(),
								request: txRequest{
									requestId: broadcastTx.request.requestId,
									to:        broadcastTx.request.to,
									value:     broadcastTx.request.value,
									data:      broadcastTx.request.data,
									gasLimit:  broadcastTx.request.gasLimit,
									gasPrice:  nextGasPrice,
									nonce:     broadcastTx.request.nonce,
								},
								broadcastedAt: time.Now(),
							}
							mu.Unlock()
							if s.metrics.Enabled {
								s.metrics.RefillTxRetries.WithLabelValues(s.chainId.String()).Inc()
							}
						} else if receipt.Status == types.ReceiptStatusFailed {
							log.Error("gas station - tx reverted",
								"hash", broadcastTx.hash,
								"to", broadcastTx.request.to,
								"value", broadcastTx.request.value,
								"gas", broadcastTx.request.gasLimit,
								"gas price", broadcastTx.request.gasPrice,
								"nonce", broadcastTx.request.nonce,
								"error", err,
							)
							mu.Lock()
							delete(s.state.broadcastedTxs, broadcastTx.hash)
							mu.Unlock()

							if s.metrics.Enabled {
								s.metrics.RefillTxReverted.WithLabelValues(s.chainId.String()).Inc()
							}
						} else {
							mu.Lock()
							delete(s.state.broadcastedTxs, broadcastTx.hash)
							for hash, tx := range s.state.broadcastedTxs {
								if tx.request.requestId == broadcastTx.request.requestId {
									delete(s.state.broadcastedTxs, hash)
								}
							}
							mu.Unlock()

							if s.metrics.Enabled {
								s.metrics.RefillTxSuccess.WithLabelValues(s.chainId.String()).Inc()
							}
						}
					}(tx)
				}

				wg.Wait()
			}()
		}
	}()
}

func (s *TxSender) Send(to common.Address, value *big.Int, data []byte, gasLimit uint64) (common.Hash, error) {
	gasPrice, err := s.client.SuggestGasPrice(context.Background())
	if err != nil {
		return common.Hash{}, err
	}

	tx := types.NewTransaction(s.state.latestNonce, to, value, gasLimit, gasPrice, data)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(s.chainId), s.pk)
	if err != nil {
		return common.Hash{}, err
	}

	if err := s.client.SendTransaction(context.Background(), signedTx); err != nil {
		return common.Hash{}, err
	}

	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	s.state.broadcastedTxs[signedTx.Hash()] = &broadcastedTx{
		hash: signedTx.Hash(),
		request: txRequest{
			requestId: s.state.numRequests,
			to:        to,
			value:     value,
			data:      data,
			gasLimit:  gasLimit,
			gasPrice:  gasPrice,
			nonce:     s.state.latestNonce,
		},
		broadcastedAt: time.Now(),
	}

	if s.metrics.Enabled {
		s.metrics.RefillTxSent.WithLabelValues(s.chainId.String()).Inc()
	}

	s.state.numRequests++
	s.state.latestNonce++

	return signedTx.Hash(), nil
}

func (s *TxSender) Address() common.Address {
	return crypto.PubkeyToAddress(s.pk.PublicKey)
}
