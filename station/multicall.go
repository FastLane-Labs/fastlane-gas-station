package station

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/FastLane-Labs/blockchain-rpc-go/eth"
	"github.com/FastLane-Labs/fastlane-gas-station/contract/multicall"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/log"
)

const (
	aggregateFunction = "aggregate"
	callPerBatch      = 500
	networkTimeout    = 3 * time.Second
)

func Multicall(
	client eth.IEthClient,
	chainId string,
	callDataBatchGeneratorFunc func(index int) ([]multicall.Multicall3Call, error),
	returnDataBatchHandlerFunc func(index int, returnDataAtIndex [][]byte) error,
	indices []int,
	logger log.Logger,
) error {
	batchAtIndex := make(map[int][]multicall.Multicall3Call)
	wg := sync.WaitGroup{}
	mu := sync.Mutex{}
	for _, idx := range indices {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			batch, err := callDataBatchGeneratorFunc(index)
			if err != nil {
				logger.Error("failed to generate call data batch", "chainId", chainId, "index", index, "err", err)
				return
			}

			mu.Lock()
			if _, ok := batchAtIndex[index]; ok {
				logger.Error("duplicate indices received in multicall", "chainId", chainId, "index", index)
				mu.Unlock()
				return
			}
			batchAtIndex[index] = batch
			mu.Unlock()
		}(idx)
	}
	wg.Wait()

	indexReturnDataLocation := make(map[int]struct {
		chunkIndex       int
		fromIndexInChunk int
		toIndexInChunk   int
	})

	// Make chunks of calldata batches such that max(len(chunk)) == numBatchesPerMulticall
	calldataChunks := make([][]multicall.Multicall3Call, 0)
	from := 0
	for {
		if from == len(indices) {
			break
		}
		to := from + callPerBatch
		if to > len(indices) {
			to = len(indices)
		}
		chunk := make([]multicall.Multicall3Call, 0)
		for i := from; i < to; i++ {
			if batch, ok := batchAtIndex[indices[i]]; ok {
				fromIndexInChunk := len(chunk)
				chunk = append(chunk, batch...)
				toIndexInChunk := len(chunk)
				indexReturnDataLocation[indices[i]] = struct {
					chunkIndex       int
					fromIndexInChunk int
					toIndexInChunk   int
				}{
					chunkIndex:       len(calldataChunks),
					fromIndexInChunk: fromIndexInChunk,
					toIndexInChunk:   toIndexInChunk,
				}
			} else {
				return fmt.Errorf("failed to generate call data batch for index %d", indices[i])
			}
		}
		calldataChunks = append(calldataChunks, chunk)
		from = to
	}

	returnDataChunks := make([][][]byte, len(calldataChunks))

	completed := 0
	for j, ch := range calldataChunks {
		wg.Add(1)
		go func(chunkIdx int, calldataChunk []multicall.Multicall3Call) {
			defer wg.Done()
			defer func() {
				completed++
			}()

			returnDataBatch, err := multicall_inner(client, calldataChunk)
			if err != nil {
				returnDataChunks[chunkIdx] = nil
				logger.Error("failed to multicall", "chainId", chainId, "err", err)
				return
			}

			returnDataChunks[chunkIdx] = returnDataBatch
		}(j, ch)
	}
	wg.Wait()

	for chIdx, chunkReturnData := range returnDataChunks {
		if chunkReturnData == nil {
			return fmt.Errorf("failed to multicall at chunk index %d", chIdx)
		}
	}

	for _, idx := range indices {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			loc := indexReturnDataLocation[index]
			if err := returnDataBatchHandlerFunc(index, returnDataChunks[loc.chunkIndex][loc.fromIndexInChunk:loc.toIndexInChunk]); err != nil {
				logger.Error("failed to handle return data", "chainId", chainId, "err", err)
			}
		}(idx)
	}
	wg.Wait()

	return nil
}

func multicall_inner(client eth.IEthClient, multicallData []multicall.Multicall3Call) ([][]byte, error) {
	ethCalldata, err := multicallAbi.Pack(aggregateFunction, multicallData)
	if err != nil {
		return nil, fmt.Errorf("failed to pack multicall data: %v", err)
	}

	ethMsg := ethereum.CallMsg{
		To:   &multicallAddr,
		Data: ethCalldata,
	}

	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()

	resp, err := client.CallContract(ctx, ethMsg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call multicall contract: %v", err)
	}

	res, err := multicallAbi.Unpack(aggregateFunction, resp)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack multicall response: %v", err)
	}

	if len(res) != 2 {
		return nil, fmt.Errorf("unexpected response length: %d", len(res))
	}

	returnData, ok := res[1].([][]byte)
	if !ok {
		return nil, fmt.Errorf("failed to convert return data to [][]byte")
	}

	return returnData, nil
}
