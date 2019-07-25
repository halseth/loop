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
	return 0, fmt.Errorf("exiting")
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
	return nil, fmt.Errorf("not impl")
}

func (m *notifier) Start() error {
	return nil
}

func (m *notifier) Stop() error {
	return nil
}
func (m *notifier) RegisterSpendNtfn(outpoint *wire.OutPoint, _ []byte,
	heightHint uint32) (*chainntnfs.SpendEvent, error) {
	return nil, fmt.Errorf("not impl")
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
