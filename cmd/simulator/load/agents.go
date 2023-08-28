// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package load

import (
	"context"
	"crypto/ecdsa"
	"math/big"

	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/ava-labs/subnet-evm/cmd/simulator/config"
	"github.com/ava-labs/subnet-evm/cmd/simulator/metrics"
	"github.com/ava-labs/subnet-evm/cmd/simulator/txs"
	"github.com/ava-labs/subnet-evm/core/types"
	"github.com/ava-labs/subnet-evm/ethclient"
	"github.com/ava-labs/subnet-evm/params"
	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

type AgentBuilder interface {
	GenerateTxSequences(
		ctx context.Context,
		config config.Config,
		chainID *big.Int,
		pks []*ecdsa.PrivateKey,
		startingNonces []uint64,
	) error

	NewAgent(
		ctx context.Context,
		config config.Config,
		idx int,
		client ethclient.Client,
		sender common.Address,
		m *metrics.Metrics,
	) (txs.Agent, error)
}

type transferTxAgentBuilder struct {
	txSequences []txs.TxSequence[*types.Transaction]
}

func (t *transferTxAgentBuilder) GenerateTxSequences(
	ctx context.Context,
	config config.Config,
	chainID *big.Int,
	pks []*ecdsa.PrivateKey,
	startingNonces []uint64,
) error {
	log.Info("Creating transaction sequences...")
	bigGwei := big.NewInt(params.GWei)
	gasTipCap := new(big.Int).Mul(bigGwei, big.NewInt(config.MaxTipCap))
	gasFeeCap := new(big.Int).Mul(bigGwei, big.NewInt(config.MaxFeeCap))
	signer := types.LatestSignerForChainID(chainID)

	txGenerator := func(key *ecdsa.PrivateKey, nonce uint64) (*types.Transaction, error) {
		addr := ethcrypto.PubkeyToAddress(key.PublicKey)
		tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     nonce,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Gas:       params.TxGas,
			To:        &addr,
			Data:      nil,
			Value:     common.Big0,
		})
		if err != nil {
			return nil, err
		}
		return tx, nil
	}
	txSequences, err := txs.GenerateTxSequences(
		ctx, txGenerator, pks, startingNonces, config.TxsPerWorker)
	if err != nil {
		return err
	}

	t.txSequences = txSequences
	return nil
}

func (t *transferTxAgentBuilder) NewAgent(
	ctx context.Context,
	config config.Config,
	idx int,
	client ethclient.Client,
	sender common.Address,
	m *metrics.Metrics,
) (txs.Agent, error) {
	worker := NewSingleAddressTxWorker(ctx, client, sender)
	return txs.NewIssueNAgent[*types.Transaction](
		t.txSequences[idx], worker, config.BatchSize, m), nil
}

type warpSendTxAgentBuilder struct {
	txSequences []txs.TxSequence[*types.Transaction]
	txTracker   *txTracker
}

func (w *warpSendTxAgentBuilder) GenerateTxSequences(
	ctx context.Context,
	config config.Config,
	chainID *big.Int,
	pks []*ecdsa.PrivateKey,
	startingNonces []uint64,
) error {
	log.Info("Creating warp send transaction sequences...")

	dstBlockchainID := config.Subnets[1].BlockchainID
	txSequences, err := GetWarpSendTxSequences(
		ctx,
		chainID,
		pks,
		startingNonces,
		big.NewInt(config.MaxTipCap),
		big.NewInt(config.MaxFeeCap),
		dstBlockchainID,
		config.TxsPerWorker,
	)

	if err != nil {
		return err
	}

	w.txSequences = txSequences
	return nil
}

func (w *warpSendTxAgentBuilder) NewAgent(
	ctx context.Context,
	config config.Config,
	idx int,
	client ethclient.Client,
	sender common.Address,
	m *metrics.Metrics,
) (txs.Agent, error) {
	worker := NewSingleAddressTxWorker(ctx, client, sender)
	worker.onIssued = w.txTracker.IssueTx
	worker.onClosed = w.txTracker.Close
	return txs.NewIssueNAgent[*types.Transaction](
		w.txSequences[idx], worker, config.BatchSize, m), nil
}

type warpReceiveTxAgentBuilder struct {
	txSequences    []txs.TxSequence[*types.Transaction]
	txTracker      *txTracker
	signedMessages chan *warp.Message
}

func (w *warpReceiveTxAgentBuilder) GenerateTxSequences(
	ctx context.Context,
	config config.Config,
	chainID *big.Int,
	pks []*ecdsa.PrivateKey,
	startingNonces []uint64,
) error {
	log.Info("Creating warp receive transaction sequences...")
	txSequences, err := GetWarpReceiveTxSequences(
		ctx, chainID, pks, startingNonces, w.signedMessages, config.Workers)
	if err != nil {
		return err
	}

	w.txSequences = txSequences
	return nil
}

func (w *warpReceiveTxAgentBuilder) NewAgent(
	ctx context.Context,
	config config.Config,
	idx int,
	client ethclient.Client,
	sender common.Address,
	m *metrics.Metrics,
) (txs.Agent, error) {
	worker := NewSingleAddressTxWorker(ctx, client, sender)
	worker.onConfirmed = w.txTracker.ConfirmTx
	return txs.NewIssueNAgent[*types.Transaction](
		w.txSequences[idx], worker, config.BatchSize, m), nil
}
