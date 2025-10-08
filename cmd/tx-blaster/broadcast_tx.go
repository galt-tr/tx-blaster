package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/internal/broadcaster"
	"github.com/tx-blaster/tx-blaster/internal/txchain"
)

// BroadcastTxManager handles broadcasting of transactions from txstore
type BroadcastTxManager struct {
	txID              string
	txStorePath       string
	usePropagation    bool
	propagationGRPC   string
	propagationHTTP   string
	rpcURL            string
	debugMode         bool
	rebuildChain      bool
	teranodeURL       string
	broadcaster       interface{ BroadcastTransaction(string) (string, error) }
	txRebuilder       *txchain.Rebuilder
}

// NewBroadcastTxManager creates a new broadcast transaction manager
func NewBroadcastTxManager(
	txID string,
	txStorePath string,
	usePropagation bool,
	propagationGRPC string,
	propagationHTTP string,
	rpcURL string,
	debugMode bool,
	rebuildChain bool,
	teranodeURL string,
) (*BroadcastTxManager, error) {
	
	btm := &BroadcastTxManager{
		txID:            txID,
		txStorePath:     txStorePath,
		usePropagation:  usePropagation,
		propagationGRPC: propagationGRPC,
		propagationHTTP: propagationHTTP,
		rpcURL:          rpcURL,
		debugMode:       debugMode,
		rebuildChain:    rebuildChain,
		teranodeURL:     teranodeURL,
	}
	
	// Set up broadcaster
	if usePropagation {
		db, err := broadcaster.NewDirectBatchBroadcaster(propagationGRPC)
		if err != nil {
			return nil, fmt.Errorf("failed to create propagation broadcaster: %w", err)
		}
		btm.broadcaster = &broadcaster.BatchBroadcasterWrapper{DB: db}
	} else {
		btm.broadcaster = broadcaster.NewBroadcaster(rpcURL)
	}
	
	// Set up chain rebuilder if enabled
	if rebuildChain {
		btm.txRebuilder = txchain.NewRebuilder(txStorePath, teranodeURL, btm.broadcaster)
	}
	
	return btm, nil
}

// loadTransaction loads a transaction from txstore
func (btm *BroadcastTxManager) loadTransaction(txID string) (string, error) {
	// Ensure txID is lowercase and properly formatted
	txID = strings.ToLower(txID)
	if len(txID) != 64 {
		return "", fmt.Errorf("invalid transaction ID length: %d (expected 64)", len(txID))
	}
	
	// Construct file path: {txstore}/{first2chars}/{txid}.tx
	firstTwoChars := txID[:2]
	filePath := fmt.Sprintf("%s/%s/%s.tx", btm.txStorePath, firstTwoChars, txID)
	
	// Read the file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("transaction not found in txstore: %s", filePath)
		}
		return "", fmt.Errorf("failed to read transaction file: %w", err)
	}
	
	// Check for T-1.0 header and skip it (8 bytes: "T-1.0    ")
	if len(data) > 8 && string(data[:5]) == "T-1.0" {
		data = data[8:] // Skip the header
	}
	
	// Convert to hex
	txHex := hex.EncodeToString(data)
	return txHex, nil
}

// parseAndDisplayTransaction parses transaction and displays details
func (btm *BroadcastTxManager) parseAndDisplayTransaction(txHex string) error {
	// Decode the transaction hex
	txBytes, err := hex.DecodeString(txHex)
	if err != nil {
		return fmt.Errorf("failed to decode transaction hex: %w", err)
	}
	
	// Parse the transaction
	tx, err := transaction.NewTransactionFromBytes(txBytes)
	if err != nil {
		return fmt.Errorf("failed to parse transaction: %w", err)
	}
	
	fmt.Println("\n=== TRANSACTION DETAILS ===")
	fmt.Printf("Transaction ID: %s\n", btm.txID)
	fmt.Printf("Version: %d\n", tx.Version)
	fmt.Printf("LockTime: %d\n", tx.LockTime)
	fmt.Printf("Size: %d bytes\n", len(txBytes))
	fmt.Printf("Hex Size: %d characters\n", len(txHex))
	
	// Display inputs
	fmt.Printf("\nInputs (%d):\n", len(tx.Inputs))
	for i, input := range tx.Inputs {
		if input.SourceTXID != nil {
			// Convert hash to string (need to reverse byte order)
			hashBytes := make([]byte, 32)
			copy(hashBytes, input.SourceTXID[:])
			// Reverse the bytes for proper txid format
			for j := 0; j < len(hashBytes)/2; j++ {
				k := len(hashBytes) - 1 - j
				hashBytes[j], hashBytes[k] = hashBytes[k], hashBytes[j]
			}
			parentTxID := hex.EncodeToString(hashBytes)
			fmt.Printf("  [%d] From: %s:%d\n", i, parentTxID, input.SourceTxOutIndex)
			fmt.Printf("      Sequence: %d\n", input.SequenceNumber)
			if input.UnlockingScript != nil {
				fmt.Printf("      UnlockingScript size: %d bytes\n", len(*input.UnlockingScript))
			}
		} else {
			fmt.Printf("  [%d] Coinbase input\n", i)
		}
	}
	
	// Display outputs
	fmt.Printf("\nOutputs (%d):\n", len(tx.Outputs))
	totalOutput := uint64(0)
	for i, output := range tx.Outputs {
		totalOutput += output.Satoshis
		fmt.Printf("  [%d] Value: %d satoshis (%.8f BSV)\n", i, output.Satoshis, float64(output.Satoshis)/1e8)
		if output.LockingScript != nil {
			scriptHex := hex.EncodeToString(*output.LockingScript)
			fmt.Printf("      LockingScript: %s\n", scriptHex[:min(64, len(scriptHex))])
			if len(scriptHex) > 64 {
				fmt.Printf("      ... (%d more bytes)\n", (len(scriptHex)-64)/2)
			}
			
			// Check for OP_RETURN
			if len(*output.LockingScript) > 0 && (*output.LockingScript)[0] == 0x6a {
				fmt.Printf("      Type: OP_RETURN")
				if len(*output.LockingScript) > 2 {
					// Try to display as string if it looks like text
					data := (*output.LockingScript)[2:]
					if isPrintable(data) {
						fmt.Printf(" - Data: \"%s\"", string(data))
					}
				}
				fmt.Println()
			}
		}
	}
	
	fmt.Printf("\nTotal Output: %d satoshis (%.8f BSV)\n", totalOutput, float64(totalOutput)/1e8)
	
	// Calculate estimated fee (this is an estimate since we don't have input values)
	fmt.Printf("\nEstimated Fee: (requires input values to calculate accurately)\n")
	
	fmt.Println("===========================")
	
	return nil
}

// isPrintable checks if bytes are printable ASCII
func isPrintable(data []byte) bool {
	if len(data) == 0 || len(data) > 100 { // Don't try to print very long data
		return false
	}
	for _, b := range data {
		if b < 32 || b > 126 {
			return false
		}
	}
	return true
}


// BroadcastTransaction broadcasts the transaction
func (btm *BroadcastTxManager) BroadcastTransaction() error {
	fmt.Printf("Loading transaction %s from txstore...\n", btm.txID)
	
	// Load the transaction
	txHex, err := btm.loadTransaction(btm.txID)
	if err != nil {
		return fmt.Errorf("failed to load transaction: %w", err)
	}
	
	fmt.Printf("✅ Transaction loaded successfully (%d bytes)\n", len(txHex)/2)
	
	// Display transaction details if debug mode is enabled
	if btm.debugMode {
		if err := btm.parseAndDisplayTransaction(txHex); err != nil {
			fmt.Printf("⚠️  Warning: Failed to parse transaction details: %v\n", err)
		}
		
		// Also display raw hex if requested
		fmt.Println("\n=== RAW TRANSACTION HEX ===")
		// Display in chunks for readability
		for i := 0; i < len(txHex); i += 64 {
			end := i + 64
			if end > len(txHex) {
				end = len(txHex)
			}
			fmt.Println(txHex[i:end])
		}
		fmt.Println("===========================\n")
	}
	
	// Check if we need to rebuild parent chain first
	if btm.rebuildChain && btm.txRebuilder != nil {
		fmt.Println("Checking for missing parent transactions...")
		
		// Extract parent transactions
		parents, err := btm.txRebuilder.ExtractParentTransactions(txHex)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to extract parent transactions: %v\n", err)
		} else if len(parents) > 0 {
			fmt.Printf("Found %d parent transaction(s), checking if they exist...\n", len(parents))
			
			// Check each parent
			missingParents := []string{}
			for _, parent := range parents {
				exists, err := btm.txRebuilder.CheckTransactionExists(parent)
				if err != nil {
					fmt.Printf("⚠️  Error checking parent %s: %v\n", parent[:8], err)
				} else if !exists {
					fmt.Printf("❌ Parent transaction %s is missing\n", parent[:8])
					missingParents = append(missingParents, parent)
				} else {
					fmt.Printf("✅ Parent transaction %s exists\n", parent[:8])
				}
			}
			
			// Rebuild missing parents if any
			if len(missingParents) > 0 {
				fmt.Printf("\n🔄 Rebuilding %d missing parent transaction(s)...\n", len(missingParents))
				
				// Build complete chain for each missing parent
				chain := make(map[string][]string)
				visited := make(map[string]bool)
				
				for _, parent := range missingParents {
					if err := btm.txRebuilder.BuildTransactionChain(parent, chain, visited, 0); err != nil {
						fmt.Printf("⚠️  Error building chain for %s: %v\n", parent[:8], err)
					}
				}
				
				if len(chain) > 0 {
					// Sort and broadcast
					sorted, err := txchain.TopologicalSort(chain)
					if err != nil {
						return fmt.Errorf("failed to sort transaction chain: %w", err)
					}
					
					fmt.Printf("Broadcasting %d transactions in dependency order...\n", len(sorted))
					successful, failed, _ := btm.txRebuilder.RebroadcastChain(sorted)
					
					if failed > 0 {
						fmt.Printf("⚠️  Parent chain rebuilding: %d successful, %d failed\n", successful, failed)
					} else {
						fmt.Printf("✅ Successfully rebuilt parent chain (%d transactions)\n", successful)
					}
					
					// Give some time for propagation
					if successful > 0 {
						fmt.Println("Waiting 2 seconds for parent transactions to propagate...")
						time.Sleep(2 * time.Second)
					}
				}
			}
		}
	}
	
	// Broadcast the transaction
	fmt.Printf("\nBroadcasting transaction %s...\n", btm.txID)
	
	newTxID, err := btm.broadcaster.BroadcastTransaction(txHex)
	if err != nil {
		// Check for specific error types
		errStr := err.Error()
		if strings.Contains(errStr, "BLOB_EXISTS") || 
		   strings.Contains(errStr, "already exists") ||
		   strings.Contains(errStr, "already in mempool") {
			fmt.Printf("⚠️  Transaction already exists in mempool\n")
			return nil
		} else if strings.Contains(errStr, "TX_MISSING_PARENT") ||
		          strings.Contains(errStr, "missing parent") {
			return fmt.Errorf("transaction has missing parent (use --rebuild-chain to attempt recovery): %w", err)
		} else if strings.Contains(errStr, "TX_INVALID") ||
		          strings.Contains(errStr, "ScriptVerifierGoBDK fail") {
			return fmt.Errorf("transaction validation failed: %w", err)
		}
		return fmt.Errorf("broadcast failed: %w", err)
	}
	
	fmt.Printf("✅ Transaction broadcast successfully!\n")
	fmt.Printf("Transaction ID: %s\n", newTxID)
	
	return nil
}

// BroadcastTxFromStore is the main entry point for the broadcast-tx command
func BroadcastTxFromStore(
	txID string,
	txStorePath string,
	usePropagation bool,
	propagationGRPC string,
	propagationHTTP string,
	rpcURL string,
	debugMode bool,
	rebuildChain bool,
	teranodeURL string,
) {
	// Validate transaction ID
	txID = strings.TrimSpace(strings.ToLower(txID))
	if len(txID) != 64 {
		log.Fatalf("Invalid transaction ID: must be 64 characters, got %d", len(txID))
	}
	
	// Validate it's hex
	if _, err := hex.DecodeString(txID); err != nil {
		log.Fatalf("Invalid transaction ID: must be hexadecimal: %v", err)
	}
	
	// Create broadcast manager
	btm, err := NewBroadcastTxManager(
		txID,
		txStorePath,
		usePropagation,
		propagationGRPC,
		propagationHTTP,
		rpcURL,
		debugMode,
		rebuildChain,
		teranodeURL,
	)
	if err != nil {
		log.Fatalf("Failed to create broadcast manager: %v", err)
	}
	
	// Broadcast the transaction
	if err := btm.BroadcastTransaction(); err != nil {
		log.Fatalf("Failed to broadcast transaction: %v", err)
	}
}