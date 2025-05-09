package txcache

import (
	"math/big"

	"github.com/TerraDharitri/drt-go-chain-core/data"
)

// After moving "drt-go-chain-storage/txcache" into "drt-go-chain", maybe merge this component into "SelectionSession".
type selectionSessionWrapper struct {
	session          SelectionSession
	recordsByAddress map[string]*accountRecord
}

type accountRecord struct {
	initialNonce    uint64
	initialBalance  *big.Int
	consumedBalance *big.Int
}

func newSelectionSessionWrapper(session SelectionSession) *selectionSessionWrapper {
	return &selectionSessionWrapper{
		session:          session,
		recordsByAddress: make(map[string]*accountRecord),
	}
}

func (sessionWrapper *selectionSessionWrapper) getAccountRecord(address []byte) *accountRecord {
	record, ok := sessionWrapper.recordsByAddress[string(address)]
	if ok {
		return record
	}

	state, err := sessionWrapper.session.GetAccountState(address)
	if err != nil {
		logSelect.Debug("selectionSessionWrapper.getAccountRecord, could not retrieve account state", "address", address, "err", err)

		record = &accountRecord{
			initialNonce:    0,
			initialBalance:  big.NewInt(0),
			consumedBalance: big.NewInt(0),
		}
	} else {
		record = &accountRecord{
			initialNonce:    state.Nonce,
			initialBalance:  state.Balance,
			consumedBalance: big.NewInt(0),
		}
	}

	sessionWrapper.recordsByAddress[string(address)] = record
	return record
}

func (sessionWrapper *selectionSessionWrapper) getNonce(address []byte) uint64 {
	return sessionWrapper.getAccountRecord(address).initialNonce
}

func (sessionWrapper *selectionSessionWrapper) accumulateConsumedBalance(tx *WrappedTransaction) {
	sender := tx.Tx.GetSndAddr()
	feePayer := tx.FeePayer

	senderRecord := sessionWrapper.getAccountRecord(sender)
	feePayerRecord := sessionWrapper.getAccountRecord(feePayer)

	transferredValue := tx.TransferredValue
	if transferredValue != nil {
		senderRecord.consumedBalance.Add(senderRecord.consumedBalance, transferredValue)
	}

	fee := tx.Fee
	if fee != nil {
		feePayerRecord.consumedBalance.Add(feePayerRecord.consumedBalance, fee)
	}
}

func (sessionWrapper *selectionSessionWrapper) detectWillFeeExceedBalance(tx *WrappedTransaction) bool {
	fee := tx.Fee
	if fee == nil {
		return false
	}

	// Here, we are not interested into an eventual transfer of value (we only check if there's enough balance to pay the transaction fee).
	feePayer := tx.FeePayer
	feePayerRecord := sessionWrapper.getAccountRecord(feePayer)

	futureConsumedBalance := new(big.Int).Add(feePayerRecord.consumedBalance, fee)
	feePayerBalance := feePayerRecord.initialBalance

	willFeeExceedBalance := futureConsumedBalance.Cmp(feePayerBalance) > 0
	if willFeeExceedBalance {
		logSelect.Trace("selectionSessionWrapper.detectWillFeeExceedBalance",
			"tx", tx.TxHash,
			"feePayer", feePayer,
			"initialBalance", feePayerRecord.initialBalance,
			"consumedBalance", feePayerRecord.consumedBalance,
		)
	}

	return willFeeExceedBalance
}

func (sessionWrapper *selectionSessionWrapper) isIncorrectlyGuarded(tx data.TransactionHandler) bool {
	return sessionWrapper.session.IsIncorrectlyGuarded(tx)
}
