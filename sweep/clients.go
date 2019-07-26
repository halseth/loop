package sweep

import (
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/sweep"
)

type feeEstimator struct {
	lnd *lndclient.LndServices
}

var _ lnwallet.FeeEstimator = (*feeEstimator)(nil)

func (f *feeEstimator) EstimateFeePerKW(
	numBlocks uint32) (lnwallet.SatPerKWeight, error) {

	// TODO:context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	feeRate, err := f.lnd.WalletKit.EstimateFee(ctx, int32(numBlocks))
	if err != nil {
		return 0, fmt.Errorf("estimate fee: %v", err)
	}

	return feeRate, nil
}

func (f *feeEstimator) RelayFeePerKW() lnwallet.SatPerKWeight {
	return 1e3
}

func (f *feeEstimator) Start() error {
	return nil
}
func (f *feeEstimator) Stop() error {
	return nil
}

type signer struct {
	lnd *lndclient.LndServices
}

var _ input.Signer = (*signer)(nil)

func (m *signer) SignOutputRaw(tx *wire.MsgTx, signDesc *input.SignDescriptor) ([]byte, error) {
	return nil, fmt.Errorf("not impl")
}
func (m *signer) ComputeInputScript(tx *wire.MsgTx, signDesc *input.SignDescriptor) (*input.Script, error) {
	return nil, fmt.Errorf("not impl")
}

type notifier struct {
	lnd *lndclient.LndServices
}

var _ chainntnfs.ChainNotifier = (*notifier)(nil)

func (m *notifier) RegisterConfirmationsNtfn(txid *chainhash.Hash, _ []byte, numConfs,
	heightHint uint32) (*chainntnfs.ConfirmationEvent, error) {
	return nil, fmt.Errorf("not impl")
}

func (m *notifier) RegisterBlockEpochNtfn(
	bestBlock *chainntnfs.BlockEpoch) (*chainntnfs.BlockEpochEvent, error) {

	// TODO:context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	blockEpochChan, blockErrorChan, err :=
		m.lnd.ChainNotifier.RegisterBlockEpochNtfn(ctx)
	if err != nil {
		return nil, err
	}

	epochs := make(chan *chainntnfs.BlockEpoch, 1)
	quit := make(chan struct{})

	go func() {
		for {
			select {
			case h := <-blockEpochChan:
				epochs <- &chainntnfs.BlockEpoch{
					Height: h,
				}

			case <-blockErrorChan:
				return
			case <-quit:
				return
			}

		}

	}()

	return &chainntnfs.BlockEpochEvent{
		Epochs: epochs,
		Cancel: func() {
			close(quit)
			cancel()
		},
	}, nil
}

func (m *notifier) Start() error {
	return nil
}

func (m *notifier) Stop() error {
	return nil
}
func (m *notifier) RegisterSpendNtfn(outpoint *wire.OutPoint, pkScript []byte,
	heightHint uint32) (*chainntnfs.SpendEvent, error) {

	fmt.Println("register lnd spend ntfnt")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	spendChan, _, err := m.lnd.ChainNotifier.RegisterSpendNtfn(
		ctx, outpoint, pkScript, int32(heightHint),
	)
	if err != nil {
		return nil, err
	}
	fmt.Println("got spend chan")

	return &chainntnfs.SpendEvent{
		Spend:  spendChan,
		Cancel: cancel,
	}, nil
}

// TODO: etcd
type sweeperStore struct {
}

var _ sweep.SweeperStore = (*sweeperStore)(nil)

func (s *sweeperStore) IsOurTx(hash chainhash.Hash) (bool, error) {
	return false, fmt.Errorf("not impl")
}

func (s *sweeperStore) NotifyPublishTx(*wire.MsgTx) error {
	return fmt.Errorf("not impl")
}

func (s *sweeperStore) GetLastPublishedTx() (*wire.MsgTx, error) {
	return nil, fmt.Errorf("not impl")
}

func newSweepScript(lnd *lndclient.LndServices) ([]byte, error) {
	// TODO:context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// TODO: resuse unused address
	var err error
	addr, err := lnd.WalletKit.NextAddr(ctx)
	if err != nil {
		return nil, err
	}

	return txscript.PayToAddrScript(addr)
}
