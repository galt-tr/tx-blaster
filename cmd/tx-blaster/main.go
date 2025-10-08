package main

import (
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"github.com/tx-blaster/tx-blaster/internal/blaster"
	"github.com/tx-blaster/tx-blaster/internal/broadcaster"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/internal/scanner"
	"github.com/tx-blaster/tx-blaster/internal/teranode"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

var (
	privateKey         string
	teranodeURL        string
	databasePath       string
	scanBlocks         int
	startHeight        uint32
	endHeight          uint32
	scanAll            bool
	numOutputs         int
	txHex              string
	rpcURL             string
	propagationGRPC    string
	propagationHTTP    string
	broadcastAfterSplit bool
	usePropagation     bool
	blastIterations    int
	blastDebug         bool
	txStorePath        string
	enableRebroadcast  bool
	broadcastTxID      string
	broadcastTxDebug   bool
	rebuildChain       bool
	txPerBatch         int
	fromTxHash         string
	fromVout           uint32
	txStatusID         string
	blastFromTxHash    string
	blastFromVout      uint32
	blastRate          int
	numSplitOutputs    int
	useSimpleUI        bool
)

var rootCmd = &cobra.Command{
	Use:   "tx-blaster",
	Short: "BSV Transaction Blaster - Find and manage coinbase UTXOs",
	Long: `TX Blaster is a CLI tool for finding and managing coinbase UTXOs 
from a BSV blockchain node (teranode). It supports both testnet and mainnet.`,
}

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan blockchain for coinbase UTXOs",
	Long:  `Scan recent blocks or a specific range to find coinbase UTXOs for your address`,
	Run: func(cmd *cobra.Command, args []string) {
		if privateKey == "" {
			log.Fatal("Private key is required. Use --key flag")
		}

		// Initialize key manager
		keyManager, err := keys.NewKeyManager(privateKey)
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		fmt.Printf("Network: %s\n", keyManager.GetNetwork())
		fmt.Printf("Address: %s\n", keyManager.GetAddress())
		fmt.Println()

		// Initialize database
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer database.Close()

		// Initialize teranode client
		client := teranode.NewClient(teranodeURL)

		// Create scanner
		scan := scanner.NewScanner(client, database, keyManager)

		// Perform scan based on flags
		if scanAll {
			// Scan all blocks from genesis
			err = scan.ScanAllBlocks()
		} else if startHeight > 0 || endHeight > 0 {
			if endHeight < startHeight {
				log.Fatal("End height must be greater than or equal to start height")
			}
			err = scan.ScanBlockRange(startHeight, endHeight)
		} else {
			err = scan.ScanRecentBlocks(scanBlocks)
		}

		if err != nil {
			log.Fatalf("Scan failed: %v", err)
		}

		// Show summary
		err = scan.GetUTXOSummary()
		if err != nil {
			log.Fatalf("Failed to get UTXO summary: %v", err)
		}
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all UTXOs for the address",
	Long:  `Display all unspent transaction outputs (UTXOs) found for your address`,
	Run: func(cmd *cobra.Command, args []string) {
		if privateKey == "" {
			log.Fatal("Private key is required. Use --key flag")
		}

		// Initialize key manager
		keyManager, err := keys.NewKeyManager(privateKey)
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		// Initialize database
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer database.Close()

		// Initialize teranode client (even though we don't use it for listing)
		client := teranode.NewClient(teranodeURL)

		// Create scanner and show summary
		scan := scanner.NewScanner(client, database, keyManager)
		err = scan.GetUTXOSummary()
		if err != nil {
			log.Fatalf("Failed to get UTXO summary: %v", err)
		}
	},
}

var balanceCmd = &cobra.Command{
	Use:   "balance",
	Short: "Show total balance from UTXOs",
	Long:  `Calculate and display the total balance from all unspent UTXOs`,
	Run: func(cmd *cobra.Command, args []string) {
		if privateKey == "" {
			log.Fatal("Private key is required. Use --key flag")
		}

		// Initialize key manager
		keyManager, err := keys.NewKeyManager(privateKey)
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		// Initialize database
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer database.Close()

		// Get balance
		balance, err := database.GetTotalBalance(keyManager.GetAddress())
		if err != nil {
			log.Fatalf("Failed to get balance: %v", err)
		}

		fmt.Printf("Address: %s\n", keyManager.GetAddress())
		fmt.Printf("Network: %s\n", keyManager.GetNetwork())
		fmt.Printf("Balance: %d satoshis (%.8f BSV)\n", balance, float64(balance)/1e8)
	},
}

var splitCmd = &cobra.Command{
	Use:   "split",
	Short: "Create a transaction that splits a UTXO into many outputs",
	Long:  `Takes the oldest UTXO and creates a transaction with many outputs for blasting`,
	Run: func(cmd *cobra.Command, args []string) {
		if privateKey == "" {
			log.Fatal("Private key is required. Use --key flag")
		}

		// Initialize key manager
		keyManager, err := keys.NewKeyManager(privateKey)
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		// Initialize database
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer database.Close()

		// Get oldest UTXO
		utxo, err := database.GetOldestUTXO(keyManager.GetAddress())
		if err != nil {
			log.Fatalf("Failed to get oldest UTXO: %v", err)
		}

		fmt.Printf("Using UTXO: %s:%d with %d satoshis\n", utxo.TxHash, utxo.Vout, utxo.Amount)

		// Create transaction builder
		builder := blaster.NewBuilder(keyManager)

		// Build split transaction
		tx, err := builder.BuildSplitTransaction(utxo, numOutputs)
		if err != nil {
			log.Fatalf("Failed to build transaction: %v", err)
		}

		// Get transaction hex
		txHexStr, err := blaster.GetTransactionHex(tx)
		if err != nil {
			log.Fatalf("Failed to get transaction hex: %v", err)
		}

		fmt.Printf("Transaction created successfully!\n")
		fmt.Printf("Transaction ID: %s\n", tx.TxID())
		fmt.Printf("Size: %d bytes\n", tx.Size())
		fmt.Printf("Inputs: %d\n", len(tx.Inputs))
		fmt.Printf("Outputs: %d\n", len(tx.Outputs))
		
		if broadcastAfterSplit {
			fmt.Printf("\nBroadcasting transaction...\n")
			
			// Create broadcaster
			pb, err := broadcaster.NewPropagationBroadcaster(propagationGRPC, propagationHTTP)
			if err != nil {
				log.Fatalf("Failed to create propagation broadcaster: %v", err)
			}
			defer pb.Stop()
			
			// Broadcast transaction
			txID, err := pb.BroadcastTransaction(txHexStr)
			if err != nil {
				log.Fatalf("Failed to broadcast transaction: %v", err)
			}
			
			// Mark UTXO as spent
			err = database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			if err != nil {
				log.Printf("Warning: Failed to mark UTXO as spent: %v", err)
			}
			
			// Get current block height
			currentHeight, err := database.GetCurrentBlockHeight()
			if err != nil {
				log.Printf("Warning: Failed to get current block height: %v", err)
				currentHeight = utxo.BlockHeight // Fallback to UTXO's height
			}

			// Save new UTXOs from the transaction outputs
			newUTXOs := make([]*models.UTXO, len(tx.Outputs))
			for i, output := range tx.Outputs {
				newUTXOs[i] = &models.UTXO{
					TxHash:      txID,
					Vout:        uint32(i),
					Amount:      output.Satoshis,
					BlockHeight: currentHeight + 1, // Use next block height
					Address:     keyManager.GetAddress(),
					Spent:       false,
					IsCoinbase:  false,
				}
			}
			
			err = database.SaveTransactionOutputs(txID, newUTXOs)
			if err != nil {
				log.Printf("Warning: Failed to save new UTXOs: %v", err)
			} else {
				fmt.Printf("  Saved %d new UTXOs to database\n", len(newUTXOs))
			}
			
			fmt.Printf("\nTransaction broadcast successfully!\n")
			fmt.Printf("Transaction ID: %s\n", txID)
		} else {
			fmt.Printf("\nTransaction hex:\n%s\n", txHexStr)
			fmt.Printf("\nUse 'broadcast --tx %s' to send this transaction\n", txHexStr)
			fmt.Printf("\nNote: After broadcasting, the new UTXOs will be saved to the database automatically.")
		}
	},
}

var broadcastCmd = &cobra.Command{
	Use:   "broadcast",
	Short: "Broadcast a transaction to the teranode network",
	Long:  `Send a raw transaction hex to the teranode network`,
	Run: func(cmd *cobra.Command, args []string) {
		if txHex == "" {
			log.Fatal("Transaction hex is required. Use --tx flag")
		}

		var txID string
		var err error
		
		if usePropagation {
			// Use propagation client
			fmt.Printf("Broadcasting transaction via propagation service...\n")
			pb, err := broadcaster.NewPropagationBroadcaster(propagationGRPC, propagationHTTP)
			if err != nil {
				log.Fatalf("Failed to create propagation broadcaster: %v", err)
			}
			defer pb.Stop()
			
			txID, err = pb.BroadcastTransaction(txHex)
		} else {
			// Use RPC
			fmt.Printf("Broadcasting transaction to %s...\n", rpcURL)
			bc := broadcaster.NewBroadcaster(rpcURL)
			txID, err = bc.BroadcastTransaction(txHex)
		}
		
		if err != nil {
			log.Fatalf("Failed to broadcast transaction: %v", err)
		}

		fmt.Printf("Transaction broadcast successfully!\n")
		fmt.Printf("Transaction ID: %s\n", txID)
	},
}

var blastCmd = &cobra.Command{
	Use:   "blast",
	Short: "Continuously create and broadcast transactions",
	Long:  `Automatically create many transactions from UTXOs and broadcast them for stress testing`,
	Run: func(cmd *cobra.Command, args []string) {
		if privateKey == "" {
			log.Fatal("Private key is required. Use --key flag")
		}

		// Initialize key manager
		keyManager, err := keys.NewKeyManager(privateKey)
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		// Initialize database
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer database.Close()
		
		// Create and start blast manager
		blastManager, err := NewBlastManagerWithTxStore(
			keyManager,
			database,
			teranodeURL,
			usePropagation,
			propagationGRPC,
			propagationHTTP,
			rpcURL,
			txStorePath,
			enableRebroadcast,
		)
		if err != nil {
			log.Fatalf("Failed to create blast manager: %v", err)
		}
		defer blastManager.Stop()
		
		// Set iterations limit if specified
		if blastIterations > 0 {
			fmt.Printf("Will stop after %d batch attempts\n", blastIterations)
			blastManager.SetIterations(blastIterations)
		}
		
		// Enable debug mode if specified
		if blastDebug {
			fmt.Printf("Debug mode enabled - will dump transaction hex on failures\n")
			blastManager.SetDebug(true)
		}
		
		// Set transactions per batch
		if txPerBatch > 0 {
			fmt.Printf("Using %d transactions per batch\n", txPerBatch)
			blastManager.SetTxPerBatch(txPerBatch)
		}
		
		// Check if specific UTXO was provided
		if fromTxHash != "" {
			// Start blasting from specific UTXO
			blastManager.StartFromUTXO(fromTxHash, fromVout)
		} else {
			// Start normal blasting
			blastManager.Start()
		}
	},
}

var blastFromTxCmd = &cobra.Command{
	Use:   "blast-from-tx",
	Short: "Split a UTXO into many outputs and blast at a fixed rate",
	Long:  `Takes a transaction output, splits it into many outputs (default 500000),
then continuously creates chained transactions from those outputs at a fixed rate.`,
	Run: func(cmd *cobra.Command, args []string) {
		if privateKey == "" {
			log.Fatal("Private key is required. Use --key flag")
		}
		if blastFromTxHash == "" {
			log.Fatal("Transaction hash is required. Use --tx flag")
		}
		if blastRate <= 0 {
			log.Fatal("Blast rate must be positive. Use --rate flag")
		}

		// Initialize key manager
		keyManager, err := keys.NewKeyManager(privateKey)
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		// Initialize database
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer database.Close()

		// Create and start blast from tx manager
		manager, err := NewBlastFromTxManager(
			keyManager,
			database,
			teranodeURL,
			propagationGRPC,
			propagationHTTP,
			blastFromTxHash,
			blastFromVout,
			numSplitOutputs,
			blastRate,
		)
		manager.SetSimpleUI(useSimpleUI)
		if err != nil {
			log.Fatalf("Failed to create blast-from-tx manager: %v", err)
		}

		// Start blasting
		manager.Start()
	},
}

var broadcastTxCmd = &cobra.Command{
	Use:   "broadcast-tx",
	Short: "Broadcast a transaction from txstore by its ID",
	Long:  `Load a transaction from the local txstore by its transaction ID and broadcast it to the network.
Optionally rebuild missing parent chains before broadcasting.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Call the broadcast function
		BroadcastTxFromStore(
			broadcastTxID,
			txStorePath,
			usePropagation,
			propagationGRPC,
			propagationHTTP,
			rpcURL,
			broadcastTxDebug,
			rebuildChain,
			teranodeURL,
		)
	},
}

var txStatusCmd = &cobra.Command{
	Use:   "tx-status",
	Short: "Check the status of a transaction and its outputs",
	Long:  `Query a transaction by ID and check the status of all its outputs.
Compares local database with aerospike data and reports any inconsistencies.
Shows which outputs are spent, unspent, and in which transactions they were spent.`,
	Run: func(cmd *cobra.Command, args []string) {
		if txStatusID == "" {
			log.Fatal("Transaction ID is required. Use --txid flag")
		}

		// Initialize key manager (optional for ownership checking)
		var keyManager *keys.KeyManager
		if privateKey != "" {
			var err error
			keyManager, err = keys.NewKeyManager(privateKey)
			if err != nil {
				log.Fatalf("Failed to parse private key: %v", err)
			}
		}

		// Initialize database (optional)
		var database *db.Database
		if keyManager != nil {
			var err error
			database, err = db.NewDatabase(databasePath)
			if err != nil {
				log.Printf("Warning: Failed to open database: %v", err)
				database = nil
			} else {
				defer database.Close()
			}
		}

		// Create status checker
		checker := NewTxStatusChecker(database, teranodeURL, keyManager)

		// Check transaction status
		report, err := checker.CheckTransactionStatus(txStatusID)
		if err != nil {
			log.Fatalf("Failed to check transaction status: %v", err)
		}

		// Print report
		checker.PrintReport(report)
	},
}

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify UTXOs against teranode/aerospike",
	Long:  `Check all UTXOs in the database against the teranode/aerospike to ensure they are correctly marked as spent/unspent.
Optionally rebroadcast missing transactions from local txstore.`,
	Run: func(cmd *cobra.Command, args []string) {
		if privateKey == "" {
			log.Fatal("Private key is required. Use --key flag")
		}

		// Initialize key manager
		keyManager, err := keys.NewKeyManager(privateKey)
		if err != nil {
			log.Fatalf("Failed to parse private key: %v", err)
		}

		// Initialize database
		database, err := db.NewDatabase(databasePath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer database.Close()

		// Create verify manager with txstore support
		verifyManager := NewVerifyManagerWithTxStore(
			keyManager, 
			database, 
			teranodeURL,
			txStorePath,
			enableRebroadcast,
			propagationGRPC,
			propagationHTTP,
			rpcURL,
			usePropagation,
		)
		if err := verifyManager.VerifyUTXOs(); err != nil {
			log.Fatalf("Verification failed: %v", err)
		}
	},
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().StringVarP(&privateKey, "key", "k", "", "Private key in WIF format (required)")
	rootCmd.PersistentFlags().StringVarP(&teranodeURL, "teranode", "t", "http://localhost:8090", "Teranode asset service URL")
	rootCmd.PersistentFlags().StringVarP(&databasePath, "database", "d", "utxos.db", "SQLite database path")

	// Scan command flags
	scanCmd.Flags().BoolVarP(&scanAll, "all", "a", false, "Scan all blocks from genesis (height 0)")
	scanCmd.Flags().IntVarP(&scanBlocks, "blocks", "b", 100, "Number of recent blocks to scan")
	scanCmd.Flags().Uint32Var(&startHeight, "start", 0, "Start block height (for range scan)")
	scanCmd.Flags().Uint32Var(&endHeight, "end", 0, "End block height (for range scan)")

	// Split command flags
	splitCmd.Flags().IntVarP(&numOutputs, "outputs", "o", 1000, "Number of outputs to create")
	splitCmd.Flags().BoolVar(&broadcastAfterSplit, "broadcast", false, "Broadcast the transaction after creating it")
	splitCmd.Flags().StringVar(&propagationGRPC, "propagation-grpc", "localhost:8084", "Propagation service gRPC address")
	splitCmd.Flags().StringVar(&propagationHTTP, "propagation-http", "http://localhost:8833", "Propagation service HTTP address")
	
	// Broadcast command flags
	broadcastCmd.Flags().StringVar(&txHex, "tx", "", "Transaction hex to broadcast (required)")
	broadcastCmd.Flags().BoolVar(&usePropagation, "use-propagation", true, "Use propagation service instead of RPC")
	broadcastCmd.Flags().StringVarP(&rpcURL, "rpc", "r", "http://localhost:9292", "Teranode RPC URL (when not using propagation)")
	broadcastCmd.Flags().StringVar(&propagationGRPC, "propagation-grpc", "localhost:8084", "Propagation service gRPC address")
	broadcastCmd.Flags().StringVar(&propagationHTTP, "propagation-http", "http://localhost:8833", "Propagation service HTTP address")
	broadcastCmd.MarkFlagRequired("tx")
	
	// Blast command flags
	blastCmd.Flags().IntVarP(&numOutputs, "outputs", "o", 1000, "Number of outputs per transaction")
	blastCmd.Flags().IntVarP(&blastIterations, "iterations", "i", 0, "Number of batch attempts before stopping (0 = unlimited)")
	blastCmd.Flags().IntVar(&txPerBatch, "tx-per-batch", 1000, "Number of transactions per batch (default 1000)")
	blastCmd.Flags().BoolVar(&blastDebug, "debug", false, "Enable debug mode (dumps raw transaction hex on failure)")
	blastCmd.Flags().BoolVar(&usePropagation, "use-propagation", true, "Use propagation service instead of RPC")
	blastCmd.Flags().StringVarP(&rpcURL, "rpc", "r", "http://localhost:9292", "Teranode RPC URL (when not using propagation)")
	blastCmd.Flags().StringVar(&propagationGRPC, "propagation-grpc", "localhost:8084", "Propagation service gRPC address")
	blastCmd.Flags().StringVar(&propagationHTTP, "propagation-http", "http://localhost:8833", "Propagation service HTTP address")
	blastCmd.Flags().StringVar(&txStorePath, "txstore", "/mnt/nvme/teranode/data/txstore", "Path to transaction store directory")
	blastCmd.Flags().BoolVar(&enableRebroadcast, "rebroadcast-parents", false, "Enable automatic rebroadcasting of missing parent transactions from txstore")
	blastCmd.Flags().StringVar(&fromTxHash, "from-tx", "", "Start blasting from a specific transaction hash")
	blastCmd.Flags().Uint32Var(&fromVout, "vout", 0, "Output index of the transaction to use (default 0)")

	// Tx-status command flags
	txStatusCmd.Flags().StringVar(&txStatusID, "txid", "", "Transaction ID to check (required)")
	txStatusCmd.MarkFlagRequired("txid")

	// Verify command flags
	verifyCmd.Flags().StringVar(&txStorePath, "txstore", "/mnt/nvme/teranode/data/txstore", "Path to transaction store directory")
	verifyCmd.Flags().BoolVar(&enableRebroadcast, "rebroadcast", false, "Enable automatic rebroadcasting of missing transactions from txstore")
	verifyCmd.Flags().BoolVar(&usePropagation, "use-propagation", true, "Use propagation service for rebroadcasting")
	verifyCmd.Flags().StringVar(&propagationGRPC, "propagation-grpc", "localhost:8084", "Propagation service gRPC address")
	verifyCmd.Flags().StringVar(&propagationHTTP, "propagation-http", "http://localhost:8833", "Propagation service HTTP address")
	verifyCmd.Flags().StringVarP(&rpcURL, "rpc", "r", "http://localhost:9292", "Teranode RPC URL (when not using propagation)")

	// Broadcast-tx command flags
	broadcastTxCmd.Flags().StringVar(&broadcastTxID, "txid", "", "Transaction ID to broadcast (required)")
	broadcastTxCmd.Flags().StringVar(&txStorePath, "txstore", "/mnt/nvme/teranode/data/txstore", "Path to transaction store directory")
	broadcastTxCmd.Flags().BoolVar(&broadcastTxDebug, "debug", false, "Enable debug mode (shows transaction details)")
	broadcastTxCmd.Flags().BoolVar(&rebuildChain, "rebuild-chain", false, "Attempt to rebuild missing parent chain before broadcasting")
	broadcastTxCmd.Flags().BoolVar(&usePropagation, "use-propagation", true, "Use propagation service instead of RPC")
	broadcastTxCmd.Flags().StringVar(&propagationGRPC, "propagation-grpc", "localhost:8084", "Propagation service gRPC address")
	broadcastTxCmd.Flags().StringVar(&propagationHTTP, "propagation-http", "http://localhost:8833", "Propagation service HTTP address")
	broadcastTxCmd.Flags().StringVarP(&rpcURL, "rpc", "r", "http://localhost:9292", "Teranode RPC URL (when not using propagation)")
	broadcastTxCmd.MarkFlagRequired("txid")

	// Blast-from-tx command flags
	blastFromTxCmd.Flags().StringVar(&blastFromTxHash, "tx", "", "Transaction hash to blast from (required)")
	blastFromTxCmd.Flags().Uint32Var(&blastFromVout, "vout", 0, "Output index of the transaction to use")
	blastFromTxCmd.Flags().IntVar(&blastRate, "rate", 10, "Transactions per second to blast")
	blastFromTxCmd.Flags().IntVar(&numSplitOutputs, "outputs", 500000, "Number of outputs to split into")
	blastFromTxCmd.Flags().BoolVar(&useSimpleUI, "simple-ui", false, "Use simple UI mode (recommended for Warp terminal)")
	blastFromTxCmd.Flags().StringVar(&propagationGRPC, "propagation-grpc", "localhost:8084", "Propagation service gRPC address")
	blastFromTxCmd.Flags().StringVar(&propagationHTTP, "propagation-http", "http://localhost:8833", "Propagation service HTTP address")
	blastFromTxCmd.MarkFlagRequired("tx")

	// Add commands
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(balanceCmd)
	rootCmd.AddCommand(splitCmd)
	rootCmd.AddCommand(broadcastCmd)
	rootCmd.AddCommand(broadcastTxCmd)
	rootCmd.AddCommand(blastCmd)
	rootCmd.AddCommand(blastFromTxCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(txStatusCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
