package batcher

import (
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/ticker"
)

const (
	// maxBatchSize is the maximum number of outputs we'll create in each
	// batch.
	maxBatchSize = 50

	txPublishTickerInterval = 1 * time.Second
)

type sendOutputRequest struct {
	ctx      context.Context
	txOut    *wire.TxOut
	cutoff   time.Time
	respChan chan *wire.MsgTx
	errChan  chan error
}

// Config specifies the various configuration options for the Batcher.
type Config struct {
	// TxConfTarget is the target confirmation target used for fee
	// estimation.
	// TODO: make per request target?
	TxConfTarget int32
}

// Batcher is used to create on-chain outputs in batch, thereby reducing fees.
// TODO: handle wallet empty.
type Batcher struct {
	cfg            *Config
	lnd            *lndclient.LndServices
	sendOutputChan chan *sendOutputRequest
}

// New initiates a new Batcher with the given config.
func New(cfg *Config, lnd *lndclient.LndServices) *Batcher {
	return &Batcher{
		cfg:            cfg,
		lnd:            lnd,
		sendOutputChan: make(chan *sendOutputRequest),
	}
}

// Run is the main Batcher routine, responsible for handling incoming send
// requests, collecting them into batches and broadcast. It will run until the
// passed context is done.
func (s *Batcher) Run(ctx context.Context) {

	// sendRequests contain the current set of pending sends we want to batch.
	var sendRequests []*sendOutputRequest

	// txPublishTicker represents the time between each time we'll check
	// whether it is time to batch and send the current set of send
	// requests.
	txPublishTicker := ticker.New(txPublishTickerInterval)
	defer txPublishTicker.Stop()

	for {

		select {
		// A new send is being requested. Add it to rou set of requests
		// and start the ticker.
		case req := <-s.sendOutputChan:
			// TODO: add to existing mempool tx by RBF.
			sendRequests = append(sendRequests, req)
			txPublishTicker.Resume()

		// The ticker ticks, so we'll check whether it is time to
		// publish a new batch.
		case <-txPublishTicker.Ticks():
			// Check if any of the send requests have reached the time cutoff.
			now := time.Now()
			publish := false
			for _, req := range sendRequests {
				if req.cutoff.Before(now) {
					publish = true
				}
			}

			if !publish {
				continue
			}

			// At least one of the send requests have reached its
			// cutoff time. We'll batch everything we have up to
			// this point, and publish a tx.
			var txOuts []*wire.TxOut
			for i := range sendRequests {
				// If we reach the maximum batch size, we'll
				// break here. The rest will be send in the
				// next batch.
				if i == maxBatchSize {
					break
				}

				// TODO: check ctx.Done() before publishing?
				txOuts = append(txOuts, sendRequests[i].txOut)
			}

			// Create and publish a transaction paying to the
			// gathered outputs.
			tx, err := s.publishTxOuts(ctx, txOuts)

			// Return the result on the response channel found in
			// the requests we just fulfilled.
			for i := range txOuts {
				if err != nil {
					sendRequests[i].errChan <- err
					continue
				}

				sendRequests[i].respChan <- tx
			}

			// Remove the fulfilled requests from our set.
			sendRequests = sendRequests[len(txOuts):]

			// If the pending send requests are now empty, we can
			// stop the ticker. It will be resumed when a new
			// request comes in.
			if len(sendRequests) == 0 {
				txPublishTicker.Pause()
			}

		case <-ctx.Done():
			return
		}
	}
}

// SendOutputBatcher adds the given txOut to the next batch of outputs to send
// to. The timeCutoff should be set to the latest time the txOut should be part
// of a batch.
func (s *Batcher) SendOutputBatched(ctx context.Context, txOut *wire.TxOut,
	timeCutoff time.Time) (*wire.MsgTx, error) {

	respChan := make(chan *wire.MsgTx)
	errChan := make(chan error)

	select {

	// When the request is picked up by the batcher, it will be added to
	// the next batch out outputs.
	case s.sendOutputChan <- &sendOutputRequest{
		ctx:      ctx,
		txOut:    txOut,
		cutoff:   timeCutoff,
		respChan: respChan,
		errChan:  errChan,
	}:
	case <-ctx.Done():
		return nil, fmt.Errorf("context timeout before output picked " +
			"up by batcher")
	}

	select {
	// Now that the batcher has picked up the request, we have to wait for
	// a result to be available.
	case tx := <-respChan:
		return tx, nil

	case err := <-errChan:
		return nil, err
	}
}

// publishTxOuts creates and sends a transaction to the given outputs.
func (s *Batcher) publishTxOuts(ctx context.Context, txOut []*wire.TxOut) (
	*wire.MsgTx, error) {

	// Get fee estimate from lnd.
	// TODO: combine per-request conf targets?
	feeRate, err := s.lnd.WalletKit.EstimateFee(ctx, s.cfg.TxConfTarget)
	if err != nil {
		return nil, err
	}

	return s.lnd.WalletKit.SendOutputs(ctx, txOut, feeRate)
}
