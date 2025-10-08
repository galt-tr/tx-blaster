package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tx-blaster/tx-blaster/internal/broadcaster"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/internal/scanner"
	"github.com/tx-blaster/tx-blaster/internal/teranode"
	"github.com/tx-blaster/tx-blaster/internal/txchain"
)


var resyncCmd = &cobra.Command{
	Use:   "resync",
	Short: "Resync UTXO database with blockchain state",
	Long:  "Verifies all UTXOs in database against teranode and removes invalid ones",
	Run: func(cmd *cobra.Command, args []string) {
		// Get WIF key from flag
		wifKey, _ := cmd.Flags().GetString("key")
		if wifKey == "" {
			log.Fatal("WIF key is required")
		}

		// Create key manager
		km, err := keys.NewKeyManager(wifKey)
		if err != nil {
			log.Fatalf("Failed to create key manager: %v", err)
		}
		address := km.GetAddress()
		fmt.Printf("Address: %s\n", address)

		// Create database instance
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to create database: %v", err)
		}
		defer database.Close()

		// Create teranode client
		teranodeURL := "http://localhost:8090"
		teranodeClient := teranode.NewClient(teranodeURL)
		
		// Create scanner for verification
		sc := scanner.NewScanner(teranodeClient, database, km)

		// Create broadcaster for rebroadcasting
		propBroadcaster, err := broadcaster.NewPropagationBroadcaster("localhost:8084", "http://localhost:8833")
		if err != nil {
			log.Fatalf("Failed to create broadcaster: %v", err)
		}
		
		// Create tx rebuilder with broadcaster
		txStore := "/mnt/nvme/teranode/data/txstore"
		txRebuilder := txchain.NewRebuilder(txStore, teranodeURL, propBroadcaster)

		fmt.Println("\n=== Starting UTXO Database Resync ===")
		fmt.Println("This will rebroadcast all UTXO transactions to ensure they exist on the network.")

		// Get all unspent UTXOs from database
		utxos, err := database.GetAllUnspentUTXOs(address)
		if err != nil {
			log.Fatalf("Failed to get UTXOs: %v", err)
		}

		fmt.Printf("\nFound %d unspent UTXOs to verify\n", len(utxos))

		// Track statistics
		validCount := 0
		invalidCount := 0
		spentCount := 0
		startTime := time.Now()

		// Process each UTXO
		for i, utxo := range utxos {
			// Show progress
			if (i+1)%10 == 0 || i == len(utxos)-1 {
				fmt.Printf("\rProgress: %d/%d rebroadcast (✓ %d valid, ✗ %d invalid, ⚠ %d already exists)", 
					i+1, len(utxos), validCount, invalidCount, spentCount)
			}

			// Get the transaction hex from txstore
			txHex, err := txRebuilder.LoadTransactionFromTxStore(utxo.TxHash)
			if err != nil {
				fmt.Printf("\n⚠️  Cannot find transaction %s in txstore: %v\n", utxo.TxHash[:8], err)
				invalidCount++
				database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
				continue
			}

			// Try to broadcast the transaction
			_, err = propBroadcaster.BroadcastTransaction(txHex)
			if err != nil {
				errStr := err.Error()
				
				// Check if it already exists (that's fine)
				if strings.Contains(errStr, "BLOB_EXISTS") || strings.Contains(errStr, "already exists") {
					spentCount++ // Using spentCount to track "already exists"
					validCount++
					continue
				}
				
				// Check if it's missing parents
				if strings.Contains(errStr, "TX_MISSING_PARENT") || strings.Contains(errStr, "missing parent") {
					fmt.Printf("\n🔄 UTXO %s:%d has missing parents, attempting to rebuild chain...\n", 
						utxo.TxHash[:8], utxo.Vout)
					
					// Build and broadcast the parent chain
					chain := make(map[string][]string)
					visited := make(map[string]bool)
					
					if buildErr := txRebuilder.BuildTransactionChain(utxo.TxHash, chain, visited, 0); buildErr != nil {
						fmt.Printf("❌ Failed to build parent chain: %v\n", buildErr)
						invalidCount++
						database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
						continue
					}
					
					// Sort transactions in dependency order
					sortedTxs, sortErr := txchain.TopologicalSort(chain)
					if sortErr != nil {
						fmt.Printf("❌ Failed to sort transaction chain: %v\n", sortErr)
						invalidCount++
						database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
						continue
					}
					
					fmt.Printf("📤 Rebroadcasting %d transactions in dependency order...\n", len(sortedTxs))
					
					// Rebroadcast the chain
					successful, failed, _ := txRebuilder.RebroadcastChain(sortedTxs)
					
					if failed > 0 {
						fmt.Printf("⚠️ Chain rebuilding: %d successful, %d failed\n", successful, failed)
						invalidCount++
						database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
					} else {
						fmt.Printf("✅ Successfully rebuilt transaction chain\n")
						validCount++
					}
					continue
				}
				
				// Other error - transaction is invalid
				fmt.Printf("\n❌ UTXO %s:%d broadcast failed: %v\n", utxo.TxHash[:8], utxo.Vout, err)
				invalidCount++
				database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
				continue
			}
			
			// Successfully broadcast (or was new to the network)
			validCount++
		}

		fmt.Println() // New line after progress

		// Print summary
		elapsed := time.Since(startTime)
		fmt.Println("\n=== Resync Complete ===")
		fmt.Printf("Time taken: %v\n", elapsed.Round(time.Second))
		fmt.Printf("Total UTXOs processed: %d\n", len(utxos))
		fmt.Printf("Valid UTXOs (on network): %d\n", validCount)
		fmt.Printf("Invalid UTXOs removed: %d\n", invalidCount)
		fmt.Printf("Already existed on network: %d\n", spentCount)

		// Now scan recent blocks for new UTXOs
		fmt.Println("\n=== Scanning Recent Blocks for New UTXOs ===")
		
		// Get current block height (not used here but keeping for future reference)
		_, err = database.GetCurrentBlockHeight()
		if err != nil {
			fmt.Printf("Warning: Failed to get current block height: %v\n", err)
		}

		// Scan last 100 blocks for new UTXOs
		blocksToScan := 100
		fmt.Printf("Scanning last %d blocks for new UTXOs...\n", blocksToScan)
		
		err = sc.ScanRecentBlocks(blocksToScan)
		if err != nil {
			fmt.Printf("Warning: Failed to scan recent blocks: %v\n", err)
		} else {
			// Get updated UTXO count
			newUtxos, _ := database.GetAllUnspentUTXOs(address)
			newUtxoCount := len(newUtxos) - validCount
			if newUtxoCount > 0 {
				fmt.Printf("✅ Found %d new UTXOs\n", newUtxoCount)
			} else {
				fmt.Println("No new UTXOs found")
			}
		}

		// Show final database state
		fmt.Println("\n=== Final Database State ===")
		allUtxos, err := database.GetAllUnspentUTXOs(address)
		if err != nil {
			fmt.Printf("Failed to get final UTXO count: %v\n", err)
		} else {
			// Count by type
			coinbaseCount := 0
			regularCount := 0
			var totalSats uint64
			
			for _, utxo := range allUtxos {
				totalSats += utxo.Amount
				if utxo.IsCoinbase {
					coinbaseCount++
				} else {
					regularCount++
				}
			}

			fmt.Printf("Total unspent UTXOs: %d\n", len(allUtxos))
			fmt.Printf("  - Coinbase UTXOs: %d\n", coinbaseCount)
			fmt.Printf("  - Regular UTXOs: %d\n", regularCount)
			fmt.Printf("Total value: %d sats (%.8f BSV)\n", totalSats, float64(totalSats)/100000000)

			// Check if we have enough UTXOs for blasting
			if len(allUtxos) == 0 {
				fmt.Println("\n⚠️  Warning: No valid UTXOs available for blasting!")
				fmt.Println("You may need to:")
				fmt.Println("  1. Wait for coinbase maturity (100+ blocks)")
				fmt.Println("  2. Run 'scan' to find more UTXOs")
				fmt.Println("  3. Check if your address has any unspent outputs")
			} else if len(allUtxos) < 5 {
				fmt.Printf("\n⚠️  Warning: Only %d UTXOs available\n", len(allUtxos))
				fmt.Println("Consider running 'split' to create more UTXOs for sustained blasting")
			} else {
				fmt.Printf("\n✅ Database is ready with %d valid UTXOs\n", len(allUtxos))
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(resyncCmd)
	resyncCmd.Flags().StringP("key", "k", "", "WIF private key")
	resyncCmd.MarkFlagRequired("key")
}
