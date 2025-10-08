package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/internal/blaster"
	"github.com/tx-blaster/tx-blaster/internal/broadcaster"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/internal/logger"
	"github.com/tx-blaster/tx-blaster/internal/teranode"
	"github.com/tx-blaster/tx-blaster/internal/ui"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

type BlastFromTxManager struct {
	keyManager       *keys.KeyManager
	database         *db.Database
	builder          *blaster.Builder
	broadcaster      *broadcaster.DirectBatchBroadcaster
	client           *teranode.Client
	txHash           string
	vout             uint32
	numOutputs       int
	rate             int // transactions per second
	splitUTXOs       []*models.UTXO
	currentUTXOIndex int
	mu               sync.Mutex

	// UI component
	terminal    ui.Terminal
	useSimpleUI bool

	// Control flags
	isPaused  atomic.Bool
	rateMu    sync.RWMutex
	stopCh    chan struct{}

	// Statistics
	totalSent      atomic.Int64
	totalFailures  atomic.Int64
	totalDuplicate atomic.Int64
	startTime      time.Time
	currentUTXO    string
}

func NewBlastFromTxManager(
	keyManager *keys.KeyManager,
	database *db.Database,
	teranodeURL string,
	propagationGRPC string,
	propagationHTTP string,
	txHash string,
	vout uint32,
	numOutputs int,
	rate int,
) (*BlastFromTxManager, error) {
	builder := blaster.NewBuilder(keyManager)

	broadcaster, err := broadcaster.NewDirectBatchBroadcaster(propagationGRPC)
	if err != nil {
		return nil, fmt.Errorf("failed to create broadcaster: %w", err)
	}

	client := teranode.NewClient(teranodeURL)

	return &BlastFromTxManager{
		keyManager:  keyManager,
		database:    database,
		builder:     builder,
		broadcaster: broadcaster,
		client:      client,
		txHash:      txHash,
		vout:        vout,
		numOutputs:  numOutputs,
		rate:        rate,
		splitUTXOs:  make([]*models.UTXO, 0),
		startTime:   time.Now(),
		stopCh:      make(chan struct{}),
	}, nil
}

func (m *BlastFromTxManager) SetSimpleUI(useSimple bool) {
	m.useSimpleUI = useSimple
}

func (m *BlastFromTxManager) Start() {
	// Initialize terminal UI (simple mode for Warp, tview for others)
	m.terminal = ui.NewTerminal(m.useSimpleUI)

	// Suppress only log output if using simple UI to prevent screen corruption
	// Keep stdout for the UI display
	if m.useSimpleUI {
		logger.SuppressLogsOnly()
		defer logger.RestoreAll()
	}

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start statistics updater first to ensure UI is ready
	go m.updateStatsLoop()

	// Start processing in background
	go m.startProcessing()

	// Handle keyboard events in background
	go m.handleKeyboard()

	// Handle signals in background
	go func() {
		<-sigChan
		m.gracefulShutdown()
	}()

	// Run the UI (this blocks)
	m.terminal.Run()
}

func (m *BlastFromTxManager) startProcessing() {
	m.terminal.ShowMessage(fmt.Sprintf("Starting blast from TX %s", m.txHash[:16]+"..."))

	// Step 1: Get the UTXO
	utxo, err := m.getOrFetchUTXO()
	if err != nil {
		m.terminal.ShowMessage(fmt.Sprintf("Failed to get UTXO: %v", err))
		time.Sleep(2 * time.Second)
		m.gracefulShutdown()
		return
	}

	m.terminal.ShowMessage(fmt.Sprintf("Found UTXO with %d satoshis", utxo.Amount))

	// Step 2: Create massive split transaction
	m.terminal.ShowMessage(fmt.Sprintf("Creating split with %d outputs...", m.numOutputs))
	splitTx, actualOutputs, err := m.builder.BuildMassiveSplitTransaction(utxo, m.numOutputs)
	if err != nil {
		m.terminal.ShowMessage(fmt.Sprintf("Failed to create split: %v", err))
		time.Sleep(2 * time.Second)
		m.gracefulShutdown()
		return
	}

	m.terminal.ShowMessage(fmt.Sprintf("Created split: %d outputs, %d bytes", actualOutputs, splitTx.Size()))

	// Step 3: Broadcast the split transaction
	m.terminal.ShowMessage("Broadcasting split transaction...")
	txHex, err := blaster.GetTransactionHex(splitTx)
	if err != nil {
		m.terminal.ShowMessage(fmt.Sprintf("Failed to get tx hex: %v", err))
		time.Sleep(2 * time.Second)
		m.gracefulShutdown()
		return
	}

	txIDs, errs, batchErr := m.broadcaster.BroadcastTransactionBatch([]string{txHex})
	if batchErr != nil || (len(errs) > 0 && errs[0] != nil) {
		errMsg := "unknown error"
		if batchErr != nil {
			errMsg = batchErr.Error()
		} else if len(errs) > 0 && errs[0] != nil {
			errMsg = errs[0].Error()
		}
		m.terminal.ShowMessage(fmt.Sprintf("Failed: %s", errMsg))
		time.Sleep(2 * time.Second)
		m.gracefulShutdown()
		return
	}

	m.terminal.ShowMessage(fmt.Sprintf("✅ Split TX: %s", txIDs[0][:16]+"..."))

	// Mark original UTXO as spent (no logging)
	m.database.MarkUTXOAsSpent(utxo.TxHash, utxo.Vout)

	// Step 4: Track the new UTXOs (no logging)
	for i := 1; i <= actualOutputs; i++ {
		newUTXO := &models.UTXO{
			TxHash:      splitTx.TxID().String(),
			Vout:        uint32(i),
			Amount:      splitTx.Outputs[i].Satoshis,
			BlockHeight: 0,
			Address:     m.keyManager.GetAddress(),
			Spent:       false,
			IsCoinbase:  false,
		}
		m.splitUTXOs = append(m.splitUTXOs, newUTXO)
		m.database.SaveUTXO(newUTXO)
	}

	m.terminal.ShowMessage(fmt.Sprintf("📊 Blasting %d UTXOs at %d TPS", len(m.splitUTXOs), m.rate))

	// Start blasting at the specified rate
	m.startRateLimitedBlasting()
}

func (m *BlastFromTxManager) handleKeyboard() {
	for {
		select {
		case <-m.stopCh:
			return
		case <-m.terminal.PauseChannel():
			m.togglePause()
		case newRate := <-m.terminal.RateChannel():
			m.changeRate(newRate)
		case <-m.terminal.QuitChannel():
			m.gracefulShutdown()
			return
		}
	}
}

func (m *BlastFromTxManager) togglePause() {
	isPaused := m.isPaused.Load()
	m.isPaused.Store(!isPaused)

	if !isPaused {
		m.terminal.ShowMessage("⏸  Blasting paused")
	} else {
		m.terminal.ShowMessage("▶  Blasting resumed")
	}
}

func (m *BlastFromTxManager) changeRate(newRate int) {
	m.rateMu.Lock()
	oldRate := m.rate
	m.rate = newRate
	m.rateMu.Unlock()

	m.terminal.ShowMessage(fmt.Sprintf("📊 Rate changed from %d to %d TPS", oldRate, newRate))
}

func (m *BlastFromTxManager) getCurrentRate() int {
	m.rateMu.RLock()
	defer m.rateMu.RUnlock()
	return m.rate
}

func (m *BlastFromTxManager) updateStatsLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	// Initial update to ensure UI shows immediately
	m.updateUI()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.updateUI()
		}
	}
}

func (m *BlastFromTxManager) updateUI() {
	stats := ui.Stats{
		TotalSent:      m.totalSent.Load(),
		TotalFailures:  m.totalFailures.Load(),
		TotalDuplicate: m.totalDuplicate.Load(),
		CurrentUTXO:    m.currentUTXO,
		CurrentRate:    m.getCurrentRate(),
		TargetRate:     m.getCurrentRate(),
		StartTime:      m.startTime,
		IsPaused:       m.isPaused.Load(),
	}

	m.terminal.UpdateStats(stats)
}

func (m *BlastFromTxManager) getOrFetchUTXO() (*models.UTXO, error) {
	// First, try to get from database
	utxo, err := m.database.GetUTXOByTxHashAndVout(m.txHash, m.vout)
	if err == nil {
		return utxo, nil
	}

	// Not in database, fetch from teranode
	txDetails, err := m.client.GetTransaction(m.txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch transaction: %w", err)
	}

	if int(m.vout) >= len(txDetails.Outputs) {
		return nil, fmt.Errorf("invalid output index %d (transaction has %d outputs)", m.vout, len(txDetails.Outputs))
	}

	output := txDetails.Outputs[m.vout]
	utxo = &models.UTXO{
		TxHash:      m.txHash,
		Vout:        m.vout,
		Amount:      output.Satoshis,
		BlockHeight: 0,
		Address:     m.keyManager.GetAddress(),
		Spent:       false,
		IsCoinbase:  false,
	}

	// Save to database
	m.database.SaveUTXO(utxo)
	return utxo, nil
}

func (m *BlastFromTxManager) startRateLimitedBlasting() {
	// Main batch submission loop
	go func() {
		for {
			// Check for stop signal
			select {
			case <-m.stopCh:
				return
			default:
			}

			// Skip if paused
			if m.isPaused.Load() {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Get current rate
			currentRate := m.getCurrentRate()
			if currentRate <= 0 {
				currentRate = 1
			}

			// Track the start of this second
			secondStart := time.Now()

			// Calculate batches needed per second
			var batchesPerSecond int
			var txPerBatch int

			if currentRate <= 1000 {
				// Single batch per second
				batchesPerSecond = 1
				txPerBatch = currentRate
			} else {
				// Multiple batches of 1000
				batchesPerSecond = (currentRate + 999) / 1000 // Round up
				txPerBatch = 1000
			}

			// Calculate interval between batch starts
			batchInterval := time.Second / time.Duration(batchesPerSecond)

			// Process batches for this second
			for b := 0; b < batchesPerSecond; b++ {
				batchStartTime := secondStart.Add(time.Duration(b) * batchInterval)

				// Wait until it's time to start this batch
				waitTime := time.Until(batchStartTime)
				if waitTime > 0 {
					time.Sleep(waitTime)
				}

				// Check for stop or rate change
				select {
				case <-m.stopCh:
					return
				default:
				}

				if m.getCurrentRate() != currentRate {
					break // Rate changed, recalculate
				}

				// Skip if paused
				if m.isPaused.Load() {
					continue
				}

				// For the last batch, adjust count if needed
				actualBatchSize := txPerBatch
				if b == batchesPerSecond-1 && currentRate%1000 != 0 && currentRate > 1000 {
					actualBatchSize = currentRate % 1000
				}

				// Submit batch
				m.submitBatch(actualBatchSize)
			}

			// Wait for the remainder of this second before starting the next
			elapsed := time.Since(secondStart)
			if elapsed < time.Second {
				time.Sleep(time.Second - elapsed)
			}
		}
	}()
}

func (m *BlastFromTxManager) submitBatch(batchSize int) {
	// Check if we have UTXOs
	m.mu.Lock()
	if len(m.splitUTXOs) == 0 {
		m.mu.Unlock()
		m.terminal.ShowMessage("No more UTXOs available for blasting")
		time.Sleep(2 * time.Second)
		m.gracefulShutdown()
		return
	}
	m.mu.Unlock()

	// Build all transactions for this batch
	transactions := make([]*transaction.Transaction, 0, batchSize)
	txHexes := make([]string, 0, batchSize)
	utxoUpdates := make(map[int]*models.UTXO) // Track UTXO updates by index

	for i := 0; i < batchSize; i++ {
		// Get next UTXO in round-robin fashion
		m.mu.Lock()
		if len(m.splitUTXOs) == 0 {
			m.mu.Unlock()
			break
		}

		utxoIndex := m.currentUTXOIndex
		utxo := m.splitUTXOs[utxoIndex]
		m.currentUTXO = fmt.Sprintf("%s:%d", utxo.TxHash[:16], utxo.Vout)
		m.currentUTXOIndex = (m.currentUTXOIndex + 1) % len(m.splitUTXOs)
		m.mu.Unlock()

		// Build transaction
		tx, err := m.buildZeroFeeTransaction(utxo)
		if err != nil {
			m.totalFailures.Add(1)
			continue
		}

		// Get hex
		txHex, err := blaster.GetTransactionHex(tx)
		if err != nil {
			m.totalFailures.Add(1)
			continue
		}

		transactions = append(transactions, tx)
		txHexes = append(txHexes, txHex)

		// Prepare UTXO update (will apply after successful broadcast)
		utxoUpdates[utxoIndex] = &models.UTXO{
			TxHash:      tx.TxID().String(),
			Vout:        1, // Output 1 has the value (0 is OP_RETURN)
			Amount:      tx.Outputs[1].Satoshis,
			BlockHeight: 0,
			Address:     m.keyManager.GetAddress(),
			Spent:       false,
			IsCoinbase:  false,
		}
	}

	// If no transactions were built, return
	if len(txHexes) == 0 {
		return
	}

	// Broadcast the batch
	batchStart := time.Now()
	_, errs, batchErr := m.broadcaster.BroadcastTransactionBatch(txHexes)
	batchDuration := time.Since(batchStart)

	// Process results
	if batchErr != nil {
		// Complete batch failure
		m.totalFailures.Add(int64(len(txHexes)))
		return
	}

	// Check individual transaction results
	successCount := 0
	duplicateCount := 0
	failureCount := 0

	for i, err := range errs {
		if err == nil {
			successCount++
		} else {
			errMsg := err.Error()
			if contains(errMsg, "BLOB_EXISTS") || contains(errMsg, "already exists") {
				duplicateCount++
				// Still treat as success for UTXO update purposes
				successCount++
			} else {
				failureCount++
				// Remove this UTXO update from the map
				for idx, _ := range utxoUpdates {
					if i < len(transactions) && m.splitUTXOs[idx].TxHash == transactions[i].Inputs[0].SourceTXID.String() {
						delete(utxoUpdates, idx)
						break
					}
				}
			}
		}
	}

	// Update statistics
	m.totalSent.Add(int64(successCount))
	m.totalDuplicate.Add(int64(duplicateCount))
	m.totalFailures.Add(int64(failureCount))

	// Show batch submission info (only for larger batches or slow submissions)
	if len(txHexes) >= 10 || batchDuration > 100*time.Millisecond {
		m.terminal.ShowMessage(fmt.Sprintf("📦 Batch: %d txs submitted in %.3fs, Success: %d, Dup: %d, Fail: %d",
			len(txHexes), batchDuration.Seconds(), successCount, duplicateCount, failureCount))
	}

	// Apply successful UTXO updates
	m.mu.Lock()
	for idx, newUTXO := range utxoUpdates {
		if idx < len(m.splitUTXOs) {
			m.splitUTXOs[idx] = newUTXO
		}
	}
	m.mu.Unlock()
}

func (m *BlastFromTxManager) buildZeroFeeTransaction(utxo *models.UTXO) (*transaction.Transaction, error) {
	// Create transaction with no fee for infinite chaining
	tx := transaction.NewTransaction()

	// Add input
	err := m.builder.AddInputFromUTXO(tx, utxo)
	if err != nil {
		return nil, err
	}

	// Add OP_RETURN output (index 0)
	opReturnScript, err := blaster.CreateOpReturnScript("Who is John Galt?")
	if err != nil {
		return nil, err
	}

	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      0,
		LockingScript: opReturnScript,
	})

	// Add value output (index 1) - full amount, no fee
	lockingScript, err := m.builder.CreateP2PKHLockingScript(m.keyManager.GetAddress())
	if err != nil {
		return nil, err
	}

	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      utxo.Amount, // No fee deduction
		LockingScript: lockingScript,
	})

	// Sign transaction
	err = tx.Sign()
	if err != nil {
		return nil, err
	}

	return tx, nil
}

func (m *BlastFromTxManager) gracefulShutdown() {
	close(m.stopCh)
	m.terminal.Stop()
	m.printFinalStats()
	os.Exit(0)
}

func (m *BlastFromTxManager) printFinalStats() {
	sent := m.totalSent.Load()
	failures := m.totalFailures.Load()
	duplicates := m.totalDuplicate.Load()
	runtime := time.Since(m.startTime)

	tps := float64(sent) / runtime.Seconds()
	successRate := float64(sent) * 100 / float64(sent+failures)
	if sent+failures == 0 {
		successRate = 0
	}

	// Print final stats to stdout after UI closes
	fmt.Printf("\n╔══════════════════════════════════════════════╗\n")
	fmt.Printf("║           FINAL STATISTICS                    ║\n")
	fmt.Printf("╠══════════════════════════════════════════════╣\n")
	fmt.Printf("║ Total sent:        %-27d║\n", sent)
	fmt.Printf("║ Total failures:    %-27d║\n", failures)
	fmt.Printf("║ Total duplicates:  %-27d║\n", duplicates)
	fmt.Printf("║ Success rate:      %-25.1f%%║\n", successRate)
	fmt.Printf("╠══════════════════════════════════════════════╣\n")
	fmt.Printf("║ Average TPS:       %-25.2f║\n", tps)
	fmt.Printf("║ Runtime:           %-27s║\n", runtime.Round(time.Second))
	fmt.Printf("╚══════════════════════════════════════════════╝\n\n")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[0:len(substr)] == substr ||
		len(s) >= len(substr) && contains(s[1:], substr)
}

