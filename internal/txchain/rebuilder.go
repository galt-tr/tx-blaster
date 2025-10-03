package txchain

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
)

// Rebuilder handles transaction chain rebuilding from txstore
type Rebuilder struct {
	txStorePath string
	teranodeURL string
	httpClient  *http.Client
	broadcaster interface {
		BroadcastTransaction(txHex string) (string, error)
	}
}

// NewRebuilder creates a new transaction chain rebuilder
func NewRebuilder(txStorePath, teranodeURL string, broadcaster interface{ BroadcastTransaction(string) (string, error) }) *Rebuilder {
	return &Rebuilder{
		txStorePath: txStorePath,
		teranodeURL: teranodeURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		broadcaster: broadcaster,
	}
}

// CheckTransactionExists verifies if a transaction exists on the network
func (r *Rebuilder) CheckTransactionExists(txID string) (exists bool, err error) {
	// Ensure the transaction hash is 64 characters
	if len(txID) == 63 {
		txID = "0" + txID
	} else if len(txID) < 64 {
		txID = fmt.Sprintf("%064s", txID)
	}
	
	// Use the /api/v1/utxos/:txid/json endpoint to check if transaction exists
	url := fmt.Sprintf("%s/api/v1/utxos/%s/json", r.teranodeURL, txID)
	
	resp, err := r.httpClient.Get(url)
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

// LoadTransactionFromTxStore loads a raw transaction from the txstore
func (r *Rebuilder) LoadTransactionFromTxStore(txID string) (string, error) {
	if r.txStorePath == "" {
		return "", fmt.Errorf("txstore path not configured")
	}
	
	// Ensure txID is lowercase and properly formatted
	txID = strings.ToLower(txID)
	if len(txID) != 64 {
		return "", fmt.Errorf("invalid transaction ID length: %d", len(txID))
	}
	
	// Construct file path: {txstore}/{first2chars}/{txid}.tx
	firstTwoChars := txID[:2]
	filePath := filepath.Join(r.txStorePath, firstTwoChars, txID+".tx")
	
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

// ExtractParentTransactions parses a transaction hex to find parent transaction IDs
func (r *Rebuilder) ExtractParentTransactions(txHex string) ([]string, error) {
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

// BuildTransactionChain recursively builds the complete chain of missing transactions
func (r *Rebuilder) BuildTransactionChain(txID string, chain map[string][]string, visited map[string]bool, depth int) error {
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
	exists, err := r.CheckTransactionExists(txID)
	if err != nil {
		return fmt.Errorf("failed to check transaction %s: %w", txID[:8], err)
	}
	
	if exists {
		// Transaction exists on network, no need to trace further
		return nil
	}
	
	// Transaction is missing - try to load it from txstore
	txHex, err := r.LoadTransactionFromTxStore(txID)
	if err != nil {
		// Can't load from txstore, can't trace further
		fmt.Printf("  ⚠️ Cannot load %s from txstore: %v\n", txID[:8], err)
		return nil // Don't fail the entire chain
	}
	
	// Extract parent transactions
	parents, err := r.ExtractParentTransactions(txHex)
	if err != nil {
		fmt.Printf("  ⚠️ Cannot parse %s to find parents: %v\n", txID[:8], err)
		return nil
	}
	
	// Store the dependency relationships
	chain[txID] = parents
	
	// Recursively trace each parent
	for _, parent := range parents {
		if err := r.BuildTransactionChain(parent, chain, visited, depth+1); err != nil {
			return err
		}
	}
	
	return nil
}

// TopologicalSort sorts transactions in dependency order (parents before children)
func TopologicalSort(dependencies map[string][]string) ([]string, error) {
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

// RebroadcastChain broadcasts a chain of transactions in dependency order
func (r *Rebuilder) RebroadcastChain(txIDs []string) (successful int, failed int, errors []error) {
	if r.broadcaster == nil {
		return 0, len(txIDs), []error{fmt.Errorf("no broadcaster configured")}
	}
	
	for i, txID := range txIDs {
		fmt.Printf("[%d/%d] Rebroadcasting %s... ", i+1, len(txIDs), txID[:8])
		
		// Load transaction from txstore
		txHex, err := r.LoadTransactionFromTxStore(txID)
		if err != nil {
			fmt.Printf("❌ Failed to load: %v\n", err)
			failed++
			errors = append(errors, fmt.Errorf("%s: %w", txID[:8], err))
			continue
		}
		
		// Broadcast the transaction
		newTxID, err := r.broadcaster.BroadcastTransaction(txHex)
		if err != nil {
			// Check if error is because transaction already exists
			if strings.Contains(err.Error(), "BLOB_EXISTS") || 
			   strings.Contains(err.Error(), "already exists") ||
			   strings.Contains(err.Error(), "already in mempool") {
				fmt.Printf("⚠️  Already exists in mempool\n")
				successful++
			} else if strings.Contains(err.Error(), "TX_MISSING_PARENT") ||
			          strings.Contains(err.Error(), "missing parent") {
				fmt.Printf("❌ Missing parent (chain incomplete)\n")
				failed++
				errors = append(errors, fmt.Errorf("%s: missing parent", txID[:8]))
			} else {
				fmt.Printf("❌ Broadcast failed: %v\n", err)
				failed++
				errors = append(errors, fmt.Errorf("%s: %w", txID[:8], err))
			}
			continue
		}
		
		fmt.Printf("✅ Success! TxID: %s\n", newTxID)
		successful++
	}
	
	return successful, failed, errors
}