package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

type TxStatusChecker struct {
	database    *db.Database
	teranodeURL string
	httpClient  *http.Client
	keyManager  *keys.KeyManager // Optional, for checking if outputs belong to us
}

type TxOutputStatus struct {
	Vout           uint32
	Amount         uint64
	Status         string // UNSPENT, SPENT, UNKNOWN
	SpendingTxID   string
	InLocalDB      bool
	LocalDBStatus  string // spent/unspent
	BelongsToUs    bool
	Inconsistency  string
	LockingScript  string
}

type TxStatusReport struct {
	TxID            string
	Found           bool
	BlockHeight     uint32
	TotalOutputs    int
	OutputStatuses  []TxOutputStatus
	Inconsistencies []string
}

func NewTxStatusChecker(database *db.Database, teranodeURL string, keyManager *keys.KeyManager) *TxStatusChecker {
	return &TxStatusChecker{
		database:    database,
		teranodeURL: teranodeURL,
		keyManager:  keyManager,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (tsc *TxStatusChecker) CheckTransactionStatus(txID string) (*TxStatusReport, error) {
	report := &TxStatusReport{
		TxID:            txID,
		OutputStatuses:  []TxOutputStatus{},
		Inconsistencies: []string{},
	}

	// Ensure the transaction hash is 64 characters
	if len(txID) == 63 {
		txID = "0" + txID
	} else if len(txID) < 64 {
		txID = fmt.Sprintf("%064s", txID)
	}

	// Query aerospike for transaction outputs
	url := fmt.Sprintf("%s/api/v1/utxos/%s/json", tsc.teranodeURL, txID)
	
	resp, err := tsc.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to query teranode: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		report.Found = false
		report.Inconsistencies = append(report.Inconsistencies, 
			"Transaction not found in aerospike")
		
		// Check if we have any UTXOs from this transaction in our database
		if tsc.database != nil {
			localUTXOs := tsc.checkLocalDatabase(txID)
			if len(localUTXOs) > 0 {
				report.Inconsistencies = append(report.Inconsistencies,
					fmt.Sprintf("Found %d UTXOs in local database for non-existent transaction", len(localUTXOs)))
				report.OutputStatuses = localUTXOs
			}
		}
		
		return report, nil
	} else if resp.StatusCode == http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if strings.Contains(bodyStr, "STORAGE_ERROR") || 
		   strings.Contains(bodyStr, "no such file or directory") ||
		   strings.Contains(bodyStr, "failed to read data from file") {
			report.Found = false
			report.Inconsistencies = append(report.Inconsistencies,
				"Transaction has STORAGE_ERROR in aerospike (file missing)")
			
			// Check local database
			if tsc.database != nil {
				localUTXOs := tsc.checkLocalDatabase(txID)
				if len(localUTXOs) > 0 {
					report.Inconsistencies = append(report.Inconsistencies,
						fmt.Sprintf("Found %d UTXOs in local database for transaction with STORAGE_ERROR", len(localUTXOs)))
					report.OutputStatuses = localUTXOs
				}
			}
			
			return report, nil
		}
		return nil, fmt.Errorf("server error: %s", bodyStr)
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var utxoResponses []UTXOResponse
	if err := json.NewDecoder(resp.Body).Decode(&utxoResponses); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	report.Found = true
	report.TotalOutputs = len(utxoResponses)

	// Process each output
	for _, utxo := range utxoResponses {
		outputStatus := TxOutputStatus{
			Vout:          utxo.Vout,
			Amount:        utxo.Satoshis,
			LockingScript: utxo.LockingScript,
		}

		// Check aerospike status
		if utxo.Status == "SPENT" || utxo.SpendingData != nil {
			outputStatus.Status = "SPENT"
			if utxo.SpendingData != nil {
				outputStatus.SpendingTxID = utxo.SpendingData.SpendingTxID
			}
		} else if utxo.Status == "UNSPENT" {
			outputStatus.Status = "UNSPENT"
		} else {
			outputStatus.Status = "UNKNOWN"
		}

		// Check ownership if we have a key manager
		if tsc.keyManager != nil {
			outputStatus.BelongsToUs = tsc.checkOwnership(utxo.LockingScript)
		}

		// Check local database
		if tsc.database != nil && outputStatus.BelongsToUs {
			localUTXO, err := tsc.getLocalUTXO(txID, utxo.Vout)
			if err == nil && localUTXO != nil {
				outputStatus.InLocalDB = true
				if localUTXO.Spent {
					outputStatus.LocalDBStatus = "spent"
				} else {
					outputStatus.LocalDBStatus = "unspent"
				}

				// Check for inconsistencies
				if outputStatus.Status == "SPENT" && !localUTXO.Spent {
					outputStatus.Inconsistency = "Aerospike: SPENT, LocalDB: unspent"
					report.Inconsistencies = append(report.Inconsistencies,
						fmt.Sprintf("Output %d: Aerospike says SPENT, local DB says unspent", utxo.Vout))
				} else if outputStatus.Status == "UNSPENT" && localUTXO.Spent {
					outputStatus.Inconsistency = "Aerospike: UNSPENT, LocalDB: spent"
					report.Inconsistencies = append(report.Inconsistencies,
						fmt.Sprintf("Output %d: Aerospike says UNSPENT, local DB says spent", utxo.Vout))
				}
			} else if outputStatus.BelongsToUs {
				// UTXO belongs to us but not in database
				outputStatus.InLocalDB = false
				if outputStatus.Status == "UNSPENT" {
					outputStatus.Inconsistency = "Missing from local DB (should be tracked)"
					report.Inconsistencies = append(report.Inconsistencies,
						fmt.Sprintf("Output %d: Belongs to us and UNSPENT but missing from local DB", utxo.Vout))
				}
			}
		}

		report.OutputStatuses = append(report.OutputStatuses, outputStatus)
	}

	// Also check if local database has UTXOs that aerospike doesn't know about
	if tsc.database != nil {
		localUTXOs := tsc.checkLocalDatabase(txID)
		for _, localStatus := range localUTXOs {
			found := false
			for _, aerospikeStatus := range report.OutputStatuses {
				if aerospikeStatus.Vout == localStatus.Vout {
					found = true
					break
				}
			}
			if !found {
				localStatus.Inconsistency = "In local DB but not in aerospike response"
				report.OutputStatuses = append(report.OutputStatuses, localStatus)
				report.Inconsistencies = append(report.Inconsistencies,
					fmt.Sprintf("Output %d: In local DB but not in aerospike", localStatus.Vout))
			}
		}
	}

	return report, nil
}

func (tsc *TxStatusChecker) checkOwnership(lockingScriptHex string) bool {
	if tsc.keyManager == nil {
		return false
	}

	scriptBytes, err := hex.DecodeString(lockingScriptHex)
	if err != nil {
		return false
	}

	lockingScript := script.Script(scriptBytes)
	
	// Check if it's P2PKH and extract address
	if lockingScript.IsP2PKH() {
		addr, err := lockingScript.Address()
		if err != nil {
			return false
		}
		
		// Convert to testnet address if needed
		addressStr := addr.AddressString
		if tsc.keyManager.IsTestnet() {
			pkh, _ := lockingScript.PublicKeyHash()
			testAddr, _ := script.NewAddressFromPublicKeyHash(pkh, false)
			if testAddr != nil {
				addressStr = testAddr.AddressString
			}
		}
		
		return addressStr == tsc.keyManager.GetAddress()
	}

	// For P2PK scripts
	if lockingScript.IsP2PK() {
		pubKey, err := lockingScript.PubKey()
		if err == nil {
			isMainnet := !tsc.keyManager.IsTestnet()
			addr, err := script.NewAddressFromPublicKey(pubKey, isMainnet)
			if err == nil {
				return addr.AddressString == tsc.keyManager.GetAddress()
			}
		}
	}

	return false
}

func (tsc *TxStatusChecker) getLocalUTXO(txID string, vout uint32) (*models.UTXO, error) {
	if tsc.database == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Normalize txID (remove 0x prefix if present, ensure lowercase)
	txID = strings.TrimPrefix(strings.ToLower(txID), "0x")
	
	return tsc.database.GetUTXOByTxHashAndVout(txID, vout)
}

func (tsc *TxStatusChecker) checkLocalDatabase(txID string) []TxOutputStatus {
	var results []TxOutputStatus
	
	if tsc.database == nil || tsc.keyManager == nil {
		return results
	}

	// Normalize txID
	txID = strings.TrimPrefix(strings.ToLower(txID), "0x")
	
	// Get all UTXOs for our address
	utxos, err := tsc.database.GetUTXOsByAddress(tsc.keyManager.GetAddress(), true)
	if err != nil {
		return results
	}

	// Filter for this transaction
	for _, utxo := range utxos {
		if strings.EqualFold(utxo.TxHash, txID) {
			status := TxOutputStatus{
				Vout:        utxo.Vout,
				Amount:      utxo.Amount,
				InLocalDB:   true,
				BelongsToUs: true,
			}
			
			if utxo.Spent {
				status.LocalDBStatus = "spent"
			} else {
				status.LocalDBStatus = "unspent"
			}
			
			results = append(results, status)
		}
	}
	
	return results
}

func (tsc *TxStatusChecker) PrintReport(report *TxStatusReport) {
	fmt.Println("\n=== TRANSACTION STATUS REPORT ===")
	fmt.Printf("Transaction ID: %s\n", report.TxID)
	
	if !report.Found {
		fmt.Printf("Status: NOT FOUND in aerospike\n")
	} else {
		fmt.Printf("Status: FOUND\n")
		fmt.Printf("Total Outputs: %d\n", report.TotalOutputs)
	}
	
	if len(report.Inconsistencies) > 0 {
		fmt.Printf("\n⚠️  INCONSISTENCIES DETECTED: %d\n", len(report.Inconsistencies))
		for _, inconsistency := range report.Inconsistencies {
			fmt.Printf("  - %s\n", inconsistency)
		}
	} else if report.Found {
		fmt.Printf("\n✅ No inconsistencies detected\n")
	}
	
	if len(report.OutputStatuses) > 0 {
		fmt.Printf("\n=== OUTPUT DETAILS ===\n")
		fmt.Printf("%-6s | %-10s | %-8s | %-10s | %-10s | %-10s | %s\n",
			"Output", "Amount", "Ours", "Aerospike", "LocalDB", "In DB", "Spending TX / Notes")
		fmt.Printf("%s\n", strings.Repeat("-", 100))
		
		for _, output := range report.OutputStatuses {
			amountStr := fmt.Sprintf("%d", output.Amount)
			if output.Amount > 1000000 {
				amountStr = fmt.Sprintf("%.3f BSV", float64(output.Amount)/1e8)
			}
			
			ours := ""
			if output.BelongsToUs {
				ours = "YES"
			} else if tsc.keyManager != nil {
				ours = "NO"
			}
			
			aerospikeStatus := output.Status
			if aerospikeStatus == "" {
				aerospikeStatus = "N/A"
			}
			
			localDBStatus := output.LocalDBStatus
			if localDBStatus == "" {
				localDBStatus = "N/A"
			}
			
			inDB := ""
			if output.InLocalDB {
				inDB = "YES"
			} else if output.BelongsToUs {
				inDB = "NO"
			}
			
			notes := ""
			if output.SpendingTxID != "" {
				if len(output.SpendingTxID) > 16 {
					notes = "Spent in: " + output.SpendingTxID[:16] + "..."
				} else {
					notes = "Spent in: " + output.SpendingTxID
				}
			}
			if output.Inconsistency != "" {
				if notes != "" {
					notes += " | "
				}
				notes += "⚠️ " + output.Inconsistency
			}
			
			fmt.Printf("%-6d | %-10s | %-8s | %-10s | %-10s | %-10s | %s\n",
				output.Vout, amountStr, ours, aerospikeStatus, localDBStatus, inDB, notes)
		}
	}
	
	fmt.Println("\n=== SUMMARY ===")
	if report.Found {
		spentCount := 0
		unspentCount := 0
		unknownCount := 0
		ourCount := 0
		ourUnspentCount := 0
		
		for _, output := range report.OutputStatuses {
			switch output.Status {
			case "SPENT":
				spentCount++
			case "UNSPENT":
				unspentCount++
				if output.BelongsToUs {
					ourUnspentCount++
				}
			default:
				unknownCount++
			}
			if output.BelongsToUs {
				ourCount++
			}
		}
		
		fmt.Printf("Total Outputs: %d\n", len(report.OutputStatuses))
		fmt.Printf("Spent: %d | Unspent: %d | Unknown: %d\n", spentCount, unspentCount, unknownCount)
		
		if tsc.keyManager != nil {
			fmt.Printf("Belonging to us: %d (Unspent: %d)\n", ourCount, ourUnspentCount)
		}
	}
	
	if len(report.Inconsistencies) > 0 {
		fmt.Printf("\n❌ Found %d inconsistencies between aerospike and local database\n", len(report.Inconsistencies))
		fmt.Println("Run 'tx-blaster verify' to fix database inconsistencies")
	} else if report.Found && tsc.database != nil {
		fmt.Println("✅ Aerospike and local database are consistent")
	}
}