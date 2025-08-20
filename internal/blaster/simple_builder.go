package blaster

import (
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
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
	
	mainOutput := utxo.Amount - fee - 1  // Keep most of the value
	dustOutput := uint64(1)              // Send 1 satoshi to create a new UTXO

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

	// Add main output (most of the value)
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      mainOutput,
		LockingScript: lockingScript,
	})
	
	// Add dust output (1 satoshi)
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      dustOutput,
		LockingScript: lockingScript,
	})
	
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
		
		// Create a "virtual" UTXO from the first output for the next iteration
		if i < count-1 {
			currentUTXO = &models.UTXO{
				TxHash:      tx.TxID().String(),
				Vout:        0, // First output has most of the value
				Amount:      tx.Outputs[0].Satoshis,
				BlockHeight: currentUTXO.BlockHeight,
				Address:     currentUTXO.Address,
				Spent:       false,
				IsCoinbase:  false,
			}
		}
	}
	
	return transactions, nil
}