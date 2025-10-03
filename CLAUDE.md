# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

TX Blaster is a BSV blockchain stress testing tool that creates and broadcasts large volumes of chained transactions to test teranode performance. It tracks UTXOs from coinbase transactions, creates chains of 1000+ transactions, and submits them via batch gRPC calls.

## Build and Run Commands

```bash
# Build the application
go build -o tx-blaster cmd/tx-blaster/*.go

# Or build all commands
go build -o tx-blaster cmd/tx-blaster/main.go

# Run tests (if any)
go test ./...

# Run with help
./tx-blaster --help

# Common development workflow
./tx-blaster scan --key YOUR_WIF_KEY        # Scan for UTXOs
./tx-blaster list --key YOUR_WIF_KEY        # List found UTXOs
./tx-blaster blast --key YOUR_WIF_KEY       # Start blasting transactions
./tx-blaster verify --key YOUR_WIF_KEY      # Verify UTXO states against teranode

# Check status of a specific transaction
./tx-blaster tx-status --txid TRANSACTION_HASH                     # Without key (no ownership check)
./tx-blaster tx-status --txid TRANSACTION_HASH --key YOUR_WIF_KEY  # With key (checks ownership and local DB)

# Start blasting from a specific UTXO
./tx-blaster blast --key YOUR_WIF_KEY --from-tx TRANSACTION_HASH --vout OUTPUT_INDEX

# Blast at a fixed rate from a large UTXO (splits into many outputs first)
./tx-blaster blast-from-tx --key YOUR_WIF_KEY --tx TRANSACTION_HASH --vout 0 --rate 100 --outputs 10000
# This command:
# 1. Takes the specified UTXO (usually 50 BSV coinbase)
# 2. Splits it into many outputs (default 500000, but limited by tx size to ~3000)
# 3. Continuously creates zero-fee chained transactions at the specified rate
```

## Architecture

### Transaction Strategy
The blaster creates **chained transactions** where each transaction spends the output of the previous one. This allows generating 1000 transactions from a single UTXO:
- Transaction 1 spends original UTXO → creates output A
- Transaction 2 spends output A → creates output B  
- Transaction 3 spends output B → creates output C
- ... continues for 1000 transactions

All 1000 chained transactions are submitted in a single batch gRPC call for efficiency.

### Key Components

**Command Layer** (`cmd/tx-blaster/`)
- `main.go` - CLI entry point using Cobra
- `blast.go` - BlastManager orchestrates continuous transaction creation/broadcasting
- `verify.go` - Verifies UTXOs against teranode/aerospike
- `tx-status.go` - Checks transaction output status and detects inconsistencies

**Transaction Building** (`internal/blaster/`)
- `simple_builder.go` - Creates chained transactions (`BuildManyTransactions`)
- `builder.go` - Creates split transactions with many outputs
- `batch_builder.go` - Optimizes for large split transactions

**Broadcasting** (`internal/broadcaster/`)
- `batch_propagation.go` - DirectBatchBroadcaster uses gRPC `ProcessTransactionBatch` API
- `propagation.go` - PropagationBroadcaster uses teranode propagation client
- `broadcaster.go` - Basic RPC broadcaster

**Data Layer** (`internal/db/`)
- SQLite database tracks UTXOs with `is_coinbase` flag
- Prioritizes coinbase UTXOs (100+ blocks old for maturity)
- Stores: tx_hash, vout, amount, block_height, address, spent, is_coinbase

### Teranode Integration

The project has a local replace directive for teranode:
```go
replace github.com/bitcoin-sv/teranode => /git/teranode
```

Key teranode services used:
- **Asset HTTP API** (`http://localhost:8080`) - Block/UTXO queries
- **Propagation gRPC** (`localhost:8084`) - Batch transaction submission via `ProcessTransactionBatch`
- **Propagation HTTP** (`http://localhost:8833`) - Fallback for large transactions

### Transaction Features

1. **OP_RETURN Tracking**: All transactions include `OP_RETURN "Who is John Galt?"` as the **FIRST OUTPUT (index 0)**
   - This is critical for transaction identification
   - Value outputs start at index 1
   - Chained transactions reference output index 1 for the next UTXO
2. **Coinbase Maturity**: Enforces 100-block maturity for coinbase UTXOs
3. **Batch Submission**: Sends up to 1000 transactions in one gRPC call
4. **Chain Preservation**: Last transaction's output at index 1 saved as new UTXO for next batch

## Important Configuration

Default ports and endpoints:
- Teranode Asset API: `http://localhost:8080`
- Propagation gRPC: `localhost:8084`  
- Propagation HTTP: `http://localhost:8833`
- RPC (fallback): `http://localhost:9292`
- Database: `utxos.db` (SQLite)

## Critical Implementation Details

### Batch Transaction Submission
The `DirectBatchBroadcaster` in `batch_propagation.go` directly calls the gRPC `ProcessTransactionBatch` method. This is crucial for performance - it sends all 1000 chained transactions in a single network call rather than queuing them individually.

### UTXO Management
- Coinbase UTXOs are tracked with `is_coinbase=true`
- `GetOldestMatureUTXO` prioritizes coinbase UTXOs first
- UTXOs marked spent only after successful broadcast
- Block height stored as `current + 1000000` for new UTXOs to prevent immediate reuse

### Error Handling
- TX_MISSING_PARENT errors indicate transaction dependency issues
- Batch failures don't mark UTXOs as spent
- Individual transaction errors tracked separately in batch response

### UTXO Verification
The `verify` command provides comprehensive UTXO state verification:
- Uses `/api/v1/utxos/:txid/json` to get transaction outputs and check spent status
- Verifies ownership by checking locking scripts
- Identifies missing parent transactions that prevent UTXO spending
- Supports rebroadcasting missing transactions from local txstore
- Automatically fixes database inconsistencies

### Transaction Status Checking
The `tx-status` command provides detailed transaction output analysis:
- Queries transaction by ID and checks all outputs
- Shows spent/unspent status for each output
- Identifies spending transactions for spent outputs
- Compares aerospike data with local database (when key provided)
- Reports inconsistencies between data sources
- Supports operation without private key for general queries

## Statistics and Monitoring

The blaster provides comprehensive real-time statistics:

### Batch Processing Stats
- Per-batch TPS calculation during submission
- Real-time progress counter showing successes, duplicates, and failures
- Batch results summary with success rate and timing

### Periodic Statistics Report (every 30 seconds)
- Overall TPS and transactions per minute
- Interval TPS (last 30 seconds performance)
- Success rate percentage
- Formatted in a clear box layout for easy reading

### Final Summary
- Detailed iteration limit report with all metrics
- Total runtime and average performance

## Common Issues and Solutions

1. **TX_MISSING_PARENT**: Transactions have dependencies not yet in mempool
   - Solution: Ensure all chained transactions submitted in single batch
   
2. **Slow batch submission**: Using wrong API or sequential submission
   - Solution: Use DirectBatchBroadcaster for direct gRPC batch calls

3. **Database migration errors**: Adding columns to existing database
   - Solution: Migration code suppresses "column already exists" errors

4. **BLOB_EXISTS errors**: Transaction already exists in mempool/blockchain
   - Solution: Treat as success (see blast.go lines 251-257)

5. **Transaction validation failures**: When transactions fail validation (TX_INVALID, ScriptVerifierGoBDK fail, etc.)
   - The blast command will automatically dump the raw transaction hex for debugging
   - This helps identify malformed transactions or missing parent issues
   - Use `--debug` flag for additional verbose output on all errors

## Development History and TODOs

### Completed Tasks
1. ✅ Explore teranode asset HTTP service API
2. ✅ Design TX blaster architecture
3. ✅ Set up Go project with BSV SDK
4. ✅ Implement private key handling for testnet/mainnet
5. ✅ Create SQLite database for UTXO tracking
6. ✅ Create CLI interface
7. ✅ Refactor scan to use block headers and hash-based queries
8. ✅ Test with testnet key to validate UTXO discovery
9. ✅ Implement transaction builder for UTXO splitting
10. ✅ Add database method to get oldest UTXO
11. ✅ Create broadcaster module for teranode
12. ✅ Add split command to CLI
13. ✅ Add broadcast command to CLI
14. ✅ Add blast command for continuous sending
15. ✅ Update UTXO database after broadcasting transactions
16. ✅ Add is_coinbase field to UTXO model
17. ✅ Update database schema with is_coinbase column
18. ✅ Update scanner to mark coinbase UTXOs
19. ✅ Modify GetOldestUTXO to prioritize coinbase UTXOs
20. ✅ Update split and blast commands to use coinbase UTXOs first
21. ✅ Add coinbase maturity check (100 blocks)
22. ✅ Implement periodic UTXO syncing during blast
23. ✅ Optimize blast to create many transactions from single UTXO
24. ✅ Add OP_RETURN output with 'Who is John Galt?' to all transactions
25. ✅ Use direct batch gRPC API for submitting transactions
26. ✅ Add iterations option to blaster for testing
27. ✅ Add verify command to check UTXOs against teranode/aerospike
28. ✅ Confirm UTXOs are not marked spent on broadcast failure
29. ✅ Increase batch size from 100 to 1000 transactions
30. ✅ Revert to chained transactions with proper batch submission

### Pending Tasks
- 🔄 Optimize periodic sync to scan from last coinbase height, not from 0
  - Currently scans recent blocks only, could track last scanned height for efficiency