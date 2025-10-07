package blaster

import (
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

const (
	// Maximum transaction size (100KB is conservative, some nodes accept up to 1MB)
	MaxTxSize = 100000
)

// BuildMassiveSplitTransaction creates a transaction that splits a UTXO into as many outputs as possible
// Returns the transaction and the actual number of value outputs created (excluding OP_RETURN)
func (b *Builder) BuildMassiveSplitTransaction(utxo *models.UTXO, targetOutputs int) (*transaction.Transaction, int, error) {
	if utxo == nil {
		return nil, 0, fmt.Errorf("UTXO is nil")
	}
	if targetOutputs <= 0 {
		return nil, 0, fmt.Errorf("number of outputs must be positive")
	}

	// Calculate the maximum number of outputs that fit in the transaction size limit
	// Each output is approximately 34 bytes, plus we need space for the input and OP_RETURN
	baseSize := BaseTxSize + InputSize + 50 // Include space for OP_RETURN output
	outputSize := OutputSize

	// Calculate maximum outputs that fit in size limit
	maxOutputsBySize := (MaxTxSize - baseSize) / outputSize

	// Use the smaller of target outputs or max possible outputs
	actualOutputs := targetOutputs
	if actualOutputs > maxOutputsBySize {
		actualOutputs = maxOutputsBySize
		// Suppressed: fmt.Printf("Note: Limiting outputs to %d due to transaction size constraints\n", actualOutputs)
	}

	// Calculate fee (1 sat/byte)
	estimatedSize := baseSize + (outputSize * actualOutputs)
	fee := uint64(estimatedSize * FeePerByte)
	if fee < MinimumFee {
		fee = MinimumFee
	}

	// Calculate amount per output
	totalOutputAmount := utxo.Amount - fee
	if totalOutputAmount < uint64(actualOutputs*DustAmount) {
		// If we can't create all outputs at dust limit, reduce the number
		actualOutputs = int(totalOutputAmount / DustAmount)
		if actualOutputs <= 0 {
			return nil, 0, fmt.Errorf("insufficient funds: UTXO has %d sats, cannot create any outputs after %d fee",
				utxo.Amount, fee)
		}
		// Suppressed: fmt.Printf("Note: Reducing outputs to %d due to insufficient funds\n", actualOutputs)

		// Recalculate fee with new output count
		estimatedSize = baseSize + (outputSize * actualOutputs)
		fee = uint64(estimatedSize * FeePerByte)
		if fee < MinimumFee {
			fee = MinimumFee
		}
		totalOutputAmount = utxo.Amount - fee
	}

	amountPerOutput := totalOutputAmount / uint64(actualOutputs)
	if amountPerOutput < DustAmount {
		amountPerOutput = DustAmount
	}

	// Create new transaction
	tx := transaction.NewTransaction()

	// Add input from UTXO
	err := b.addInputFromUTXO(tx, utxo)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to add input: %w", err)
	}

	// FIRST: Add OP_RETURN output with "Who is John Galt?" message (index 0)
	opReturnScript, err := createOpReturnScript("Who is John Galt?")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create OP_RETURN script: %w", err)
	}

	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      0, // OP_RETURN outputs have 0 value
		LockingScript: opReturnScript,
	})

	// Create custom locking script (OP_NOP - hex 0x61)
	lockingScript, _ := script.NewFromHex("61")

	// Add value outputs starting from index 1
	for i := 0; i < actualOutputs; i++ {
		output := &transaction.TransactionOutput{
			Satoshis:      amountPerOutput,
			LockingScript: lockingScript,
		}
		tx.AddOutput(output)
	}

	// Sign the transaction
	err = tx.Sign()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Suppressed transaction details logging for UI compatibility
	_ = tx.TxID() // Keep for side effects if any
	_ = tx.Size() // Keep for side effects if any

	return tx, actualOutputs, nil
}

// Helper functions that need to be exposed for blast_from_tx.go

// AddInputFromUTXO adds an input from a UTXO to the transaction (exposed version)
func (b *Builder) AddInputFromUTXO(tx *transaction.Transaction, utxo *models.UTXO) error {
	return b.addInputFromUTXO(tx, utxo)
}

// CreateNOPLockingScript creates a custom locking script (OP_NOP - hex 0x61)
func (b *Builder) CreateNOPLockingScript(address string) (*script.Script, error) {
	// Return custom locking script instead of P2PKH
	lockingScript, _ := script.NewFromHex("61")
	return lockingScript, nil
}

// CreateOpReturnScript creates an OP_RETURN script with the given data (exposed version)
func CreateOpReturnScript(data string) (*script.Script, error) {
	return createOpReturnScript(data)
}
