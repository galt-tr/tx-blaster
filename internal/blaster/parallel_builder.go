package blaster

import (
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

// BuildSplitForParallelBlast creates a transaction with many outputs that can be spent independently.
// This is the first step in the two-phase blasting approach:
// 1. Create one transaction with 1000 outputs
// 2. Create 1000 independent transactions, each spending one output
func (b *Builder) BuildSplitForParallelBlast(utxo *models.UTXO, numOutputs int) (*transaction.Transaction, error) {
	// Validate inputs
	if utxo == nil {
		return nil, fmt.Errorf("UTXO is nil")
	}
	if numOutputs <= 0 {
		return nil, fmt.Errorf("numOutputs must be positive")
	}

	// Create new transaction
	tx := transaction.NewTransaction()

	// Add input from UTXO
	err := b.addInputFromUTXO(tx, utxo)
	if err != nil {
		return nil, fmt.Errorf("failed to add input: %w", err)
	}

	// Calculate fee (similar to working BuildSplitTransaction)
	estimatedSize := BaseTxSize + InputSize + (OutputSize * numOutputs) + 50 // Extra for OP_RETURN
	fee := uint64(estimatedSize * FeePerByte)
	if fee < MinimumFee {
		fee = MinimumFee
	}

	// Calculate amount per output
	totalOutputAmount := utxo.Amount - fee

	// Minimum viable dust amount for BSV (546 satoshis is standard)
	minDust := uint64(546)

	// Check if we have enough for the requested outputs
	if totalOutputAmount < uint64(numOutputs)*minDust {
		// Reduce number of outputs to what we can afford
		maxOutputs := int(totalOutputAmount / minDust)
		if maxOutputs < 1 {
			return nil, fmt.Errorf("insufficient funds: UTXO has %d sats, after %d fee only %d sats remain, need at least %d sats per output",
				utxo.Amount, fee, totalOutputAmount, minDust)
		}
		// Suppressed: fmt.Printf("Reducing outputs from %d to %d due to insufficient funds\n", numOutputs, maxOutputs)
		numOutputs = maxOutputs
	}

	amountPerOutput := totalOutputAmount / uint64(numOutputs)
	remainder := totalOutputAmount % uint64(numOutputs)

	// Suppressed debug logging for UI compatibility

	// Ensure outputs meet minimum dust threshold
	if amountPerOutput < minDust {
		return nil, fmt.Errorf("amount per output %d is below minimum dust limit %d", amountPerOutput, minDust)
	}

	// FIRST: Add OP_RETURN output with "Who is John Galt?" message (index 0)
	opReturnScript, err := createOpReturnScript("Who is John Galt?")
	if err != nil {
		return nil, fmt.Errorf("failed to create OP_RETURN script: %w", err)
	}

	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      0, // OP_RETURN outputs have 0 value
		LockingScript: opReturnScript,
	})

	// Create custom locking script (OP_NOP - hex 0x61)
	lockingScript, _ := script.NewFromHex("61")

	// Add value outputs starting from index 1
	for i := 0; i < numOutputs; i++ {
		amount := amountPerOutput
		if i == numOutputs-1 {
			// Add remainder to last output
			amount += remainder
		}

		// Safety check - never add 0 value output
		if amount == 0 {
			return nil, fmt.Errorf("attempted to create 0 value output at index %d", i)
		}

		tx.AddOutput(&transaction.TransactionOutput{
			Satoshis:      amount,
			LockingScript: lockingScript,
		})
	}

	// Sign the transaction
	err = tx.Sign()
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Suppressed transaction logging for UI compatibility
	_ = tx.TxID() // Keep for side effects if any
	_ = tx.Size() // Keep for side effects if any

	return tx, nil
}

// BuildParallelTransactions creates independent transactions from the outputs of a split transaction.
// Each transaction spends exactly one output from the parent transaction.
// This is the second phase of the two-phase approach.
func (b *Builder) BuildParallelTransactions(parentTxID string, parentOutputs []*transaction.TransactionOutput, count int) ([]*transaction.Transaction, error) {
	if count > len(parentOutputs) {
		count = len(parentOutputs)
	}

	transactions := make([]*transaction.Transaction, 0, count)

	// Get address for reference (used for UTXO address field)
	address := b.keyManager.GetAddress()

	// Create custom locking script (OP_NOP - hex 0x61) for outputs
	lockingScript, _ := script.NewFromHex("61")

	for i := 0; i < count; i++ {
		// Skip if this is the OP_RETURN output
		if parentOutputs[i].Satoshis == 0 {
			continue
		}

		// Create a UTXO representing this output from the parent transaction
		utxo := &models.UTXO{
			TxHash:     parentTxID,
			Vout:       uint32(i),
			Amount:     parentOutputs[i].Satoshis,
			Address:    address,
			Spent:      false,
			IsCoinbase: false,
		}

		// Create new transaction
		tx := transaction.NewTransaction()

		// Add input from the parent output
		err := b.addInputFromUTXO(tx, utxo)
		if err != nil {
			return nil, fmt.Errorf("failed to add input for output %d: %w", i, err)
		}

		// Calculate fee
		estimatedSize := 250 // Small transaction with 1 input, 2 outputs
		fee := uint64(estimatedSize * FeePerByte)
		if fee < MinimumFee {
			fee = MinimumFee
		}

		// Check if we have enough for fee
		if utxo.Amount <= fee {
			// Skip outputs that are too small
			continue
		}

		// FIRST: Add OP_RETURN output (index 0)
		opReturnScript, err := createOpReturnScript("Who is John Galt?")
		if err != nil {
			return nil, fmt.Errorf("failed to create OP_RETURN script: %w", err)
		}

		tx.AddOutput(&transaction.TransactionOutput{
			Satoshis:      0,
			LockingScript: opReturnScript,
		})

		// SECOND: Add single output with remaining value (index 1)
		outputAmount := utxo.Amount - fee

		tx.AddOutput(&transaction.TransactionOutput{
			Satoshis:      outputAmount,
			LockingScript: lockingScript,
		})

		// Sign the transaction
		err = tx.Sign()
		if err != nil {
			return nil, fmt.Errorf("failed to sign transaction %d: %w", i, err)
		}

		transactions = append(transactions, tx)
	}

	return transactions, nil
}
