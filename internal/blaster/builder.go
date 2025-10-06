package blaster

import (
	"encoding/hex"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

const (
	// Minimum dust amount per output (546 satoshis is standard for BSV)
	DustAmount = 546
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

// createOpReturnScript creates an OP_RETURN script with the given data
func createOpReturnScript(data string) (*script.Script, error) {
	// Create a new script starting with OP_RETURN (0x6a)
	s := &script.Script{}
	
	// Add OP_RETURN opcode
	*s = append(*s, 0x6a) // OP_RETURN
	
	// Add data push
	dataBytes := []byte(data)
	if len(dataBytes) <= 75 {
		// For data up to 75 bytes, use direct push
		*s = append(*s, byte(len(dataBytes)))
		*s = append(*s, dataBytes...)
	} else {
		return nil, fmt.Errorf("data too long for OP_RETURN: %d bytes", len(dataBytes))
	}
	
	return s, nil
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
		// Suppressed: log.Printf("Reduced outputs to %d to maintain dust limit", numOutputs)
	}

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

	// Add value outputs starting from index 1
	for i := 0; i < numOutputs; i++ {
		output := &transaction.TransactionOutput{
			Satoshis:      amountPerOutput,
			LockingScript: lockingScript,
		}
		tx.AddOutput(output)
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

func (b *Builder) addInputFromUTXO(tx *transaction.Transaction, utxo *models.UTXO) error {
	// Ensure transaction hash is 64 characters
	txHash := utxo.TxHash
	if len(txHash) == 63 {
		// Missing one character, likely a leading zero
		txHash = "0" + txHash
	} else if len(txHash) < 64 {
		return fmt.Errorf("invalid transaction hash length: got %d, expected 64 for hash %s", len(txHash), txHash)
	}

	// Parse transaction ID
	txID, err := hex.DecodeString(txHash)
	if err != nil {
		return fmt.Errorf("failed to decode tx hash: %w", err)
	}

	// Reverse byte order (Bitcoin uses little-endian)
	for i, j := 0, len(txID)-1; i < j; i, j = i+1, j-1 {
		txID[i], txID[j] = txID[j], txID[i]
	}

	// Get private key for signing
	privKey := b.keyManager.GetPrivateKey()

	// Debug: Log private key details
	// Skip for now as method names are unclear

	// Try to get the actual locking script from the UTXO if available
	var prevLockingScript *script.Script

	// If we have the locking script stored (future enhancement), use it
	// Otherwise, create a standard P2PKH locking script based on our address
	address := b.keyManager.GetAddress()
	addr, err := script.NewAddressFromString(address)
	if err != nil {
		return fmt.Errorf("failed to parse address: %w", err)
	}

	prevLockingScript, err = p2pkh.Lock(addr)
	if err != nil {
		return fmt.Errorf("failed to create locking script: %w", err)
	}

	// Suppressed debug logging for UI compatibility

	// Create unlocking template with the private key
	unlockingTemplate, err := p2pkh.Unlock(privKey, nil)
	if err != nil {
		return fmt.Errorf("failed to create unlocking template: %w", err)
	}

	// Add input to transaction
	err = tx.AddInputFrom(
		txHash,
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