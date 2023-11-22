// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package load

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ava-labs/subnet-evm/cmd/simulator/config"
	"github.com/ava-labs/subnet-evm/cmd/simulator/key"
	"github.com/ava-labs/subnet-evm/cmd/simulator/metrics"
	"github.com/ava-labs/subnet-evm/cmd/simulator/txs"
	"github.com/ava-labs/subnet-evm/core/types"
	"github.com/ava-labs/subnet-evm/ethclient"
	"github.com/ava-labs/subnet-evm/params"
	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sync/errgroup"
)

const (
	MetricsEndpoint = "/metrics" // Endpoint for the Prometheus Metrics Server
)

type Loader[T txs.THash] struct {
	clients     []txs.Worker[T]
	txSequences []txs.TxSequence[T]
	batchSize   uint64
	metrics     *metrics.Metrics
}

func New[T txs.THash](
	clients []txs.Worker[T],
	txSequences []txs.TxSequence[T],
	batchSize uint64,
	metrics *metrics.Metrics,
) *Loader[T] {
	return &Loader[T]{
		clients:     clients,
		txSequences: txSequences,
		batchSize:   batchSize,
		metrics:     metrics,
	}
}

func (l *Loader[T]) Execute(ctx context.Context) error {
	log.Info("Constructing tx agents...", "numAgents", len(l.txSequences))
	agents := make([]txs.Agent[T], 0, len(l.txSequences))
	for i := 0; i < len(l.txSequences); i++ {
		agents = append(agents, txs.NewIssueNAgent(l.txSequences[i], l.clients[i], l.batchSize, l.metrics))
	}

	log.Info("Starting tx agents...")
	eg := errgroup.Group{}
	for _, agent := range agents {
		agent := agent
		eg.Go(func() error {
			return agent.Execute(ctx)
		})
	}

	log.Info("Waiting for tx agents...")
	if err := eg.Wait(); err != nil {
		return err
	}
	log.Info("Tx agents completed successfully.")
	return nil
}

// ConfirmReachedTip finds the max height any client has reached and then ensures every client
// reaches at least that height.
//
// This allows the network to continue to roll forward and creates a synchronization point to ensure
// that every client in the loader has reached at least the max height observed of any client at
// the time this function was called.
func (l *Loader[T]) ConfirmReachedTip(ctx context.Context) error {
	maxHeight := uint64(0)
	for i, client := range l.clients {
		latestHeight, err := client.LatestHeight(ctx)
		if err != nil {
			return fmt.Errorf("client %d failed to get latest height: %w", i, err)
		}
		if latestHeight > maxHeight {
			maxHeight = latestHeight
		}
	}

	eg := errgroup.Group{}
	for i, client := range l.clients {
		i := i
		client := client
		eg.Go(func() error {
			for {
				latestHeight, err := client.LatestHeight(ctx)
				if err != nil {
					return fmt.Errorf("failed to get latest height from client %d: %w", i, err)
				}
				if latestHeight >= maxHeight {
					return nil
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("failed to get latest height from client %d: %w", i, ctx.Err())
				case <-time.After(time.Second):
				}
			}
		})
	}

	return eg.Wait()
}

// ExecuteLoader creates txSequences from [config] and has txAgents execute the specified simulation.
func ExecuteLoader(ctx context.Context, config config.Config) error {
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
	}

	// Create buffered sigChan to receive SIGINT notifications
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	// Create context with cancel
	ctx, cancel := context.WithCancel(ctx)

	go func() {
		// Blocks until we receive a SIGINT notification or if parent context is done
		select {
		case <-sigChan:
		case <-ctx.Done():
		}

		// Cancel the child context and end all processes
		cancel()
	}()

	m := metrics.NewDefaultMetrics()
	ms := m.Serve(ctx, strconv.Itoa(int(config.MetricsPort)), MetricsEndpoint)

	// Construct the arguments for the load simulator
	clients := make([]ethclient.Client, 0, len(config.Endpoints))
	for i := 0; i < config.Workers; i++ {
		clientURI := config.Endpoints[i%len(config.Endpoints)]
		client, err := ethclient.Dial(clientURI)
		if err != nil {
			return fmt.Errorf("failed to dial client at %s: %w", clientURI, err)
		}
		clients = append(clients, client)
	}

	keys, err := key.LoadAll(ctx, config.KeyDir)
	if err != nil {
		return err
	}
	// Ensure there are at least [config.Workers] keys and save any newly generated ones.
	if len(keys) < config.Workers {
		for i := 0; len(keys) < config.Workers; i++ {
			newKey, err := key.Generate()
			if err != nil {
				return fmt.Errorf("failed to generate %d new key: %w", i, err)
			}
			if err := newKey.Save(config.KeyDir); err != nil {
				return fmt.Errorf("failed to save %d new key: %w", i, err)
			}
			keys = append(keys, newKey)
		}
	}

	// Each address needs: params.GWei * MaxFeeCap * params.TxGas * TxsPerWorker total wei
	// to fund gas for all of their transactions.
	maxFeeCap := new(big.Int).Mul(big.NewInt(params.GWei), big.NewInt(config.MaxFeeCap))
	minFundsPerAddr := new(big.Int).Mul(maxFeeCap, big.NewInt(int64(config.TxsPerWorker*params.TxGas)))

	log.Info("Distributing funds", "numTxsPerWorker", config.TxsPerWorker, "minFunds", minFundsPerAddr)
	keys, err = DistributeFunds(ctx, clients[0], keys, config.Workers, minFundsPerAddr, m)
	if err != nil {
		return err
	}
	log.Info("Distributed funds successfully")

	pks := make([]*ecdsa.PrivateKey, 0, len(keys))
	senders := make([]common.Address, 0, len(keys))
	for _, key := range keys {
		pks = append(pks, key.PrivKey)
		senders = append(senders, key.Address)
	}

	bigGwei := big.NewInt(params.GWei)
	gasTipCap := new(big.Int).Mul(bigGwei, big.NewInt(config.MaxTipCap))
	gasFeeCap := new(big.Int).Mul(bigGwei, big.NewInt(config.MaxFeeCap))
	client := clients[0]
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch chainID: %w", err)
	}
	signer := types.LatestSignerForChainID(chainID)

	log.Info("Creating transaction sequences...")
	txGenerator := func(key *ecdsa.PrivateKey, nonce uint64) (*types.Transaction, error) {
		addr := ethcrypto.PubkeyToAddress(key.PublicKey)
		return types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     nonce,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Gas:       params.TxGas,
			To:        &addr,
			Data:      nil,
			Value:     common.Big0,
		})
	}

	txSequences, err := txs.GenerateTxSequences(ctx, txGenerator, clients[0], pks, config.TxsPerWorker, false)
	if err != nil {
		return err
	}

	workers := make([]txs.Worker[*types.Transaction], 0, len(clients))
	for i, client := range clients {
		workers = append(workers, NewSingleAddressTxWorker(ctx, client, ethcrypto.PubkeyToAddress(pks[i].PublicKey)))
	}
	loader := New(workers, txSequences, config.BatchSize, m)
	err = loader.Execute(ctx)
	ms.Print() // Print regardless of execution error
	return err
}
