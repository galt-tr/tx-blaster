package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/internal/blaster"
	"github.com/tx-blaster/tx-blaster/internal/broadcaster"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/internal/scanner"
	"github.com/tx-blaster/tx-blaster/internal/teranode"
	"github.com/tx-blaster/tx-blaster/internal/txchain"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

type BlastManager struct {
	keyManager *keys.KeyManager
	database   *db.Database
	builder    *blaster.Builder
	pb         *broadcaster.PropagationBroadcaster
	db         *broadcaster.DirectBatchBroadcaster // Direct batch broadcaster
	bc         *broadcaster.Broadcaster
	scanner    *scanner.Scanner
	client     *teranode.Client // Teranode client for UTXO verification
	useProp    bool

	// Chain rebuilding support
	txRebuilder       *txchain.Rebuilder
	enableRebroadcast bool
	teranodeURL       string

	// Statistics
	totalTxSent   int
	totalFailures int
	totalAttempts int // Track total batch attempts
	startTime     time.Time
	mu            sync.Mutex

	// Configuration
	txPerBatch    int
	syncInterval  time.Duration
	maxIterations int  // Maximum batch attempts (0 = unlimited)
	debugMode     bool // Debug mode for dumping transaction hex
}

func NewBlastManager(
	keyManager *keys.KeyManager,
	database *db.Database,
	teranodeURL string,
	usePropagation bool,
	propagationGRPC string,
	propagationHTTP string,
	rpcURL string,
) (*BlastManager, error) {
	return NewBlastManagerWithTxStore(
		keyManager,
		database,
		teranodeURL,
		usePropagation,
		propagationGRPC,
		propagationHTTP,
		rpcURL,
		"",    // No txstore by default
		false, // No rebroadcast by default
	)
}

func NewBlastManagerWithTxStore(
	keyManager *keys.KeyManager,
	database *db.Database,
	teranodeURL string,
	usePropagation bool,
	propagationGRPC string,
	propagationHTTP string,
	rpcURL string,
	txStorePath string,
	enableRebroadcast bool,
) (*BlastManager, error) {

	builder := blaster.NewBuilder(keyManager)

	var pb *broadcaster.PropagationBroadcaster
	var db *broadcaster.DirectBatchBroadcaster
	var bc *broadcaster.Broadcaster

	if usePropagation {
		var err error
		// Use direct batch broadcaster for maximum efficiency
		db, err = broadcaster.NewDirectBatchBroadcaster(propagationGRPC)
		if err != nil {
			return nil, fmt.Errorf("failed to create direct batch broadcaster: %w", err)
		}
		// Also create regular propagation broadcaster as fallback
		pb, err = broadcaster.NewPropagationBroadcasterWithBatching(propagationGRPC, propagationHTTP, true)
		if err != nil {
			return nil, fmt.Errorf("failed to create propagation broadcaster: %w", err)
		}
	} else {
		bc = broadcaster.NewBroadcaster(rpcURL)
	}

	// Create scanner for periodic syncing
	client := teranode.NewClient(teranodeURL)
	scan := scanner.NewScanner(client, database, keyManager)

	bm := &BlastManager{
		keyManager:        keyManager,
		database:          database,
		builder:           builder,
		pb:                pb,
		db:                db,
		bc:                bc,
		scanner:           scan,
		client:            client,
		useProp:           usePropagation,
		teranodeURL:       teranodeURL,
		enableRebroadcast: enableRebroadcast,
		txPerBatch:        1000,            // Create 1000 transactions from each UTXO (max batch size)
		syncInterval:      5 * time.Minute, // Sync new blocks every 5 minutes
		startTime:         time.Now(),
		maxIterations:     0, // Unlimited by default
	}

	// Set up transaction chain rebuilder if enabled
	if enableRebroadcast && txStorePath != "" {
		// Create a simple broadcaster wrapper
		var txBroadcaster interface{ BroadcastTransaction(string) (string, error) }
		if usePropagation && db != nil {
			txBroadcaster = &broadcaster.BatchBroadcasterWrapper{DB: db}
		} else if bc != nil {
			txBroadcaster = bc
		}

		if txBroadcaster != nil {
			bm.txRebuilder = txchain.NewRebuilder(txStorePath, teranodeURL, txBroadcaster)
			fmt.Printf("Transaction chain rebuilding enabled with txstore: %s\n", txStorePath)
		}
	}

	return bm, nil
}

// SetIterations sets the maximum number of batch attempts
func (bm *BlastManager) SetIterations(iterations int) {
	bm.maxIterations = iterations
}

// SetDebug enables or disables debug mode
func (bm *BlastManager) SetDebug(debug bool) {
	bm.debugMode = debug
}

// SetTxPerBatch sets the number of transactions per batch
func (bm *BlastManager) SetTxPerBatch(txPerBatch int) {
	bm.txPerBatch = txPerBatch
}

func (bm *BlastManager) Start() {
	fmt.Printf("Starting optimized transaction blaster...\n")
	fmt.Printf("Address: %s\n", bm.keyManager.GetAddress())
	fmt.Printf("Transactions per UTXO: %d\n", bm.txPerBatch)
	fmt.Printf("Sync interval: %v\n", bm.syncInterval)
	if bm.maxIterations > 0 {
		fmt.Printf("Max batch attempts: %d\n", bm.maxIterations)
	}
	if bm.debugMode {
		fmt.Printf("Debug mode: ENABLED\n")
	}
	if bm.enableRebroadcast && bm.txRebuilder != nil {
		fmt.Printf("Parent chain rebuilding: ENABLED\n")
	}
	fmt.Println()

	// Start periodic syncing in background
	go bm.periodicSync()

	// Start statistics reporter
	go bm.reportStats()

	// Main blasting loop
	for {
		// Check if we've reached the iteration limit (batch attempts)
		if bm.maxIterations > 0 && bm.totalAttempts >= bm.maxIterations {
			runtime := time.Since(bm.startTime)
			tps := float64(bm.totalTxSent) / runtime.Seconds()
			successRate := float64(bm.totalTxSent) / float64(bm.totalTxSent+bm.totalFailures) * 100

			fmt.Printf("\n╔══════════════════════════════════════════╗\n")
			fmt.Printf("║      ITERATION LIMIT REACHED             ║\n")
			fmt.Printf("╠══════════════════════════════════════════╣\n")
			fmt.Printf("║ Target attempts:   %-22d║\n", bm.maxIterations)
			fmt.Printf("║ Total attempts:    %-22d║\n", bm.totalAttempts)
			fmt.Printf("║ TX sent:           %-22d║\n", bm.totalTxSent)
			fmt.Printf("║ TX failed:         %-22d║\n", bm.totalFailures)
			fmt.Printf("║ Success rate:      %-21.1f%%║\n", successRate)
			fmt.Printf("╠══════════════════════════════════════════╣\n")
			fmt.Printf("║ Total runtime:     %-22s║\n", runtime.Round(time.Second))
			fmt.Printf("║ Average TPS:       %-22.2f║\n", tps)
			fmt.Printf("║ TX per minute:     %-22.0f║\n", tps*60)
			fmt.Printf("╚══════════════════════════════════════════╝\n")
			break
		}
		// Get current block height from teranode for maturity check
		client := teranode.NewClient(bm.teranodeURL)
		bestHeader, err := client.GetBestBlockHeader()
		var currentHeight uint32
		if err != nil {
			log.Printf("Failed to get current block height from teranode: %v", err)
			// Fall back to database height if teranode fails
			currentHeight, err = bm.database.GetCurrentBlockHeight()
			if err != nil {
				log.Printf("Failed to get current block height from database: %v", err)
				currentHeight = 0
			}
		} else {
			currentHeight = bestHeader.Height
		}

		// Get oldest mature UTXO (coinbase needs to be 100+ blocks old)
		utxo, err := bm.database.GetOldestMatureUTXO(bm.keyManager.GetAddress(), currentHeight)
		if err != nil {
			fmt.Printf("No mature UTXOs available: %v (current height: %d)\n", err, currentHeight)
			fmt.Printf("Waiting for new UTXOs or coinbase maturity...\n")
			time.Sleep(30 * time.Second)
			continue
		}

		// Calculate maturity status for logging
		maturityInfo := ""
		if utxo.IsCoinbase {
			blocksUntilMature := int(utxo.BlockHeight+100) - int(currentHeight)
			if blocksUntilMature > 0 {
				maturityInfo = fmt.Sprintf(" (WARNING: needs %d more blocks for maturity)", blocksUntilMature)
			} else {
				maturityInfo = fmt.Sprintf(" (mature: %d blocks old)", currentHeight-utxo.BlockHeight)
			}
		}

		fmt.Printf("Using utxo %s (height %d, current %d)%s\n", utxo.TxHash, utxo.BlockHeight, currentHeight, maturityInfo)

		// Log UTXO type
		utxoType := "split"
		if utxo.IsCoinbase {
			utxoType = "coinbase"
		}
		
		fmt.Printf("\n[%s] Using %s UTXO %s:%d (%d sats, height %d)\n",
			time.Now().Format("15:04:05"), utxoType, utxo.TxHash[:8], utxo.Vout, utxo.Amount, utxo.BlockHeight)

		// Mark UTXO as spent immediately to prevent reuse
		err = bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
		if err != nil {
			log.Printf("Warning: Failed to mark UTXO as spent before use: %v", err)
		}

		// Create many transactions from this UTXO
		bm.blastFromUTXO(utxo)

		// Small delay between batches
		time.Sleep(100 * time.Millisecond)
	}
}

// StartFromUTXO starts blasting from a specific UTXO identified by transaction hash and output index
func (bm *BlastManager) StartFromUTXO(txHash string, vout uint32) {
	fmt.Printf("Starting optimized transaction blaster from specific UTXO...\n")
	fmt.Printf("Address: %s\n", bm.keyManager.GetAddress())
	fmt.Printf("Starting UTXO: %s:%d\n", txHash, vout)
	fmt.Printf("Transactions per UTXO: %d\n", bm.txPerBatch)
	fmt.Printf("Sync interval: %v\n", bm.syncInterval)
	if bm.maxIterations > 0 {
		fmt.Printf("Max batch attempts: %d\n", bm.maxIterations)
	}
	if bm.debugMode {
		fmt.Printf("Debug mode: ENABLED\n")
	}
	if bm.enableRebroadcast && bm.txRebuilder != nil {
		fmt.Printf("Parent chain rebuilding: ENABLED\n")
	}
	fmt.Println()

	// Start periodic syncing in background
	go bm.periodicSync()

	// Start statistics reporter
	go bm.reportStats()

	// First, try to get the UTXO from the database
	utxo, err := bm.database.GetUTXOByTxHashAndVout(txHash, vout)
	if err != nil {
		// UTXO not in database, create it from the transaction info
		// We'll need to fetch transaction details from teranode
		fmt.Printf("UTXO not found in database, fetching from teranode...\n")
		
		client := teranode.NewClient(bm.teranodeURL)
		txDetails, fetchErr := client.GetTransaction(txHash)
		if fetchErr != nil {
			log.Fatalf("Failed to fetch transaction %s: %v", txHash, fetchErr)
		}

		// Check if the output index is valid
		if int(vout) >= len(txDetails.Outputs) {
			log.Fatalf("Invalid output index %d for transaction %s (has %d outputs)", vout, txHash, len(txDetails.Outputs))
		}

		output := txDetails.Outputs[vout]
		
		// Create UTXO from the transaction output
		utxo = &models.UTXO{
			TxHash:      txHash,
			Vout:        vout,
			Amount:      output.Satoshis,
			BlockHeight: 0, // We don't have block height from transaction details
			Address:     bm.keyManager.GetAddress(), // Assume it's ours
			Spent:       false,
			IsCoinbase:  false, // We'll assume it's not coinbase for specific UTXOs
		}

		// Save to database for tracking
		if saveErr := bm.database.SaveUTXO(utxo); saveErr != nil {
			log.Printf("Warning: Failed to save UTXO to database: %v", saveErr)
		}
	}

	// Log UTXO type
	utxoType := "split"
	if utxo.IsCoinbase {
		utxoType = "coinbase"
	}
	
	fmt.Printf("\n[%s] Starting with %s UTXO %s:%d (%d sats, height %d)\n",
		time.Now().Format("15:04:05"), utxoType, utxo.TxHash[:8], utxo.Vout, utxo.Amount, utxo.BlockHeight)

	// Mark UTXO as spent immediately to prevent reuse
	err = bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
	if err != nil {
		log.Printf("Warning: Failed to mark UTXO as spent before use: %v", err)
	}

	// Create many transactions from this UTXO
	bm.blastFromUTXO(utxo)

	// After the first UTXO, continue with normal blasting
	bm.continueBlasting()
}

// continueBlasting continues the normal blasting loop after processing the initial UTXO
func (bm *BlastManager) continueBlasting() {
	// Main blasting loop
	for {
		// Check if we've reached the iteration limit (batch attempts)
		if bm.maxIterations > 0 && bm.totalAttempts >= bm.maxIterations {
			runtime := time.Since(bm.startTime)
			tps := float64(bm.totalTxSent) / runtime.Seconds()
			successRate := float64(bm.totalTxSent) / float64(bm.totalTxSent+bm.totalFailures) * 100

			fmt.Printf("\n╔══════════════════════════════════════════╗\n")
			fmt.Printf("║      ITERATION LIMIT REACHED             ║\n")
			fmt.Printf("╠══════════════════════════════════════════╣\n")
			fmt.Printf("║ Target attempts:   %-22d║\n", bm.maxIterations)
			fmt.Printf("║ Total attempts:    %-22d║\n", bm.totalAttempts)
			fmt.Printf("║ TX sent:           %-22d║\n", bm.totalTxSent)
			fmt.Printf("║ TX failed:         %-22d║\n", bm.totalFailures)
			fmt.Printf("║ Success rate:      %-21.1f%%║\n", successRate)
			fmt.Printf("╠══════════════════════════════════════════╣\n")
			fmt.Printf("║ Total runtime:     %-22s║\n", runtime.Round(time.Second))
			fmt.Printf("║ Average TPS:       %-22.2f║\n", tps)
			fmt.Printf("║ TX per minute:     %-22.0f║\n", tps*60)
			fmt.Printf("╚══════════════════════════════════════════╝\n")
			break
		}
		
		// Get current block height from teranode for maturity check
		client := teranode.NewClient(bm.teranodeURL)
		bestHeader, err := client.GetBestBlockHeader()
		var currentHeight uint32
		if err != nil {
			log.Printf("Failed to get current block height from teranode: %v", err)
			// Fall back to database height if teranode fails
			currentHeight, err = bm.database.GetCurrentBlockHeight()
			if err != nil {
				log.Printf("Failed to get current block height from database: %v", err)
				currentHeight = 0
			}
		} else {
			currentHeight = bestHeader.Height
		}

		// Get oldest mature UTXO (coinbase needs to be 100+ blocks old)
		utxo, err := bm.database.GetOldestMatureUTXO(bm.keyManager.GetAddress(), currentHeight)
		if err != nil {
			fmt.Printf("No mature UTXOs available: %v (current height: %d)\n", err, currentHeight)
			fmt.Printf("Waiting for new UTXOs or coinbase maturity...\n")
			time.Sleep(30 * time.Second)
			continue
		}

		// Log UTXO type
		utxoType := "split"
		if utxo.IsCoinbase {
			utxoType = "coinbase"
		}

		// Calculate maturity status for logging
		maturityInfo := ""
		if utxo.IsCoinbase {
			blocksUntilMature := int(utxo.BlockHeight+100) - int(currentHeight)
			if blocksUntilMature > 0 {
				maturityInfo = fmt.Sprintf(" (WARNING: needs %d more blocks for maturity)", blocksUntilMature)
			} else {
				maturityInfo = fmt.Sprintf(" (mature: %d blocks old)", currentHeight-utxo.BlockHeight)
			}
		}

		fmt.Printf("\n[%s] Using %s UTXO %s:%d (%d sats, height %d, current %d)%s\n",
			time.Now().Format("15:04:05"), utxoType, utxo.TxHash[:8], utxo.Vout, utxo.Amount, utxo.BlockHeight, currentHeight, maturityInfo)

		// Mark UTXO as spent immediately to prevent reuse
		err = bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
		if err != nil {
			log.Printf("Warning: Failed to mark UTXO as spent before use: %v", err)
		}

		// Create many transactions from this UTXO
		bm.blastFromUTXO(utxo)

		// Small delay between batches
		time.Sleep(100 * time.Millisecond)
	}
}

func (bm *BlastManager) blastFromUTXO(utxo *models.UTXO) {
	// Increment attempt counter at the start
	bm.mu.Lock()
	bm.totalAttempts++
	bm.mu.Unlock()

	// Always verify UTXO exists before attempting to use it
	// This prevents issues with stale or incorrect database entries
	if bm.client != nil {
		tx, err := bm.client.GetTransaction(utxo.TxHash)
		if err != nil {
			fmt.Printf("⚠️ Failed to verify UTXO %s:%d - transaction not found: %v\n",
				utxo.TxHash[:8], utxo.Vout, err)
			// If we can't verify, mark as spent and skip
			bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			return
		}

		// Check if our specific output exists and matches
		if int(utxo.Vout) >= len(tx.Outputs) {
			fmt.Printf("❌ UTXO %s:%d - output index out of range (tx has %d outputs)\n",
				utxo.TxHash[:8], utxo.Vout, len(tx.Outputs))
			bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			return
		}

		output := tx.Outputs[utxo.Vout]
		// Verify amount matches
		if output.Satoshis != utxo.Amount {
			fmt.Printf("⚠️ UTXO %s:%d amount mismatch! Database: %d, Teranode: %d - updating database\n",
				utxo.TxHash[:8], utxo.Vout, utxo.Amount, output.Satoshis)
			// Update database with correct amount
			utxo.Amount = output.Satoshis
			bm.database.SaveUTXO(utxo)
		}
	}

	// Check if parent transaction exists and rebuild chain if needed
	if bm.enableRebroadcast && bm.txRebuilder != nil {
		if err := bm.checkAndRebuildParentChain(utxo); err != nil {
			fmt.Printf("Failed to rebuild parent chain for UTXO %s:%d: %v\n",
				utxo.TxHash[:8], utxo.Vout, err)
			// Mark UTXO as spent since we can't use it
			bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			return
		}
	}

	// Calculate how many transactions to create
	txToCreate := bm.txPerBatch

	// Use the chained transactions approach which we know worked before
	fmt.Printf("Creating %d chained transactions from UTXO %s:%d with %d sats...\n",
		txToCreate, utxo.TxHash[:8], utxo.Vout, utxo.Amount)
	transactions, err := bm.builder.BuildManyTransactions(utxo, txToCreate)
	if err != nil {
		fmt.Printf("Failed to build transactions: %v\n", err)
		// In debug mode, show more details about the failure
		if bm.debugMode {
			fmt.Printf("DEBUG - UTXO details: TxHash=%s, Vout=%d, Amount=%d sats\n",
				utxo.TxHash, utxo.Vout, utxo.Amount)
		}

		// Check if this is an insufficient funds error
		if strings.Contains(err.Error(), "insufficient funds") {
			fmt.Printf("UTXO too small for transaction creation, attempting consolidation...\n")

			// Calculate consolidation threshold (enough for at least one transaction with fees)
			threshold := blaster.CalculateConsolidationThreshold(txToCreate)

			// Get small UTXOs to consolidate (limit to 100 to avoid too large transaction)
			smallUTXOs, dbErr := bm.database.GetSmallUTXOs(bm.keyManager.GetAddress(), threshold, 100)
			if dbErr != nil {
				fmt.Printf("Failed to get small UTXOs for consolidation: %v\n", dbErr)
				bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
				return
			}

			// Include the current UTXO in consolidation if not already included
			foundCurrent := false
			for _, u := range smallUTXOs {
				if u.TxHash == utxo.TxHash && u.Vout == utxo.Vout {
					foundCurrent = true
					break
				}
			}
			if !foundCurrent {
				smallUTXOs = append([]*models.UTXO{utxo}, smallUTXOs...)
			}

			if len(smallUTXOs) < 2 {
				fmt.Printf("Not enough small UTXOs to consolidate (found %d)\n", len(smallUTXOs))
				bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
				return
			}

			fmt.Printf("Found %d small UTXOs to consolidate\n", len(smallUTXOs))

			// Build consolidation transaction
			consolidationTx, buildErr := bm.builder.BuildConsolidationTransaction(smallUTXOs)
			if buildErr != nil {
				fmt.Printf("Failed to build consolidation transaction: %v\n", buildErr)
				bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
				return
			}

			// Mark all input UTXOs as spent
			for _, u := range smallUTXOs {
				if markErr := bm.database.MarkUTXOAsSpent(u.TxHash, u.Vout); markErr != nil {
					log.Printf("Warning: Failed to mark UTXO %s:%d as spent: %v", u.TxHash[:8], u.Vout, markErr)
				}
			}

			// Get the raw transaction hex for logging
			txHex, hexErr := blaster.GetTransactionHex(consolidationTx)
			if hexErr != nil {
				fmt.Printf("Failed to serialize consolidation transaction: %v\n", hexErr)
				return
			}
			
			// Output the raw consolidation transaction hex
			fmt.Printf("\n╔══════════════════════════════════════════╗\n")
			fmt.Printf("║   CONSOLIDATION TRANSACTION HEX          ║\n")
			fmt.Printf("╠══════════════════════════════════════════╣\n")
			fmt.Printf("║ TxID: %s\n", consolidationTx.TxID())
			fmt.Printf("║ Size: %d bytes\n", consolidationTx.Size())
			fmt.Printf("║ Inputs: %d\n", len(consolidationTx.Inputs))
			fmt.Printf("║ Output Amount: %d sats\n", consolidationTx.Outputs[1].Satoshis)
			fmt.Printf("╠══════════════════════════════════════════╣\n")
			fmt.Printf("║ Raw Hex (copy below):\n")
			fmt.Printf("╚══════════════════════════════════════════╝\n")
			fmt.Printf("%s\n\n", txHex)
			
			// Broadcast consolidation transaction
			fmt.Printf("Broadcasting consolidation transaction...\n")
			var consolidationTxID string
			if bm.useProp && bm.db != nil {
				// Use direct batch broadcaster for consolidation

				// Note: txHex already obtained above
				txIDs, errs, batchErr := bm.db.BroadcastTransactionBatch([]string{txHex})
				if batchErr != nil || (len(errs) > 0 && errs[0] != nil) {
					errMsg := "unknown error"
					if batchErr != nil {
						errMsg = batchErr.Error()
					} else if len(errs) > 0 && errs[0] != nil {
						errMsg = errs[0].Error()
					}
					fmt.Printf("Failed to broadcast consolidation transaction: %s\n", errMsg)
					// Restore UTXOs as unspent on failure
					for _, u := range smallUTXOs {
						u.Spent = false
						bm.database.SaveUTXO(u)
					}
					return
				}
				if len(txIDs) > 0 {
					consolidationTxID = txIDs[0]
				}
			} else {
				// Use regular broadcaster
				// Note: txHex already obtained above
				var broadcastErr error
				consolidationTxID, broadcastErr = bm.bc.BroadcastTransaction(txHex)
				if broadcastErr != nil {
					fmt.Printf("Failed to broadcast consolidation transaction: %v\n", broadcastErr)
					// Restore UTXOs as unspent on failure
					for _, u := range smallUTXOs {
						u.Spent = false
						bm.database.SaveUTXO(u)
					}
					return
				}
			}

			fmt.Printf("✓ Consolidation successful! TxID: %s\n", consolidationTxID)
			fmt.Printf("  Consolidated %d UTXOs into 1 output with %d sats\n", len(smallUTXOs), consolidationTx.Outputs[1].Satoshis)

			// Get current block height for saving
			consolidationHeight, err := bm.database.GetCurrentBlockHeight()
			if err != nil {
				log.Printf("Warning: Failed to get current block height: %v", err)
				consolidationHeight = utxo.BlockHeight // Fallback to UTXO's height
			}

			// Save the consolidated UTXO (index 1, since index 0 is OP_RETURN)
			consolidatedUTXO := &models.UTXO{
				TxHash:      consolidationTxID,
				Vout:        1, // Index 1 is the value output
				Amount:      consolidationTx.Outputs[1].Satoshis,
				BlockHeight: consolidationHeight + 1, // Use next block height
				Address:     bm.keyManager.GetAddress(),
				Spent:       false,
				IsCoinbase:  false,
			}

			if saveErr := bm.database.SaveUTXO(consolidatedUTXO); saveErr != nil {
				log.Printf("Warning: Failed to save consolidated UTXO: %v", saveErr)
			}

			fmt.Printf("Created consolidated UTXO with %d sats, continuing with blast...\n", consolidatedUTXO.Amount)

			// Recursively try again with the consolidated UTXO
			bm.blastFromUTXO(consolidatedUTXO)
			return
		}

		// Mark UTXO as spent anyway to skip it
		bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
		return
	}

	// Check if any transaction IDs already exist before broadcasting
	// This prevents conflicts and ensures we don't try to create duplicate transactions
	if bm.client != nil {
		duplicatesFound := false
		for i, tx := range transactions {
			txID := tx.TxID().String()
			// Only check first few transactions to avoid too many API calls
			if i < 3 {
				exists := false
				if bm.txRebuilder != nil {
					var checkErr error
					exists, checkErr = bm.txRebuilder.CheckTransactionExists(txID)
					if checkErr != nil {
						// Ignore error, assume doesn't exist
						exists = false
					}
				}
				if exists {
					fmt.Printf("⚠️  Transaction %d already exists with ID %s\n", i, txID[:8])
					duplicatesFound = true
					break
				}
			}
		}

		if duplicatesFound {
			fmt.Printf("❌ Duplicate transaction IDs detected! This UTXO may have already been used.\n")
			bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			return
		}
	}

	fmt.Printf("Created %d chained transactions, broadcasting...\n", len(transactions))
	bm.broadcastBatch(utxo, transactions)
}

func (bm *BlastManager) broadcastBatch(utxo *models.UTXO, transactions []*transaction.Transaction) {
	bm.broadcastBatchWithRetries(utxo, transactions, 0)
}

func (bm *BlastManager) broadcastBatchWithRetries(utxo *models.UTXO, transactions []*transaction.Transaction, retryCount int) {

	successCount := 0
	var lastTxID string
	batchStartTime := time.Now()
	validationFailed := false
	coinbaseImmature := false

	// Broadcast transactions - use batching if propagation is enabled
	if bm.useProp && bm.db != nil {
		// Prepare all transaction hexes for batch submission
		txHexes := make([]string, 0, len(transactions))
		for i, tx := range transactions {
			txHexStr, err := blaster.GetTransactionHex(tx)
			if err != nil {
				fmt.Printf("Failed to serialize transaction %d: %v\n", i, err)
				bm.incrementFailures()
				continue
			}
			txHexes = append(txHexes, txHexStr)
		}

		if len(txHexes) > 0 {
			fmt.Printf("Submitting batch of %d chained transactions via direct gRPC...\n", len(txHexes))
			startTime := time.Now()

			// Submit entire batch at once using direct batch API
			txIDs, errs, err := bm.db.BroadcastTransactionBatch(txHexes)

			elapsed := time.Since(startTime)
			tps := float64(len(txHexes)) / elapsed.Seconds()
			fmt.Printf("Batch submission took %v (%.2f TPS)\n", elapsed, tps)
			if err != nil {
				fmt.Printf("Batch submission failed: %v\n", err)
				for range txHexes {
					bm.incrementFailures()
				}
				// Dump transaction hex in debug mode
				if bm.debugMode {
					fmt.Printf("\n=== DEBUG: Transaction Hex Dump ===\n")
					for i, hex := range txHexes {
						if i < 3 { // Only show first 3 transactions to avoid spam
							fmt.Printf("Transaction %d:\n%s\n\n", i, hex)
						}
					}
					if len(txHexes) > 3 {
						fmt.Printf("... and %d more transactions\n", len(txHexes)-3)
					}
					fmt.Printf("===================================\n")
				}
			} else {
				// Process results
				duplicates := 0
				failures := 0

				for i, txErr := range errs {
					if txErr != nil {
						// Check if the error is because the transaction already exists
						errStr := txErr.Error()
						if strings.Contains(errStr, "BLOB_EXISTS") || strings.Contains(errStr, "already exists") {
							duplicates++
							successCount++
							if i < len(txIDs) {
								lastTxID = txIDs[i]
							}
							bm.incrementSuccess()
						} else if strings.Contains(errStr, "TX_MISSING_PARENT") ||
							strings.Contains(errStr, "TX_NOT_FOUND") ||
							strings.Contains(errStr, "error getting parent transaction") {
							// Parent transaction is missing - this UTXO is invalid
							validationFailed = true
							fmt.Printf("  🔴 Transaction has missing parent (TX_MISSING_PARENT): parent transaction not found in blockchain\n")
							// Stop processing batch since this UTXO won't work
							failures++
							bm.incrementFailures()
							break
						} else if strings.Contains(errStr, "TX_COINBASE_IMMATURE") ||
							strings.Contains(errStr, "coinbase is locked") ||
							strings.Contains(errStr, "Coinbase UTXO can only be spent when it matures") {
							// Coinbase is not mature yet - don't mark as spent, just skip
							coinbaseImmature = true
							fmt.Printf("  🔴 Coinbase UTXO is immature, needs to mature before spending\n")
							failures++
							bm.incrementFailures()
							break
						} else if (strings.Contains(errStr, "TX_INVALID") ||
							strings.Contains(errStr, "ScriptVerifierGoBDK fail") ||
							strings.Contains(errStr, "Signature must be zero") ||
							strings.Contains(errStr, "failed to validate transaction")) {
							// Transaction failed validation for other reasons
							if i == 0 {
								// First transaction failed validation
								fmt.Printf("  🔴 Transaction validation failed: %v\n", txErr)
							} else {
								fmt.Printf("  🔴 Transaction %d validation failed: %v\n", i, txErr)
							}
							// Always dump the raw hex for validation failures to help debugging
							if i < len(txHexes) {
								fmt.Printf("\n=== VALIDATION FAILURE - Raw Transaction Hex ===\n")
								fmt.Printf("Transaction %d hex:\n%s\n", i, txHexes[i])
								fmt.Printf("=================================================\n\n")
							}
							failures++
							bm.incrementFailures()
							if i == 0 {
								break // Stop processing since all will fail if first transaction failed
							}
						} else {
							failures++
							if failures <= 5 { // Only show first 5 errors to avoid spam
								fmt.Printf("  Transaction %d failed: %v\n", i, txErr)
								// Dump hex for failed transaction in debug mode
								if bm.debugMode && i < len(txHexes) {
									fmt.Printf("  DEBUG - Transaction %d hex:\n  %s\n", i, txHexes[i])
								}
							}
							bm.incrementFailures()
						}
					} else {
						successCount++
						if i < len(txIDs) {
							lastTxID = txIDs[i]
						}
						bm.incrementSuccess()
					}

					// Show progress every 100 transactions
					if (i+1)%100 == 0 || i == len(errs)-1 {
						fmt.Printf("  Progress: %d/%d processed (✓ %d, ⚠ %d duplicates, ✗ %d failures)\r",
							i+1, len(errs), successCount-duplicates, duplicates, failures)
					}
				}
				fmt.Println() // New line after progress

				if failures > 5 {
					fmt.Printf("  ... and %d more failures\n", failures-5)
				}

				// Transaction successful - outputs will be saved later
			}
		}
	} else {
		// Non-batched broadcasting for RPC
		for i, tx := range transactions {
			txHexStr, err := blaster.GetTransactionHex(tx)
			if err != nil {
				fmt.Printf("Failed to serialize transaction %d: %v\n", i, err)
				bm.incrementFailures()
				break
			}

			// Broadcast transaction
			txID, err := bm.bc.BroadcastTransaction(txHexStr)
			if err != nil {
				errStr := err.Error()
				// Check if this is a validation error
				if strings.Contains(errStr, "TX_INVALID") ||
					strings.Contains(errStr, "ScriptVerifierGoBDK fail") ||
					strings.Contains(errStr, "Signature must be zero") ||
					strings.Contains(errStr, "failed to validate transaction") {
					if i == 0 {
						validationFailed = true
						fmt.Printf("🔴 Transaction validation failed (likely missing parent): %v\n", err)
					} else {
						fmt.Printf("🔴 Transaction %d validation failed: %v\n", i, err)
					}
					// Always dump hex for validation failures
					fmt.Printf("\n=== VALIDATION FAILURE - Raw Transaction Hex ===\n")
					fmt.Printf("Transaction %d hex:\n%s\n", i, txHexStr)
					fmt.Printf("=================================================\n\n")
				} else {
					fmt.Printf("Failed to broadcast tx %d: %v\n", i, err)
					// Dump hex in debug mode for other errors
					if bm.debugMode {
						fmt.Printf("DEBUG - Transaction %d hex:\n%s\n", i, txHexStr)
					}
				}
				bm.incrementFailures()
				break
			}

			successCount++
			lastTxID = txID
			bm.incrementSuccess()

			// No need to save change output since we're creating split transactions now

			// Small delay between transactions
			if i < len(transactions)-1 {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}

	// coinbaseImmature flag was already set if TX_COINBASE_IMMATURE error occurred

	// Calculate and display batch statistics
	batchElapsed := time.Since(batchStartTime)
	batchTPS := float64(successCount) / batchElapsed.Seconds()
	successRate := float64(successCount) / float64(len(transactions)) * 100

	fmt.Printf("\n=== BATCH RESULTS ===\n")
	fmt.Printf("Total time: %v\n", batchElapsed.Round(time.Millisecond))
	fmt.Printf("Success: %d/%d (%.1f%%)\n", successCount, len(transactions), successRate)
	fmt.Printf("Batch TPS: %.2f transactions/sec\n", batchTPS)

	// Handle UTXO state based on success
	// Only save change UTXO if we had significant success (at least 90% success rate)
	// This prevents saving UTXOs from partially failed batches that might have incorrect state
	minSuccessRateForSave := 90.0
	if successCount > 0 && successRate >= minSuccessRateForSave {
		// UTXO was already marked as spent at the beginning, just save the new output
		// Save the last transaction's output as a new UTXO for the next batch
		if len(transactions) > 0 && lastTxID != "" {
			lastTx := transactions[len(transactions)-1]
			// The second output (index 1) has the remaining value (index 0 is OP_RETURN)
			if lastTx.Outputs[1].Satoshis > 1 {
				// Verify the transaction actually exists before saving its output
				// This prevents saving UTXOs from transactions that didn't actually get accepted
				exists := false
				if bm.txRebuilder != nil {
					var checkErr error
					exists, checkErr = bm.txRebuilder.CheckTransactionExists(lastTxID)
					if checkErr != nil {
						log.Printf("Warning: Failed to verify transaction %s exists: %v", lastTxID, checkErr)
						// Don't save UTXO if we can't verify it exists
						exists = false
					}
				} else {
					// If we don't have txRebuilder, assume it exists since we got success
					exists = true
				}

				if exists {
					// Get current block height for new UTXO
					newBlockHeight, err := bm.database.GetCurrentBlockHeight()
					if err != nil {
						log.Printf("Warning: Failed to get current block height: %v", err)
						newBlockHeight = utxo.BlockHeight // Fallback to UTXO's height
					}

					newUTXO := &models.UTXO{
						TxHash:      lastTxID,
						Vout:        1, // Index 1 is the value output (index 0 is OP_RETURN)
						Amount:      lastTx.Outputs[1].Satoshis,
						BlockHeight: newBlockHeight + 1, // Use next block height
						Address:     bm.keyManager.GetAddress(),
						Spent:       false,
						IsCoinbase:  false,
					}
					err = bm.database.SaveUTXO(newUTXO)
					if err != nil {
						log.Printf("Warning: Failed to save new UTXO: %v", err)
					} else {
						fmt.Printf("Saved verified change UTXO with %d sats for next batch\n", newUTXO.Amount)
					}
				} else {
					fmt.Printf("⚠️  Not saving change UTXO - transaction %s not found in blockchain\n", lastTxID[:8])
				}
			}
		}
	} else if successCount > 0 {
		fmt.Printf("⚠️  Not saving change UTXO - success rate too low (%.1f%% < 90%%)\n", successRate)
	} else {
		// Check if this was a TX_MISSING_PARENT error
		if validationFailed {
			// The parent transaction doesn't exist, so this UTXO is invalid
			// Keep it marked as spent and move on
			fmt.Printf("⚠️  UTXO parent transaction missing, marking as invalid and moving to next UTXO\n")
		} else if coinbaseImmature {
			// Coinbase is not mature yet, restore UTXO to try again later
			utxo.Spent = false
			err := bm.database.SaveUTXO(utxo)
			if err != nil {
				log.Printf("Warning: Failed to unmark UTXO as spent after coinbase immaturity: %v", err)
			}
			fmt.Printf("⚠️  Coinbase UTXO is not mature yet, will retry later\n")
		} else {
			// Other type of failure, restore UTXO as unspent to retry later
			utxo.Spent = false
			err := bm.database.SaveUTXO(utxo)
			if err != nil {
				log.Printf("Warning: Failed to unmark UTXO as spent after failure: %v", err)
			}
			fmt.Printf("⚠️  No transactions were successfully broadcast, UTXO restored as unspent\n")
		}
	}

	if lastTxID != "" {
		fmt.Printf("Last TxID: %s\n", lastTxID)
	}
	fmt.Printf("=====================\n")

	// If validation failed due to missing parent, don't try to rebuild
	// The UTXO has already been marked as spent, just move on to the next one
	if validationFailed {
		fmt.Printf("📝 Moving to next UTXO since this one has a missing parent transaction\n")
		return
	}

	// For other types of failures with chain rebuilding enabled, try to rebuild parent chain
	if bm.enableRebroadcast && bm.txRebuilder != nil && successCount == 0 {
		// Check retry limit to prevent infinite loops
		const maxRetries = 3
		if retryCount >= maxRetries {
			fmt.Printf("❌ Maximum retry attempts (%d) reached for UTXO %s:%d\n", maxRetries, utxo.TxHash[:8], utxo.Vout)
			fmt.Printf("⚠️  This UTXO appears to be invalid or missing from the blockchain\n")
			// Mark UTXO as spent to skip it in future iterations
			bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
			return
		}

		fmt.Printf("\n🔄 Attempting to rebuild parent chain for UTXO %s:%d (retry %d/%d)...\n",
			utxo.TxHash[:8], utxo.Vout, retryCount+1, maxRetries)

		if err := bm.checkAndRebuildParentChain(utxo); err != nil {
			fmt.Printf("❌ Failed to rebuild parent chain: %v\n", err)
			// If we can't rebuild the chain, it likely doesn't exist
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "transaction not found in txstore") {
				fmt.Printf("⚠️  UTXO %s:%d appears to be invalid, marking as spent\n", utxo.TxHash[:8], utxo.Vout)
				bm.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)
				return
			}
			// For other errors, try again
		}

		fmt.Printf("🔁 Retrying transaction broadcast after rebuilding parent chain...\n")

		// Retry the broadcast with incremented counter
		bm.broadcastBatchWithRetries(utxo, transactions, retryCount+1)
		return
	}
}

func (bm *BlastManager) periodicSync() {
	ticker := time.NewTicker(bm.syncInterval)
	defer ticker.Stop()

	for range ticker.C {
		fmt.Printf("\n[%s] Starting periodic sync...\n", time.Now().Format("15:04:05"))

		// Scan recent blocks for new coinbase UTXOs
		err := bm.scanner.ScanRecentBlocks(10)
		if err != nil {
			log.Printf("Periodic sync failed: %v", err)
		} else {
			fmt.Printf("Periodic sync completed\n")
		}
	}
}

func (bm *BlastManager) reportStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	lastReportTxSent := 0
	lastReportTime := time.Now()

	for range ticker.C {
		bm.mu.Lock()
		runtime := time.Since(bm.startTime)
		overallTPS := float64(bm.totalTxSent) / runtime.Seconds()

		// Calculate interval TPS (last 30 seconds)
		intervalTxSent := bm.totalTxSent - lastReportTxSent
		intervalTime := time.Since(lastReportTime)
		intervalTPS := float64(intervalTxSent) / intervalTime.Seconds()

		successRate := float64(bm.totalTxSent) / float64(bm.totalTxSent+bm.totalFailures) * 100
		if bm.totalTxSent+bm.totalFailures == 0 {
			successRate = 0
		}

		// Calculate transactions per minute for better readability
		txPerMinute := overallTPS * 60
		intervalTxPerMinute := intervalTPS * 60

		bm.mu.Unlock()

		fmt.Printf("\n╔══════════════════════════════════════════╗\n")
		fmt.Printf("║         BLAST STATISTICS REPORT          ║\n")
		fmt.Printf("╠══════════════════════════════════════════╣\n")
		fmt.Printf("║ Runtime:           %-22s║\n", runtime.Round(time.Second))
		fmt.Printf("║ Total TX sent:     %-22d║\n", bm.totalTxSent)
		fmt.Printf("║ Total failures:    %-22d║\n", bm.totalFailures)
		fmt.Printf("║ Success rate:      %-21.1f%%║\n", successRate)
		fmt.Printf("╠══════════════════════════════════════════╣\n")
		fmt.Printf("║ Overall TPS:       %-22.2f║\n", overallTPS)
		fmt.Printf("║ Overall TX/min:    %-22.0f║\n", txPerMinute)
		fmt.Printf("║ Last 30s TPS:      %-22.2f║\n", intervalTPS)
		fmt.Printf("║ Last 30s TX/min:   %-22.0f║\n", intervalTxPerMinute)
		fmt.Printf("╚══════════════════════════════════════════╝\n\n")

		// Update for next interval
		lastReportTxSent = bm.totalTxSent
		lastReportTime = time.Now()
	}
}

func (bm *BlastManager) incrementSuccess() {
	bm.mu.Lock()
	bm.totalTxSent++
	bm.mu.Unlock()
}

func (bm *BlastManager) incrementFailures() {
	bm.mu.Lock()
	bm.totalFailures++
	bm.mu.Unlock()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// checkAndRebuildParentChain checks if a UTXO's parent transaction exists and rebuilds the chain if needed
func (bm *BlastManager) checkAndRebuildParentChain(utxo *models.UTXO) error {
	if bm.txRebuilder == nil {
		return nil
	}

	// Check if parent transaction exists
	exists, err := bm.txRebuilder.CheckTransactionExists(utxo.TxHash)
	if err != nil {
		return fmt.Errorf("failed to check parent transaction: %w", err)
	}

	if exists {
		// Parent exists, we're good to go
		return nil
	}

	// Parent transaction is missing - need to rebuild the chain
	fmt.Printf("\n🔄 Parent transaction %s is missing, rebuilding chain...\n", utxo.TxHash[:8])

	// Build the complete transaction chain
	chain := make(map[string][]string)
	visited := make(map[string]bool)

	if err := bm.txRebuilder.BuildTransactionChain(utxo.TxHash, chain, visited, 0); err != nil {
		return fmt.Errorf("failed to build transaction chain: %w", err)
	}

	if len(chain) == 0 {
		return fmt.Errorf("could not build transaction chain (transaction not found in txstore)")
	}

	fmt.Printf("Found %d transactions in dependency chain\n", len(chain))

	// Sort transactions in dependency order
	sortedTxs, err := txchain.TopologicalSort(chain)
	if err != nil {
		return fmt.Errorf("failed to sort transaction chain: %w", err)
	}

	fmt.Printf("Rebroadcasting %d transactions in dependency order...\n", len(sortedTxs))

	// Rebroadcast the chain
	successful, failed, errors := bm.txRebuilder.RebroadcastChain(sortedTxs)

	if failed > 0 {
		fmt.Printf("⚠️ Chain rebuilding: %d successful, %d failed\n", successful, failed)
		if len(errors) > 0 {
			return fmt.Errorf("failed to rebuild complete chain: %v", errors[0])
		}
	} else {
		fmt.Printf("✅ Successfully rebuilt transaction chain (%d transactions)\n", successful)
	}

	// Give some time for transactions to propagate
	if successful > 0 {
		time.Sleep(2 * time.Second)
	}

	return nil
}

func (bm *BlastManager) Stop() {
	if bm.pb != nil {
		bm.pb.Stop()
	}
	if bm.db != nil {
		bm.db.Stop()
	}
}
