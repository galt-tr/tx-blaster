package blaster

import (
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

// BuildSimpleTransaction creates a transaction that sends most of the input to one output
// and only 1 satoshi to another output (for maximum transaction generation)
func (b *Builder) BuildSimpleTransaction(utxo *models.UTXO) (*transaction.Transaction, error) {
	// Validate inputs
	if utxo == nil {
		return nil, fmt.Errorf("UTXO is nil")
	}

	// Calculate fee (minimal - just 200 bytes estimated)
	estimatedSize := 200
	fee := uint64(estimatedSize * FeePerByte)
	if fee < MinimumFee {
		fee = MinimumFee
	}

	// Calculate output amounts
	if utxo.Amount <= fee+2 {
		return nil, fmt.Errorf("insufficient funds: UTXO has %d sats, need at least %d sats for fee and 2 outputs",
			utxo.Amount, fee+2)
	}

	mainOutput := utxo.Amount - fee - 1 // Keep most of the value
	dustOutput := uint64(1)             // Send 1 satoshi to create a new UTXO

	// Create new transaction
	tx := transaction.NewTransaction()

	// Add input from UTXO
	err := b.addInputFromUTXO(tx, utxo)
	if err != nil {
		return nil, fmt.Errorf("failed to add input: %w", err)
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

	// SECOND: Add main output (most of the value) at index 1
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      mainOutput,
		LockingScript: lockingScript,
	})

	// THIRD: Add dust output (1 satoshi) at index 2
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      dustOutput,
		LockingScript: lockingScript,
	})

	// Sign the transaction
	err = tx.Sign()
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Debug: Check if the transaction was properly signed
	if len(tx.Inputs) > 0 {
		unlockingScriptLen := len(*tx.Inputs[0].UnlockingScript)
		if unlockingScriptLen == 0 {
			return nil, fmt.Errorf("transaction not properly signed - unlocking script is empty")
		}
		// Suppressed debug logging for UI compatibility
		_ = tx.TxID() // Keep for side effects if any
	}

	return tx, nil
}

// BuildManyTransactions creates many simple chained transactions from a single UTXO
// Each transaction spends the "change" from the previous one
func (b *Builder) BuildManyTransactions(utxo *models.UTXO, count int) ([]*transaction.Transaction, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be positive")
	}

	transactions := make([]*transaction.Transaction, 0, count)
	currentUTXO := utxo

	for i := 0; i < count; i++ {
		// Build a simple transaction
		tx, err := b.BuildSimpleTransaction(currentUTXO)
		if err != nil {
			// If we can't create more transactions, return what we have
			if i > 0 {
				return transactions, nil
			}
			return nil, err
		}

		transactions = append(transactions, tx)

		// Create a "virtual" UTXO from the second output (index 1) for the next iteration
		// Index 0 is OP_RETURN, index 1 has the main value
		if i < count-1 {
			currentUTXO = &models.UTXO{
				TxHash:      tx.TxID().String(),
				Vout:        1,                      // Second output (index 1) has most of the value
				Amount:      tx.Outputs[1].Satoshis, // Index 1 is the main output
				BlockHeight: currentUTXO.BlockHeight,
				Address:     currentUTXO.Address,
				Spent:       false,
				IsCoinbase:  false,
			}
		}
	}

	return transactions, nil
}
