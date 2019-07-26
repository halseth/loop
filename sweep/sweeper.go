package sweep

import (
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightninglabs/loop/swap"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/sweep"
	"github.com/lightningnetwork/lnd/ticker"
)

type Config struct {
	TxConfTarget int32
	SweeperStore sweep.SweeperStore
}

// Sweeper creates htlc sweep txes.
type Sweeper struct {
	cfg            *Config
	lnd            *lndclient.LndServices
	sendOutputChan chan *sendOutputRequest

	*sweep.UtxoSweeper
}

type sendOutputRequest struct {
	ctx      context.Context
	txOut    *wire.TxOut
	cutoff   time.Time
	respChan chan *wire.MsgTx
	errChan  chan error
}

func New(cfg *Config, lnd *lndclient.LndServices) *Sweeper {
	return &Sweeper{
		cfg:            cfg,
		lnd:            lnd,
		sendOutputChan: make(chan *sendOutputRequest),
		UtxoSweeper: sweep.New(&sweep.UtxoSweeperConfig{
			FeeEstimator: &feeEstimator{lnd},
			GenSweepScript: func() ([]byte, error) {
				return newSweepScript(lnd)
			},
			Signer: &signer{lnd},
			PublishTransaction: func(tx *wire.MsgTx) error {
				// TODO:context
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				return lnd.WalletKit.PublishTransaction(ctx, tx)
			},
			NewBatchTimer: func() <-chan time.Time {
				//return time.NewTimer(sweep.DefaultBatchWindowDuration).C
				return time.NewTimer(100 * time.Millisecond).C
			},
			Notifier: &notifier{lnd},
			GetBestBlock: func() (*chainhash.Hash, int32, error) {
				return nil, 0, nil
			},
			Store:                cfg.SweeperStore,
			MaxInputsPerTx:       sweep.DefaultMaxInputsPerTx,
			MaxSweepAttempts:     sweep.DefaultMaxSweepAttempts,
			NextAttemptDeltaFunc: sweep.DefaultNextAttemptDeltaFunc,
			MaxFeeRate:           sweep.DefaultMaxFeeRate,
			FeeRateBucketSize:    sweep.DefaultFeeRateBucketSize,
		}),
	}
}

func (s *Sweeper) Run(ctx context.Context) {

	fmt.Println("will start sweeper")
	if err := s.Start(); err != nil {
		fmt.Println("error:", err)
		return
	}
	defer s.Stop()

	var sendRequests []*sendOutputRequest

	txPublishTicker := ticker.New(1 * time.Second)
	defer txPublishTicker.Stop()

	for {

		select {
		case req := <-s.sendOutputChan:
			sendRequests = append(sendRequests, req)
			txPublishTicker.Resume()

		case <-txPublishTicker.Ticks():
			fmt.Println("tick")
			// Check if any have had the time cutoff.
			now := time.Now()
			publish := false
			for _, req := range sendRequests {
				if req.cutoff.Before(now) {
					publish = true
				}
			}

			if !publish {
				fmt.Println("not publish")
				continue
			}
			fmt.Println("publish!")

			// Publish what we got so far.
			txOuts := make([]*wire.TxOut, 0, len(sendRequests))
			for _, req := range sendRequests {
				txOuts = append(txOuts, req.txOut)
			}

			tx, err := s.publishTxOuts(txOuts)
			for _, req := range sendRequests {
				if err != nil {
					req.errChan <- err
					continue
				}

				req.respChan <- tx
			}

			sendRequests = nil
			txPublishTicker.Pause()

		case <-ctx.Done():
			return
		}

	}

}

func (s *Sweeper) SendOutputBatched(ctx context.Context, txOut *wire.TxOut,
	timeCutoff time.Time) (*wire.MsgTx, error) {

	respChan := make(chan *wire.MsgTx)
	errChan := make(chan error)

	select {
	case s.sendOutputChan <- &sendOutputRequest{
		ctx:      ctx,
		txOut:    txOut,
		cutoff:   timeCutoff,
		respChan: respChan,
		errChan:  errChan,
	}:
	case <-ctx.Done():
		return nil, fmt.Errorf("Context timeout before outputs added")
	}

	select {
	case tx := <-respChan:
		return tx, nil

	case err := <-errChan:
		return nil, err

		// TODO: how long is this context timeout?
		// Not safe to wait for context?
		//	case <-ctx.Done():
		//		return nil, fmt.Errorf("Context timeout before outputs added")
	}
}

func (s *Sweeper) publishTxOuts(txOut []*wire.TxOut) (*wire.MsgTx, error) {

	// TODO: choose context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get fee estimate from lnd.
	feeRate, err := s.lnd.WalletKit.EstimateFee(ctx, s.cfg.TxConfTarget)
	if err != nil {
		return nil, fmt.Errorf("estimate fee: %v", err)
	}

	tx, err := s.lnd.WalletKit.SendOutputs(
		ctx, txOut, feeRate,
	)
	if err != nil {
		return nil, fmt.Errorf("send outputs: %v", err)
	}

	return tx, nil
}

// CreateSweepTx creates an htlc sweep tx.
func (s *Sweeper) CreateSweepTxOld(
	globalCtx context.Context, height int32,
	htlc *swap.Htlc, htlcOutpoint wire.OutPoint,
	keyBytes [33]byte,
	witnessFunc func(sig []byte) (wire.TxWitness, error),
	amount, fee btcutil.Amount,
	destAddr btcutil.Address) (*wire.MsgTx, error) {

	// Compose tx.
	sweepTx := wire.NewMsgTx(2)

	sweepTx.LockTime = uint32(height)

	// Add HTLC input.
	sweepTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: htlcOutpoint,
		SignatureScript:  htlc.SigScript,
	})

	// Add output for the destination address.
	sweepPkScript, err := txscript.PayToAddrScript(destAddr)
	if err != nil {
		return nil, err
	}

	sweepTx.AddTxOut(&wire.TxOut{
		PkScript: sweepPkScript,
		Value:    int64(amount - fee),
	})

	// Generate a signature for the swap htlc transaction.

	key, err := btcec.ParsePubKey(keyBytes[:], btcec.S256())
	if err != nil {
		return nil, err
	}

	signDesc := input.SignDescriptor{
		WitnessScript: htlc.Script,
		Output: &wire.TxOut{
			Value: int64(amount),
		},
		HashType:   txscript.SigHashAll,
		InputIndex: 0,
		KeyDesc: keychain.KeyDescriptor{
			PubKey: key,
		},
	}

	rawSigs, err := s.lnd.Signer.SignOutputRaw(
		globalCtx, sweepTx, []*input.SignDescriptor{&signDesc},
	)
	if err != nil {
		return nil, fmt.Errorf("signing: %v", err)
	}
	sig := rawSigs[0]

	// Add witness stack to the tx input.
	sweepTx.TxIn[0].Witness, err = witnessFunc(sig)
	if err != nil {
		return nil, err
	}

	return sweepTx, nil
}

// GetSweepFee calculates the required tx fee to spend to P2WKH. It takes a
// function that is expected to add the weight of the input to the weight
// estimator.
func (s *Sweeper) GetSweepFee(ctx context.Context,
	addInputEstimate func(*input.TxWeightEstimator),
	sweepConfTarget int32) (
	btcutil.Amount, error) {

	// Get fee estimate from lnd.
	feeRate, err := s.lnd.WalletKit.EstimateFee(ctx, sweepConfTarget)
	if err != nil {
		return 0, fmt.Errorf("estimate fee: %v", err)
	}

	// Calculate weight for this tx.
	var weightEstimate input.TxWeightEstimator
	weightEstimate.AddP2WKHOutput()
	addInputEstimate(&weightEstimate)
	weight := weightEstimate.Weight()

	return feeRate.FeeForWeight(int64(weight)), nil
}
