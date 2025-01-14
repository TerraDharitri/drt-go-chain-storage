package txcachemocks

import (
	"math/big"

	"github.com/TerraDharitri/drt-go-chain-core/core"
	"github.com/TerraDharitri/drt-go-chain-core/data"
)

// MempoolHostMock -
type MempoolHostMock struct {
	minGasLimit      uint64
	minGasPrice      uint64
	gasPdrTataByte   uint64
	gasPriceModifier float64

	ComputeTxFeeCalled        func(tx data.TransactionWithFeeHandler) *big.Int
	GetTransferredValueCalled func(tx data.TransactionHandler) *big.Int
}

// NewMempoolHostMock -
func NewMempoolHostMock() *MempoolHostMock {
	return &MempoolHostMock{
		minGasLimit:      50000,
		minGasPrice:      1000000000,
		gasPdrTataByte:   1500,
		gasPriceModifier: 0.01,
	}
}

// ComputeTxFee -
func (mock *MempoolHostMock) ComputeTxFee(tx data.TransactionWithFeeHandler) *big.Int {
	if mock.ComputeTxFeeCalled != nil {
		return mock.ComputeTxFeeCalled(tx)
	}

	dataLength := uint64(len(tx.GetData()))
	gasPriceForMovement := tx.GetGasPrice()
	gasPriceForProcessing := uint64(float64(gasPriceForMovement) * mock.gasPriceModifier)

	gasLimitForMovement := mock.minGasLimit + dataLength*mock.gasPdrTataByte
	if tx.GetGasLimit() < gasLimitForMovement {
		panic("tx.GetGasLimit() < gasLimitForMovement")
	}

	gasLimitForProcessing := tx.GetGasLimit() - gasLimitForMovement
	feeForMovement := core.SafeMul(gasPriceForMovement, gasLimitForMovement)
	feeForProcessing := core.SafeMul(gasPriceForProcessing, gasLimitForProcessing)
	fee := big.NewInt(0).Add(feeForMovement, feeForProcessing)
	return fee
}

// GetTransferredValue -
func (mock *MempoolHostMock) GetTransferredValue(tx data.TransactionHandler) *big.Int {
	if mock.GetTransferredValueCalled != nil {
		return mock.GetTransferredValueCalled(tx)
	}

	return tx.GetValue()
}

// IsInterfaceNil -
func (mock *MempoolHostMock) IsInterfaceNil() bool {
	return mock == nil
}
