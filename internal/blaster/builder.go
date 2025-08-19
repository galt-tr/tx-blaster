package blaster

import (
	"encoding/hex"
	"fmt"
	"log"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

const (
	// Minimum dust amount per output (1 satoshi)
	DustAmount = 1
	// Minimum fee for miners (1 satoshi)
	MinimumFee = 1
	// Estimated fee per byte (can be used for larger transactions)
	FeePerByte = 1
	// Estimated size of each output (34 bytes)
	OutputSize = 34
	// Estimated size of input with signature (148 bytes)
	InputSize = 148
	// Base transaction size (version + locktime + varint counts)
	BaseTxSize = 10
)

type Builder struct {
	keyManager *keys.KeyManager
}

func NewBuilder(keyManager *keys.KeyManager) *Builder {
	return &Builder{
		keyManager: keyManager,
	}
}

// BuildSplitTransaction creates a transaction that splits a single UTXO into many outputs
func (b *Builder) BuildSplitTransaction(utxo *models.UTXO, numOutputs int) (*transaction.Transaction, error) {
	// Validate inputs
	if utxo == nil {
		return nil, fmt.Errorf("UTXO is nil")
	}
	if numOutputs <= 0 {
		return nil, fmt.Errorf("number of outputs must be positive")
	}

	// Calculate fee (1 sat/byte with minimum of 1 satoshi)
	estimatedSize := BaseTxSize + InputSize + (OutputSize * numOutputs)
	fee := uint64(estimatedSize * FeePerByte)
	if fee < MinimumFee {
		fee = MinimumFee
	}
	
	// Calculate amount per output
	totalOutputAmount := utxo.Amount - fee
	if totalOutputAmount < uint64(numOutputs*DustAmount) {
		return nil, fmt.Errorf("insufficient funds: UTXO has %d sats, need at least %d sats for %d outputs plus %d fee",
			utxo.Amount, numOutputs*DustAmount, numOutputs, fee)
	}
	
	amountPerOutput := totalOutputAmount / uint64(numOutputs)
	if amountPerOutput < DustAmount {
		// If amount per output is less than dust, reduce number of outputs
		numOutputs = int(totalOutputAmount / DustAmount)
		amountPerOutput = DustAmount
		log.Printf("Reduced outputs to %d to maintain dust limit", numOutputs)
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
	for i := 0; i < numOutputs; i++ {
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

	// Log transaction details
	log.Printf("Created transaction: %d inputs, %d outputs, %d sats per output, %d total fee",
		len(tx.Inputs), len(tx.Outputs), amountPerOutput, fee)
	log.Printf("Transaction ID: %s", tx.TxID())
	log.Printf("Transaction size: %d bytes", tx.Size())

	return tx, nil
}

func (b *Builder) addInputFromUTXO(tx *transaction.Transaction, utxo *models.UTXO) error {
	// Parse transaction ID
	txID, err := hex.DecodeString(utxo.TxHash)
	if err != nil {
		return fmt.Errorf("failed to decode tx hash: %w", err)
	}
	
	// Reverse byte order (Bitcoin uses little-endian)
	for i, j := 0, len(txID)-1; i < j; i, j = i+1, j-1 {
		txID[i], txID[j] = txID[j], txID[i]
	}

	// Get private key for signing
	privKey := b.keyManager.GetPrivateKey()
	
	// Create unlocking script template
	unlockingTemplate, err := p2pkh.Unlock(privKey, nil)
	if err != nil {
		return fmt.Errorf("failed to create unlocking template: %w", err)
	}

	// Create P2PKH locking script for the input
	address := b.keyManager.GetAddress()
	addr, err := script.NewAddressFromString(address)
	if err != nil {
		return fmt.Errorf("failed to parse address: %w", err)
	}
	
	prevLockingScript, err := p2pkh.Lock(addr)
	if err != nil {
		return fmt.Errorf("failed to create locking script: %w", err)
	}

	// Add input to transaction
	err = tx.AddInputFrom(
		utxo.TxHash,
		utxo.Vout,
		hex.EncodeToString(*prevLockingScript),
		utxo.Amount,
		unlockingTemplate,
	)
	if err != nil {
		return fmt.Errorf("failed to add input: %w", err)
	}

	return nil
}

// GetTransactionHex returns the transaction in hexadecimal format
func GetTransactionHex(tx *transaction.Transaction) (string, error) {
	bytes := tx.Bytes()
	return hex.EncodeToString(bytes), nil
}