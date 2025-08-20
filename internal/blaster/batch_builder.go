package blaster

import (
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

// BuildBatchTransactions creates a single large split transaction from a UTXO
// that maximizes outputs for network stress testing
func (b *Builder) BuildBatchTransactions(utxo *models.UTXO, maxOutputs int) ([]*transaction.Transaction, error) {
	if maxOutputs <= 0 {
		return nil, fmt.Errorf("maxOutputs must be positive")
	}
	
	// Create one large split transaction with many outputs
	// This avoids the TX_MISSING_PARENT issue entirely
	tx, err := b.BuildOptimizedSplitTransaction(utxo, maxOutputs)
	if err != nil {
		return nil, err
	}
	
	// Return a single transaction in an array
	return []*transaction.Transaction{tx}, nil
}

// BuildOptimizedSplitTransaction creates a transaction optimized for maximum outputs
func (b *Builder) BuildOptimizedSplitTransaction(utxo *models.UTXO, targetOutputs int) (*transaction.Transaction, error) {
	// Validate inputs
	if utxo == nil {
		return nil, fmt.Errorf("UTXO is nil")
	}
	if targetOutputs <= 0 {
		return nil, fmt.Errorf("number of outputs must be positive")
	}

	// Calculate the maximum number of outputs we can create
	// Transaction size limit is ~100KB for most nodes
	maxTxSize := 90000 // Conservative limit in bytes
	
	// Calculate sizes
	baseSize := BaseTxSize + InputSize + 50 // Include OP_RETURN
	outputSize := OutputSize
	
	// Calculate maximum outputs that fit in size limit
	maxOutputsBySize := (maxTxSize - baseSize) / outputSize
	if targetOutputs > maxOutputsBySize {
		targetOutputs = maxOutputsBySize
	}
	
	// Calculate fee (1 sat/byte)
	estimatedSize := baseSize + (outputSize * targetOutputs)
	fee := uint64(estimatedSize * FeePerByte)
	if fee < MinimumFee {
		fee = MinimumFee
	}
	
	// Calculate amount per output
	totalOutputAmount := utxo.Amount - fee
	if totalOutputAmount < uint64(targetOutputs*DustAmount) {
		// Reduce outputs to what we can afford
		targetOutputs = int(totalOutputAmount / DustAmount)
		if targetOutputs <= 0 {
			return nil, fmt.Errorf("insufficient funds: UTXO has %d sats, cannot create any outputs after %d fee", 
				utxo.Amount, fee)
		}
	}
	
	amountPerOutput := totalOutputAmount / uint64(targetOutputs)
	if amountPerOutput < DustAmount {
		amountPerOutput = DustAmount
		targetOutputs = int(totalOutputAmount / DustAmount)
	}

	// Create new transaction
	tx := transaction.NewTransaction()

	// Add input from UTXO
	err := b.addInputFromUTXO(tx, utxo)
	if err != nil {
		return nil, fmt.Errorf("failed to add input: %w", err)
	}

	// Get address for outputs
	address := b.keyManager.GetAddress()
	addr, err := script.NewAddressFromString(address)
	if err != nil {
		return nil, fmt.Errorf("failed to parse address: %w", err)
	}

	// Create P2PKH locking script
	lockingScript, err := p2pkh.Lock(addr)
	if err != nil {
		return nil, fmt.Errorf("failed to create locking script: %w", err)
	}

	// Add outputs
	for i := 0; i < targetOutputs; i++ {
		output := &transaction.TransactionOutput{
			Satoshis:      amountPerOutput,
			LockingScript: lockingScript,
		}
		tx.AddOutput(output)
	}
	
	// Add OP_RETURN output with "Who is John Galt?" message
	opReturnScript, err := createOpReturnScript("Who is John Galt?")
	if err != nil {
		return nil, fmt.Errorf("failed to create OP_RETURN script: %w", err)
	}
	
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      0, // OP_RETURN outputs have 0 value
		LockingScript: opReturnScript,
	})

	// Sign the transaction
	err = tx.Sign()
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	return tx, nil
}