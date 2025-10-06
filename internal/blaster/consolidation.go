package blaster

import (
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

// BuildConsolidationTransaction creates a transaction that combines multiple small UTXOs into one
func (b *Builder) BuildConsolidationTransaction(utxos []*models.UTXO) (*transaction.Transaction, error) {
	if len(utxos) == 0 {
		return nil, fmt.Errorf("no UTXOs provided for consolidation")
	}

	// Create new transaction
	tx := transaction.NewTransaction()

	// Calculate total input amount
	var totalInput uint64
	for _, utxo := range utxos {
		totalInput += utxo.Amount
		
		// Add input from each UTXO
		err := b.addInputFromUTXO(tx, utxo)
		if err != nil {
			return nil, fmt.Errorf("failed to add input from UTXO %s:%d: %w", utxo.TxHash, utxo.Vout, err)
		}
	}

	// Calculate fee (estimate based on transaction size)
	// Each input is ~148 bytes, outputs are ~34 bytes, base is ~10 bytes
	estimatedSize := BaseTxSize + (InputSize * len(utxos)) + (OutputSize * 2) // 2 outputs: OP_RETURN + value
	fee := uint64(estimatedSize * FeePerByte)
	if fee < MinimumFee {
		fee = MinimumFee
	}

	// Check if consolidation is worthwhile
	outputAmount := totalInput - fee
	// Ensure output is well above dust limit (use 1000 sats as minimum for safety)
	minConsolidationOutput := uint64(1000)
	if minConsolidationOutput < DustAmount {
		minConsolidationOutput = DustAmount * 2
	}
	
	if outputAmount < minConsolidationOutput {
		return nil, fmt.Errorf("consolidation not worthwhile: total %d sats - fee %d sats = %d sats (below minimum %d)", 
			totalInput, fee, outputAmount, minConsolidationOutput)
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

	// Get address for output
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

	// SECOND: Add consolidated output (index 1)
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      outputAmount,
		LockingScript: lockingScript,
	})

	// Sign the transaction
	err = tx.Sign()
	if err != nil {
		return nil, fmt.Errorf("failed to sign consolidation transaction: %w", err)
	}

	// Suppressed consolidation logging for UI compatibility
	_ = tx.TxID() // Keep for side effects if any
	_ = tx.Size() // Keep for side effects if any

	return tx, nil
}

// CalculateConsolidationThreshold returns the minimum amount below which UTXOs should be consolidated
func CalculateConsolidationThreshold(numOutputsPerTx int) uint64 {
	// Calculate the cost of a simple transaction (1 input, 2 outputs: value + OP_RETURN)
	simpleTxSize := BaseTxSize + InputSize + (OutputSize * 2)
	simpleTxFee := uint64(simpleTxSize * FeePerByte)
	
	// Add buffer for dust outputs
	return simpleTxFee + (DustAmount * 2)
}