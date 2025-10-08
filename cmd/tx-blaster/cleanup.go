package main

import (
	"fmt"
	"log"

	"github.com/spf13/cobra"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean up invalid UTXOs from database",
	Long:  "Marks all unspent UTXOs as spent, effectively clearing the database for a fresh start",
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

		fmt.Println("\n=== UTXO Database Cleanup ===")
		fmt.Println("This will mark all unspent UTXOs as spent.")
		
		// Get all unspent UTXOs
		utxos, err := database.GetAllUnspentUTXOs(address)
		if err != nil {
			log.Fatalf("Failed to get UTXOs: %v", err)
		}

		fmt.Printf("\nFound %d unspent UTXOs to clean up\n", len(utxos))

		if len(utxos) == 0 {
			fmt.Println("No UTXOs to clean up")
			return
		}

		// Mark all as spent
		cleanedCount := 0
		for i, utxo := range utxos {
			err = database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			if err != nil {
				fmt.Printf("⚠️  Failed to mark UTXO %s:%d as spent: %v\n", 
					utxo.TxHash[:8], utxo.Vout, err)
			} else {
				cleanedCount++
			}
			
			// Show progress
			if (i+1)%50 == 0 || i == len(utxos)-1 {
				fmt.Printf("\rProgress: %d/%d UTXOs cleaned", i+1, len(utxos))
			}
		}
		
		fmt.Println() // New line after progress
		fmt.Printf("\n✅ Cleanup complete: %d UTXOs marked as spent\n", cleanedCount)
		fmt.Println("\nDatabase is now empty. Run 'scan' to find new UTXOs.")
	},
}

func init() {
	rootCmd.AddCommand(cleanupCmd)
	cleanupCmd.Flags().StringP("key", "k", "", "WIF private key")
	cleanupCmd.MarkFlagRequired("key")
}