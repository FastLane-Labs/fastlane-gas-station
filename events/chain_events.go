package events

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
)

type EthClientConnection struct {
	Client     *ethclient.Client
	logsChan   chan *types.Log
	blocksChan chan *types.Header

	url string
}

func NewEthClientConnection(wsUrl string) (*EthClientConnection, error) {
	client, err := ethclient.Dial(wsUrl)
	if err != nil {
		return nil, err
	}

	return &EthClientConnection{
		Client:     client,
		logsChan:   make(chan *types.Log),
		blocksChan: make(chan *types.Header),
		url:        wsUrl,
	}, nil
}

func (c *EthClientConnection) reconnect() {
	minBackoff := 100 * time.Millisecond
	maxBackoff := time.Minute
	backoff := minBackoff

	for {
		time.Sleep(backoff)
		log.Info("ethClient conn: reconnecting...")
		newConn, err := NewEthClientConnection(c.url)
		if err == nil {
			*c.Client = *newConn.Client
			c.Start()
			return
		}

		backoff *= 3
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		log.Error("ethClientConn:", "reconnect failed:", err, "next attempt in", backoff)
	}
}

func (c *EthClientConnection) Start() error {
	log.Info("ethClient conn: starting...")

	// Subscribe to new heads
	headers := make(chan *types.Header)
	headsSub, err := c.Client.SubscribeNewHead(context.Background(), headers)
	if err != nil {
		return err
	}
	log.Info("Subscribed to new heads")

	// Subscribe to new logs
	logs := make(chan types.Log)
	query := ethereum.FilterQuery{
		FromBlock: nil,
		ToBlock:   nil,
		Topics:    [][]common.Hash{{common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")}},
		Addresses: []common.Address{},
	}
	logsSub, err := c.Client.SubscribeFilterLogs(context.Background(), query, logs)
	if err != nil {
		return err
	}
	log.Info("Subscribed to new logs")

	// Start goroutines to handle new heads, logs, and errors
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-headsSub.Err():
				log.Error("Error in new head subscription", "error", err)
				cancel()
				return
			case header := <-headers:
				c.blocksChan <- header
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-logsSub.Err():
				log.Error("Error in new logs subscription", "error", err)
				cancel()
				return
			case log := <-logs:
				c.logsChan <- &log
			}
		}
	}()

	go func() {
		for {
			<-ctx.Done()
			c.Client.Close()
			c.reconnect()
			return
		}
	}()
	return nil
}

func (c *EthClientConnection) BlocksChan() chan *types.Header {
	return c.blocksChan
}

func (c *EthClientConnection) LogsChan() chan *types.Log {
	return c.logsChan
}
