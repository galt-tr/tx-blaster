package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/internal/broadcaster"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

type VerifyManager struct {
	keyManager        *keys.KeyManager
	database          *db.Database
	teranodeURL       string
	httpClient        *http.Client
	txStorePath       string
	enableRebroadcast bool
	broadcaster       interface {
		BroadcastTransaction(txHex string) (string, error)
	}
	missingTransactions   map[string]*models.UTXO // Map of txID to UTXO
	missingParents        map[string]bool          // Set of missing parent transaction IDs
	utxosWithMissingParents map[string]*models.UTXO // UTXOs whose parent transactions are missing
	rebroadcastedTxs      int
	rebroadcastFailed     int
}

type UTXOResponse struct {
	Txid          string `json:"txid"`
	Vout          uint32 `json:"vout"`
	LockingScript string `json:"lockingScript"`
	Satoshis      uint64 `json:"satoshis"`
	UtxoHash      string `json:"utxoHash"`
	Status        string `json:"status"`
	SpendingData  *struct {
		SpendingTxID string `json:"spendingTxId"`
	} `json:"spendingData,omitempty"`
	LockTime uint32 `json:"lockTime,omitempty"`
}

func NewVerifyManager(keyManager *keys.KeyManager, database *db.Database, teranodeURL string) *VerifyManager {
	return &VerifyManager{
		keyManager:          keyManager,
		database:            database,
		teranodeURL:         teranodeURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		missingTransactions:     make(map[string]*models.UTXO),
		missingParents:          make(map[string]bool),
		utxosWithMissingParents: make(map[string]*models.UTXO),
	}
}

func NewVerifyManagerWithTxStore(
	keyManager *keys.KeyManager,
	database *db.Database,
	teranodeURL string,
	txStorePath string,
	enableRebroadcast bool,
	propagationGRPC string,
	propagationHTTP string,
	rpcURL string,
	usePropagation bool,
) *VerifyManager {
	vm := &VerifyManager{
		keyManager:          keyManager,
		database:            database,
		teranodeURL:         teranodeURL,
		txStorePath:         txStorePath,
		enableRebroadcast:   enableRebroadcast,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		missingTransactions:     make(map[string]*models.UTXO),
		missingParents:          make(map[string]bool),
		utxosWithMissingParents: make(map[string]*models.UTXO),
	}
	
	// Set up broadcaster if rebroadcast is enabled
	if enableRebroadcast {
		if usePropagation {
			// Use direct batch broadcaster for single transactions
			db, err := broadcaster.NewDirectBatchBroadcaster(propagationGRPC)
			if err != nil {
				fmt.Printf("Warning: Failed to create propagation broadcaster: %v\n", err)
				fmt.Printf("Falling back to RPC broadcaster\n")
				vm.broadcaster = broadcaster.NewBroadcaster(rpcURL)
			} else {
				// Wrap the batch broadcaster to implement simple broadcast interface
				vm.broadcaster = &broadcaster.BatchBroadcasterWrapper{DB: db}
			}
		} else {
			vm.broadcaster = broadcaster.NewBroadcaster(rpcURL)
		}
	}
	
	return vm
}

func (vm *VerifyManager) VerifyUTXOs() error {
	fmt.Println("=== UTXO VERIFICATION ===")
	fmt.Printf("Verifying UTXOs for address: %s\n", vm.keyManager.GetAddress())
	fmt.Printf("Against teranode at: %s\n", vm.teranodeURL)
	fmt.Printf("Checking parent transactions: ENABLED\n")
	fmt.Println()

	// Get all UTXOs from database (including spent ones for verification)
	utxos, err := vm.database.GetUTXOsByAddress(vm.keyManager.GetAddress(), true)
	if err != nil {
		return fmt.Errorf("failed to get UTXOs from database: %w", err)
	}

	fmt.Printf("Found %d UTXOs in database to verify\n", len(utxos))
	fmt.Println()

	var (
		validUnspent   int
		validSpent     int
		invalidUnspent int
		invalidSpent   int
		errors         int
		toMarkSpent    []*models.UTXO
		toMarkUnspent  []*models.UTXO
		toDelete       []*models.UTXO
	)

	// Check each UTXO
	for i, utxo := range utxos {
		if i%100 == 0 && i > 0 {
			fmt.Printf("Progress: %d/%d UTXOs verified\n", i, len(utxos))
		}

		// Check if UTXO exists and is spendable
		isSpent, belongsToUs, err := vm.checkUTXO(utxo)
		if err != nil {
			fmt.Printf("❌ Error checking UTXO %s:%d: %v\n", utxo.TxHash[:8], utxo.Vout, err)
			errors++
			continue
		}
		
		// Check if the parent transaction (the UTXO's transaction) exists on the network
		// This is important because if parent doesn't exist, the UTXO can't be spent
		if !utxo.Spent { // Only check parent for unspent UTXOs
			parentExists, err := vm.checkTransactionExists(utxo.TxHash)
			if err != nil {
				fmt.Printf("⚠️  Error checking parent transaction %s: %v\n", utxo.TxHash[:8], err)
			} else if !parentExists {
				// Parent transaction is missing - this UTXO cannot be spent!
				fmt.Printf("🔴 UTXO %s:%d has missing parent transaction\n", utxo.TxHash[:8], utxo.Vout)
				vm.missingParents[utxo.TxHash] = true
				vm.utxosWithMissingParents[fmt.Sprintf("%s:%d", utxo.TxHash, utxo.Vout)] = utxo
				// Mark as spent since it can't be used
				isSpent = true
			}
		}

		// Verify ownership
		if !belongsToUs {
			fmt.Printf("⚠️  UTXO %s:%d does not belong to our address\n", utxo.TxHash[:8], utxo.Vout)
			toDelete = append(toDelete, utxo)
			continue
		}

		// Compare with database status
		if utxo.Spent && isSpent {
			// Correctly marked as spent
			validSpent++
		} else if !utxo.Spent && !isSpent {
			// Correctly marked as unspent
			validUnspent++
		} else if utxo.Spent && !isSpent {
			// Database says spent, but teranode says unspent
			fmt.Printf("🔄 UTXO %s:%d marked as spent but is unspent on chain\n", utxo.TxHash[:8], utxo.Vout)
			toMarkUnspent = append(toMarkUnspent, utxo)
			invalidSpent++
		} else if !utxo.Spent && isSpent {
			// Database says unspent, but teranode says spent
			fmt.Printf("🔄 UTXO %s:%d marked as unspent but is spent on chain\n", utxo.TxHash[:8], utxo.Vout)
			toMarkSpent = append(toMarkSpent, utxo)
			invalidUnspent++
		}
	}

	// Print initial summary
	fmt.Println()
	fmt.Println("=== INITIAL VERIFICATION RESULTS ===")
	fmt.Printf("✅ Valid unspent: %d\n", validUnspent)
	fmt.Printf("✅ Valid spent: %d\n", validSpent)
	fmt.Printf("❌ Invalid unspent (actually spent): %d\n", invalidUnspent)
	fmt.Printf("❌ Invalid spent (actually unspent): %d\n", invalidSpent)
	fmt.Printf("❌ Errors: %d\n", errors)
	fmt.Printf("🗑️  To delete (wrong address): %d\n", len(toDelete))
	fmt.Printf("🔍 Missing transactions: %d\n", len(vm.missingTransactions))
	fmt.Printf("🔴 Missing parent transactions: %d (affecting %d UTXOs)\n", 
		len(vm.missingParents), len(vm.utxosWithMissingParents))
	fmt.Println()
	
	// Attempt to rebroadcast missing transactions if enabled
	if vm.enableRebroadcast && (len(vm.missingTransactions) > 0 || len(vm.missingParents) > 0) {
		// First rebroadcast parent transactions
		if len(vm.missingParents) > 0 {
			if err := vm.rebroadcastMissingParents(); err != nil {
				fmt.Printf("Warning: Failed to rebroadcast parent transactions: %v\n", err)
			}
		}
		
		// Then rebroadcast regular missing transactions
		if len(vm.missingTransactions) > 0 {
			if err := vm.rebroadcastMissingTransactions(); err != nil {
				fmt.Printf("Warning: Failed to rebroadcast transactions: %v\n", err)
			}
		}
	} else if !vm.enableRebroadcast {
		if len(vm.missingTransactions) > 0 || len(vm.missingParents) > 0 {
			fmt.Printf("Note: Found %d missing transactions and %d missing parent transactions.\n", 
				len(vm.missingTransactions), len(vm.missingParents))
			fmt.Printf("Use --rebroadcast to attempt recovery from txstore\n")
			
			if len(vm.missingTransactions) <= 5 {
				fmt.Println("Missing transaction IDs:")
				for txID := range vm.missingTransactions {
					fmt.Printf("  - %s\n", txID)
				}
			}
			
			if len(vm.missingParents) <= 5 {
				fmt.Println("Missing parent transaction IDs:")
				for txID := range vm.missingParents {
					fmt.Printf("  - %s (parent)\n", txID)
				}
			}
		}
	}

	// Apply fixes if needed
	if len(toMarkSpent) > 0 || len(toMarkUnspent) > 0 || len(toDelete) > 0 {
		fmt.Println("Applying fixes to database...")

		// Mark UTXOs as spent
		for _, utxo := range toMarkSpent {
			err := vm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			if err != nil {
				fmt.Printf("Failed to mark UTXO %s:%d as spent: %v\n", utxo.TxHash[:8], utxo.Vout, err)
			}
		}

		// Mark UTXOs as unspent (update spent flag to false)
		for _, utxo := range toMarkUnspent {
			utxo.Spent = false
			err := vm.database.SaveUTXO(utxo)
			if err != nil {
				fmt.Printf("Failed to mark UTXO %s:%d as unspent: %v\n", utxo.TxHash[:8], utxo.Vout, err)
			}
		}

		// Delete UTXOs that don't belong to us
		for _, utxo := range toDelete {
			err := vm.database.DeleteUTXO(utxo.TxHash, utxo.Vout)
			if err != nil {
				fmt.Printf("Failed to delete UTXO %s:%d: %v\n", utxo.TxHash[:8], utxo.Vout, err)
			}
		}

		fmt.Printf("✅ Fixed %d UTXOs\n", len(toMarkSpent)+len(toMarkUnspent)+len(toDelete))
	} else {
		fmt.Println("✅ No fixes needed - database is in sync!")
	}

	// Show final summary if we did rebroadcasting
	if vm.rebroadcastedTxs > 0 || vm.rebroadcastFailed > 0 {
		fmt.Println()
		fmt.Println("=== FINAL SUMMARY ===")
		fmt.Printf("🔄 Rebroadcasted: %d\n", vm.rebroadcastedTxs)
		fmt.Printf("❌ Rebroadcast failed: %d\n", vm.rebroadcastFailed)
		if len(vm.missingTransactions) > 0 {
			fmt.Printf("⚠️  Still missing transactions: %d\n", len(vm.missingTransactions))
		}
		if len(vm.missingParents) > 0 {
			fmt.Printf("🔴 Still missing parent transactions: %d\n", len(vm.missingParents))
			fmt.Printf("⚠️  UTXOs affected by missing parents: %d (cannot be spent)\n", len(vm.utxosWithMissingParents))
		}
	}
	
	// Show final balance
	balance, err := vm.database.GetTotalBalance(vm.keyManager.GetAddress())
	if err != nil {
		return fmt.Errorf("failed to get balance: %w", err)
	}

	fmt.Println()
	fmt.Printf("Final spendable balance: %d satoshis (%.8f BSV)\n", balance, float64(balance)/1e8)

	return nil
}

func (vm *VerifyManager) checkUTXO(utxo *models.UTXO) (isSpent bool, belongsToUs bool, err error) {
	// Ensure the transaction hash is 64 characters (pad with leading zeros if needed)
	txHash := utxo.TxHash
	if len(txHash) == 63 {
		// Missing one character, likely a leading zero
		txHash = "0" + txHash
	} else if len(txHash) < 64 {
		// Pad with leading zeros to make it 64 characters
		txHash = fmt.Sprintf("%064s", txHash)
	}
	
	// Use the /api/v1/utxos/:txid/json endpoint to get all UTXOs for this transaction
	url := fmt.Sprintf("%s/api/v1/utxos/%s/json", vm.teranodeURL, txHash)
	
	resp, err := vm.httpClient.Get(url)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Transaction not found - track it as missing
		vm.missingTransactions[utxo.TxHash] = utxo
		// For now, treat as spent to avoid trying to use it
		return true, false, nil
	} else if resp.StatusCode == http.StatusInternalServerError {
		// Check if this is a STORAGE_ERROR which means the tx doesn't exist in aerospike
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if strings.Contains(bodyStr, "STORAGE_ERROR") || 
		   strings.Contains(bodyStr, "no such file or directory") ||
		   strings.Contains(bodyStr, "failed to read data from file") {
			// Transaction file missing in aerospike - track it as missing
			fmt.Printf("📁 Transaction %s has STORAGE_ERROR (missing in aerospike)\n", utxo.TxHash[:8])
			vm.missingTransactions[utxo.TxHash] = utxo
			// Treat as spent to avoid trying to use it
			return true, false, nil
		}
		// Other 500 errors
		return false, false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, bodyStr)
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var utxoResponses []UTXOResponse
	if err := json.NewDecoder(resp.Body).Decode(&utxoResponses); err != nil {
		return false, false, err
	}
	
	// Find our specific UTXO by vout
	for _, utxoResp := range utxoResponses {
		if utxoResp.Vout == utxo.Vout {
			// Check if it's spent
			isSpent = (utxoResp.Status == "SPENT" || utxoResp.SpendingData != nil)
			
			// Verify ownership from the locking script
			belongsToUs, err = vm.verifyOwnershipFromScript(utxoResp.LockingScript)
			return isSpent, belongsToUs, err
		}
	}
	
	// UTXO not found in the transaction outputs
	return true, false, nil
}

func (vm *VerifyManager) verifyOwnershipFromScript(lockingScriptHex string) (bool, error) {
	// Decode the locking script
	scriptBytes, err := hex.DecodeString(lockingScriptHex)
	if err != nil {
		return false, err
	}

	lockingScript := script.Script(scriptBytes)
	
	// Check if it's P2PKH and extract address
	if lockingScript.IsP2PKH() {
		addr, err := lockingScript.Address()
		if err != nil {
			return false, err
		}
		
		// Convert to testnet address if needed
		addressStr := addr.AddressString
		if vm.keyManager.IsTestnet() {
			pkh, _ := lockingScript.PublicKeyHash()
			testAddr, _ := script.NewAddressFromPublicKeyHash(pkh, false)
			if testAddr != nil {
				addressStr = testAddr.AddressString
			}
		}
		
		return addressStr == vm.keyManager.GetAddress(), nil
	}

	// For P2PK scripts
	if lockingScript.IsP2PK() {
		pubKey, err := lockingScript.PubKey()
		if err == nil {
			isMainnet := !vm.keyManager.IsTestnet()
			addr, err := script.NewAddressFromPublicKey(pubKey, isMainnet)
			if err == nil {
				return addr.AddressString == vm.keyManager.GetAddress(), nil
			}
		}
	}

	// Unknown script type or couldn't extract address
	return false, nil
}

// checkTransactionExists verifies if a transaction exists on the network
func (vm *VerifyManager) checkTransactionExists(txID string) (exists bool, err error) {
	// Ensure the transaction hash is 64 characters
	if len(txID) == 63 {
		txID = "0" + txID
	} else if len(txID) < 64 {
		txID = fmt.Sprintf("%064s", txID)
	}
	
	// Use the /api/v1/utxos/:txid/json endpoint to check if transaction exists
	url := fmt.Sprintf("%s/api/v1/utxos/%s/json", vm.teranodeURL, txID)
	
	resp, err := vm.httpClient.Get(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	} else if resp.StatusCode == http.StatusInternalServerError {
		// Check if this is a STORAGE_ERROR
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if strings.Contains(bodyStr, "STORAGE_ERROR") || 
		   strings.Contains(bodyStr, "no such file or directory") ||
		   strings.Contains(bodyStr, "failed to read data from file") {
			// Transaction file missing in aerospike storage
			return false, nil
		}
		// Other 500 errors
		return false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, bodyStr)
	} else if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	
	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// extractParentTransactions parses a transaction hex to find parent transaction IDs
func (vm *VerifyManager) extractParentTransactions(txHex string) ([]string, error) {
	// Decode the transaction hex
	txBytes, err := hex.DecodeString(txHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode transaction hex: %w", err)
	}
	
	// Parse the transaction
	tx, err := transaction.NewTransactionFromBytes(txBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse transaction: %w", err)
	}
	
	// Extract parent transaction IDs from inputs
	parents := make(map[string]bool)
	for _, input := range tx.Inputs {
		if input.SourceTXID != nil {
			// Convert hash to string (need to reverse byte order)
			hashBytes := input.SourceTXID[:]
			// Reverse the bytes for proper txid format
			for i := 0; i < len(hashBytes)/2; i++ {
				j := len(hashBytes) - 1 - i
				hashBytes[i], hashBytes[j] = hashBytes[j], hashBytes[i]
			}
			parentTxID := hex.EncodeToString(hashBytes)
			parents[parentTxID] = true
		}
	}
	
	// Convert map to slice
	result := make([]string, 0, len(parents))
	for parent := range parents {
		result = append(result, parent)
	}
	
	return result, nil
}

// buildTransactionChain recursively builds the complete chain of missing transactions
func (vm *VerifyManager) buildTransactionChain(txID string, chain map[string][]string, visited map[string]bool, depth int) error {
	// Avoid infinite recursion
	if depth > 100 {
		return fmt.Errorf("transaction chain too deep (>100 levels)")
	}
	
	// Skip if already visited
	if visited[txID] {
		return nil
	}
	visited[txID] = true
	
	// Check if transaction exists on network
	exists, err := vm.checkTransactionExists(txID)
	if err != nil {
		return fmt.Errorf("failed to check transaction %s: %w", txID[:8], err)
	}
	
	if exists {
		// Transaction exists on network, no need to trace further
		return nil
	}
	
	// Transaction is missing - try to load it from txstore
	txHex, err := vm.loadTransactionFromTxStore(txID)
	if err != nil {
		// Can't load from txstore, can't trace further
		fmt.Printf("  ⚠️ Cannot load %s from txstore: %v\n", txID[:8], err)
		return nil // Don't fail the entire chain
	}
	
	// Extract parent transactions
	parents, err := vm.extractParentTransactions(txHex)
	if err != nil {
		fmt.Printf("  ⚠️ Cannot parse %s to find parents: %v\n", txID[:8], err)
		return nil
	}
	
	// Store the dependency relationships
	chain[txID] = parents
	
	// Recursively trace each parent
	for _, parent := range parents {
		if err := vm.buildTransactionChain(parent, chain, visited, depth+1); err != nil {
			return err
		}
	}
	
	return nil
}

// topologicalSort sorts transactions in dependency order (parents before children)
func (vm *VerifyManager) topologicalSort(dependencies map[string][]string) ([]string, error) {
	// Build in-degree map
	inDegree := make(map[string]int)
	for tx := range dependencies {
		inDegree[tx] = 0
	}
	
	for _, parents := range dependencies {
		for _, parent := range parents {
			if _, exists := dependencies[parent]; exists {
				inDegree[parent]++
			}
		}
	}
	
	// Find all nodes with no dependencies
	queue := []string{}
	for tx, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, tx)
		}
	}
	
	// Process queue
	var sorted []string
	for len(queue) > 0 {
		// Pop from queue
		tx := queue[0]
		queue = queue[1:]
		sorted = append(sorted, tx)
		
		// Update in-degrees
		for child, parents := range dependencies {
			for _, parent := range parents {
				if parent == tx {
					inDegree[child]--
					if inDegree[child] == 0 {
						queue = append(queue, child)
					}
				}
			}
		}
	}
	
	// Check for cycles
	if len(sorted) != len(dependencies) {
		return nil, fmt.Errorf("circular dependency detected in transaction chain")
	}
	
	// Reverse to get parent-first order
	for i := 0; i < len(sorted)/2; i++ {
		j := len(sorted) - 1 - i
		sorted[i], sorted[j] = sorted[j], sorted[i]
	}
	
	return sorted, nil
}

// loadTransactionFromTxStore loads a raw transaction from the txstore
func (vm *VerifyManager) loadTransactionFromTxStore(txID string) (string, error) {
	if vm.txStorePath == "" {
		return "", fmt.Errorf("txstore path not configured")
	}
	
	// Ensure txID is lowercase and properly formatted
	txID = strings.ToLower(txID)
	if len(txID) != 64 {
		return "", fmt.Errorf("invalid transaction ID length: %d", len(txID))
	}
	
	// Construct file path: {txstore}/{first2chars}/{txid}.tx
	firstTwoChars := txID[:2]
	filePath := filepath.Join(vm.txStorePath, firstTwoChars, txID+".tx")
	
	// Read the file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("transaction not found in txstore: %s", txID)
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

// rebroadcastMissingParents attempts to rebroadcast missing parent transactions from txstore
func (vm *VerifyManager) rebroadcastMissingParents() error {
	if !vm.enableRebroadcast {
		return nil
	}
	
	if len(vm.missingParents) == 0 {
		return nil
	}
	
	fmt.Printf("\n=== REBROADCASTING MISSING PARENT TRANSACTIONS ===\n")
	fmt.Printf("Found %d missing parent transactions affecting %d UTXOs\n", 
		len(vm.missingParents), len(vm.utxosWithMissingParents))
	
	// Build complete transaction chains for all missing parents
	fmt.Printf("\nBuilding transaction dependency chains...\n")
	completeDependencies := make(map[string][]string)
	visited := make(map[string]bool)
	
	for txID := range vm.missingParents {
		fmt.Printf("  Tracing chain for %s...\n", txID[:8])
		if err := vm.buildTransactionChain(txID, completeDependencies, visited, 0); err != nil {
			fmt.Printf("    ⚠️ Error: %v\n", err)
		}
	}
	
	// Add any other missing transactions to the chain
	for txID := range vm.missingTransactions {
		if !visited[txID] {
			fmt.Printf("  Tracing chain for %s...\n", txID[:8])
			if err := vm.buildTransactionChain(txID, completeDependencies, visited, 0); err != nil {
				fmt.Printf("    ⚠️ Error: %v\n", err)
			}
		}
	}
	
	if len(completeDependencies) == 0 {
		fmt.Printf("No transaction chains could be built\n")
		return nil
	}
	
	fmt.Printf("\nFound %d transactions in dependency chain\n", len(completeDependencies))
	
	// Sort transactions in dependency order
	sortedTxs, err := vm.topologicalSort(completeDependencies)
	if err != nil {
		fmt.Printf("❌ Failed to sort transaction chain: %v\n", err)
		return err
	}
	
	fmt.Printf("Sorted %d transactions in dependency order\n\n", len(sortedTxs))
	
	// Rebroadcast transactions in order
	successfulParents := make(map[string]bool)
	parentRebroadcasted := 0
	parentFailed := 0
	
	for i, txID := range sortedTxs {
		fmt.Printf("[%d/%d] Rebroadcasting %s... ", i+1, len(sortedTxs), txID[:8])
		
		// Load transaction from txstore
		txHex, err := vm.loadTransactionFromTxStore(txID)
		if err != nil {
			fmt.Printf("❌ Failed to load: %v\n", err)
			parentFailed++
			continue
		}
		
		// Broadcast the transaction
		if vm.broadcaster == nil {
			fmt.Printf("❌ No broadcaster configured\n")
			parentFailed++
			continue
		}
		
		newTxID, err := vm.broadcaster.BroadcastTransaction(txHex)
		if err != nil {
			// Check if error is because transaction already exists
			if strings.Contains(err.Error(), "BLOB_EXISTS") || 
			   strings.Contains(err.Error(), "already exists") ||
			   strings.Contains(err.Error(), "already in mempool") {
				fmt.Printf("⚠️  Already exists in mempool\n")
				parentRebroadcasted++
				successfulParents[txID] = true
			} else if strings.Contains(err.Error(), "TX_MISSING_PARENT") ||
			          strings.Contains(err.Error(), "missing parent") {
				fmt.Printf("❌ Missing parent (chain incomplete)\n")
				parentFailed++
			} else {
				fmt.Printf("❌ Broadcast failed: %v\n", err)
				parentFailed++
			}
			continue
		}
		
		fmt.Printf("✅ Success! TxID: %s\n", newTxID)
		parentRebroadcasted++
		successfulParents[txID] = true
	}
	
	fmt.Printf("\nParent rebroadcast results: %d successful, %d failed\n", 
		parentRebroadcasted, parentFailed)
	
	// Update statistics
	vm.rebroadcastedTxs += parentRebroadcasted
	vm.rebroadcastFailed += parentFailed
	
	// If we successfully rebroadcasted parent transactions, update affected UTXOs
	if len(successfulParents) > 0 {
		fmt.Printf("\nUpdating UTXOs with recovered parent transactions...\n")
		time.Sleep(2 * time.Second) // Give some time for transactions to propagate
		
		for utxoKey, utxo := range vm.utxosWithMissingParents {
			if successfulParents[utxo.TxHash] {
				// Re-verify this UTXO now that its parent exists
				isSpent, belongsToUs, err := vm.checkUTXO(utxo)
				if err != nil {
					fmt.Printf("Failed to re-verify UTXO %s:%d: %v\n", utxo.TxHash[:8], utxo.Vout, err)
					continue
				}
				
				if !isSpent && belongsToUs {
					// Update UTXO as unspent if it's now available
					utxo.Spent = false
					if err := vm.database.SaveUTXO(utxo); err != nil {
						fmt.Printf("Failed to update UTXO %s:%d: %v\n", utxo.TxHash[:8], utxo.Vout, err)
					} else {
						fmt.Printf("✅ UTXO %s:%d now available (parent recovered)\n", utxo.TxHash[:8], utxo.Vout)
					}
				}
				
				// Remove from missing parents lists
				delete(vm.utxosWithMissingParents, utxoKey)
			}
		}
		
		// Remove successfully rebroadcasted parents from missing list
		for txID := range successfulParents {
			delete(vm.missingParents, txID)
		}
	}
	
	return nil
}

// rebroadcastMissingTransactions attempts to rebroadcast missing transactions from txstore
func (vm *VerifyManager) rebroadcastMissingTransactions() error {
	if !vm.enableRebroadcast {
		return nil
	}
	
	if len(vm.missingTransactions) == 0 {
		return nil
	}
	
	fmt.Printf("\n=== REBROADCASTING MISSING TRANSACTIONS ===\n")
	fmt.Printf("Found %d missing transactions to rebroadcast\n", len(vm.missingTransactions))
	fmt.Printf("Note: These were already handled in parent chain rebuilding if applicable\n\n")
	
	successfulTxs := make(map[string]*models.UTXO)
	
	// Only rebroadcast transactions that weren't already handled in parent chain rebuilding
	for txID, utxo := range vm.missingTransactions {
		// Skip if already handled in parent chain
		if _, handled := vm.missingParents[txID]; handled {
			continue
		}
		
		fmt.Printf("Attempting to rebroadcast %s... ", txID[:8])
		
		// Load transaction from txstore
		txHex, err := vm.loadTransactionFromTxStore(txID)
		if err != nil {
			fmt.Printf("❌ Failed to load: %v\n", err)
			vm.rebroadcastFailed++
			continue
		}
		
		// Broadcast the transaction
		if vm.broadcaster == nil {
			fmt.Printf("❌ No broadcaster configured\n")
			vm.rebroadcastFailed++
			continue
		}
		
		newTxID, err := vm.broadcaster.BroadcastTransaction(txHex)
		if err != nil {
			// Check if error is because transaction already exists
			if strings.Contains(err.Error(), "BLOB_EXISTS") || 
			   strings.Contains(err.Error(), "already exists") ||
			   strings.Contains(err.Error(), "already in mempool") {
				fmt.Printf("⚠️  Already exists in mempool\n")
				vm.rebroadcastedTxs++
				successfulTxs[txID] = utxo
			} else {
				fmt.Printf("❌ Broadcast failed: %v\n", err)
				vm.rebroadcastFailed++
			}
			continue
		}
		
		fmt.Printf("✅ Success! TxID: %s\n", newTxID)
		vm.rebroadcastedTxs++
		successfulTxs[txID] = utxo
	}
	
	// Remove successfully rebroadcasted transactions from missing list
	for txID := range successfulTxs {
		delete(vm.missingTransactions, txID)
	}
	
	fmt.Printf("\nRebroadcast results: %d successful, %d failed\n", 
		vm.rebroadcastedTxs, vm.rebroadcastFailed)
	
	// If we successfully rebroadcasted some transactions, re-verify them
	if len(successfulTxs) > 0 {
		fmt.Printf("\nRe-verifying rebroadcasted transactions...\n")
		time.Sleep(2 * time.Second) // Give some time for transactions to propagate
		
		for txID, utxo := range successfulTxs {
			isSpent, belongsToUs, err := vm.checkUTXO(utxo)
			if err != nil {
				fmt.Printf("Failed to re-verify %s: %v\n", txID[:8], err)
				continue
			}
			
			if !isSpent && belongsToUs {
				// Update UTXO as unspent if it's now available
				utxo.Spent = false
				if err := vm.database.SaveUTXO(utxo); err != nil {
					fmt.Printf("Failed to update UTXO %s:%d: %v\n", txID[:8], utxo.Vout, err)
				} else {
					fmt.Printf("✅ UTXO %s:%d marked as unspent\n", txID[:8], utxo.Vout)
				}
			}
		}
	}
	
	return nil
}

