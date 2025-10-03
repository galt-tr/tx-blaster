package scanner

import (
	"encoding/hex"
	"fmt"
	"log"
	"strings"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/tx-blaster/tx-blaster/internal/db"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/internal/teranode"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

type Scanner struct {
	client     *teranode.Client
	database   *db.Database
	keyManager *keys.KeyManager
}

func NewScanner(client *teranode.Client, database *db.Database, keyManager *keys.KeyManager) *Scanner {
	return &Scanner{
		client:     client,
		database:   database,
		keyManager: keyManager,
	}
}

func (s *Scanner) ScanAllBlocks() error {
	log.Printf("Scanning ALL blocks from genesis for UTXOs belonging to address: %s", s.keyManager.GetAddress())

	// Get best block header to determine current height
	bestHeader, err := s.client.GetBestBlockHeader()
	if err != nil {
		return fmt.Errorf("failed to get best block header: %w", err)
	}

	log.Printf("Current blockchain height: %d", bestHeader.Height)
	log.Printf("Will scan from height 1 to %d (skipping genesis)", bestHeader.Height)

	foundUTXOs := 0
	skippedBlocks := 0
	processedBlocks := 0

	// Scan from height 1 (skip genesis) to current height
	for height := uint32(1); height <= bestHeader.Height; height++ {
		if height%100 == 0 {
			log.Printf("Progress: Scanning block at height %d (%.1f%%) - Found %d UTXOs, Processed %d blocks, Skipped %d",
				height, float64(height)/float64(bestHeader.Height)*100, foundUTXOs, processedBlocks, skippedBlocks)
		}

		// Get full block data directly by height
		block, err := s.client.GetBlockByHeight(height)
		if err != nil {
			// Block might be missing or orphaned, skip it
			if height%100 != 0 { // Only log non-progress heights
				log.Printf("Could not get block at height %d (likely missing or orphaned): %v", height, err)
			}
			skippedBlocks++
			continue
		}

		processedBlocks++

		// Process coinbase transaction
		if block.CoinbaseTx != nil {
			count, err := s.processCoinbaseTransaction(block.CoinbaseTx, height)
			if err != nil {
				log.Printf("Failed to process coinbase tx at height %d: %v", height, err)
			} else if count > 0 {
				foundUTXOs += count
				log.Printf("✓ Found %d UTXO(s) at height %d", count, height)
			}
		}
	}

	log.Printf("\n=== Scan Complete ===")
	log.Printf("Total blocks processed: %d", processedBlocks)
	log.Printf("Blocks skipped (missing/orphaned): %d", skippedBlocks)
	log.Printf("Total UTXOs found: %d", foundUTXOs)

	return nil
}

func (s *Scanner) ScanRecentBlocks(numBlocks int) error {
	log.Printf("Scanning last %d blocks for UTXOs belonging to address: %s", numBlocks, s.keyManager.GetAddress())
	
	// Get best block header to determine current height
	bestHeader, err := s.client.GetBestBlockHeader()
	if err != nil {
		return fmt.Errorf("failed to get best block header: %w", err)
	}
	
	log.Printf("Current blockchain height: %d", bestHeader.Height)
	
	// Start from the current height and go backwards
	startHeight := bestHeader.Height
	endHeight := startHeight - uint32(numBlocks) + 1
	if endHeight < 0 {
		endHeight = 0
	}
	
	return s.ScanBlockRange(endHeight, startHeight)
}

func (s *Scanner) ScanBlockRange(startHeight, endHeight uint32) error {
	log.Printf("Scanning blocks from height %d to %d for address: %s", 
		startHeight, endHeight, s.keyManager.GetAddress())
	
	// Calculate how many blocks we need
	numBlocks := int(endHeight - startHeight + 1)
	
	// Get best block header to determine current height
	bestHeader, err := s.client.GetBestBlockHeader()
	if err != nil {
		return fmt.Errorf("failed to get best block header: %w", err)
	}
	
	if endHeight > bestHeader.Height {
		log.Printf("Warning: end height %d exceeds current blockchain height %d", endHeight, bestHeader.Height)
		endHeight = bestHeader.Height
	}
	
	foundUTXOs := 0
	
	// We need to fetch blocks in batches from the blocks endpoint
	// The blocks endpoint returns blocks in descending order from the tip
	offset := int(bestHeader.Height - endHeight)
	
	for currentHeight := endHeight; currentHeight >= startHeight; {
		// Calculate how many blocks to fetch in this batch
		limit := 100 // Max blocks per request
		if numBlocks < limit {
			limit = numBlocks
		}
		
		// Get blocks info
		blocksResp, err := s.client.GetBlocks(limit, offset)
		if err != nil {
			return fmt.Errorf("failed to get blocks list: %w", err)
		}
		
		for _, blockInfo := range blocksResp.Data {
			if blockInfo.Height < startHeight {
				break
			}
			
			if blockInfo.Height > endHeight {
				continue
			}
			
			if blockInfo.Orphaned {
				log.Printf("Skipping orphaned block at height %d", blockInfo.Height)
				continue
			}
			
			log.Printf("Scanning block at height %d", blockInfo.Height)

			// Get full block data directly by height
			block, err := s.client.GetBlockByHeight(blockInfo.Height)
			if err != nil {
				log.Printf("Could not find valid block at height %d, skipping: %v", blockInfo.Height, err)
				continue
			}
			
			// Set the height from the blocks response
			if block.Height == 0 {
				block.Height = blockInfo.Height
			}
			
			// Process coinbase transaction
			if block.CoinbaseTx != nil {
				count, err := s.processCoinbaseTransaction(block.CoinbaseTx, block.Height)
				if err != nil {
					log.Printf("Failed to process coinbase tx at height %d: %v", block.Height, err)
				} else {
					foundUTXOs += count
				}
			}
			
			currentHeight = blockInfo.Height - 1
		}
		
		// Move to next batch
		offset += limit
		numBlocks -= limit
		
		if numBlocks <= 0 {
			break
		}
	}
	
	log.Printf("Scan complete. Found %d UTXOs", foundUTXOs)
	return nil
}

func (s *Scanner) processCoinbaseTransaction(tx *teranode.Transaction, blockHeight uint32) (int, error) {
	if tx == nil {
		return 0, fmt.Errorf("transaction is nil")
	}
	
	// Ensure we have a transaction ID
	txID := tx.TxID
	if txID == "" {
		// If TxID is not set, we need to calculate it or skip
		log.Printf("Warning: Coinbase transaction at height %d has no TxID", blockHeight)
		return 0, nil
	}
	
	foundCount := 0
	targetAddress := s.keyManager.GetAddress()
	
	// Check each output
	for vout, output := range tx.Outputs {
		// Parse the locking script to get the address
		scriptBytes, err := hex.DecodeString(output.LockingScript)
		if err != nil {
			log.Printf("Failed to decode locking script: %v", err)
			continue
		}
		
		lockingScript := script.Script(scriptBytes)
		
		// Check if this is a P2PKH script
		if lockingScript.IsP2PKH() {
			// Extract address from script
			addr, err := lockingScript.Address()
			if err != nil {
				continue
			}
			
			// Convert to testnet address if needed
			addressStr := addr.AddressString
			if s.keyManager.IsTestnet() {
				// Address() returns mainnet by default, need to convert
				pkh, _ := lockingScript.PublicKeyHash()
				testAddr, _ := script.NewAddressFromPublicKeyHash(pkh, false)
				if testAddr != nil {
					addressStr = testAddr.AddressString
				}
			}
			
			if addressStr == targetAddress {
				log.Printf("Found UTXO: tx=%s, vout=%d, amount=%d sats, height=%d", 
					txID, vout, output.Satoshis, blockHeight)
				
				// Save to database
				utxo := &models.UTXO{
					TxHash:      txID,
					Vout:        uint32(vout),
					Amount:      output.Satoshis,
					BlockHeight: blockHeight,
					Address:     addressStr,
					Spent:       false,
					IsCoinbase:  true,
				}
				
				if err := s.database.SaveUTXO(utxo); err != nil {
					log.Printf("Failed to save UTXO: %v", err)
				} else {
					foundCount++
				}
			}
		} else if lockingScript.IsP2PK() {
			// Handle P2PK scripts (common in coinbase)
			// Extract public key and compare
			pubKey, err := lockingScript.PubKey()
			if err == nil {
				// Create address from public key
				isMainnet := !s.keyManager.IsTestnet()
				addr, err := script.NewAddressFromPublicKey(pubKey, isMainnet)
				if err == nil {
					if addr.AddressString == targetAddress {
						log.Printf("Found P2PK UTXO: tx=%s, vout=%d, amount=%d sats, height=%d", 
							txID, vout, output.Satoshis, blockHeight)
						
						utxo := &models.UTXO{
							TxHash:      txID,
							Vout:        uint32(vout),
							Amount:      output.Satoshis,
							BlockHeight: blockHeight,
							Address:     addr.AddressString,
							Spent:       false,
							IsCoinbase:  true,
						}
						
						if err := s.database.SaveUTXO(utxo); err != nil {
							log.Printf("Failed to save UTXO: %v", err)
						} else {
							foundCount++
						}
					}
				}
			}
		}
	}
	
	return foundCount, nil
}


func (s *Scanner) GetUTXOSummary() error {
	address := s.keyManager.GetAddress()
	utxos, err := s.database.GetUnspentUTXOs(address)
	if err != nil {
		return fmt.Errorf("failed to get UTXOs: %w", err)
	}
	
	totalBalance, err := s.database.GetTotalBalance(address)
	if err != nil {
		return fmt.Errorf("failed to get balance: %w", err)
	}
	
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("UTXO Summary for Address: %s\n", address)
	fmt.Printf("Network: %s\n", s.keyManager.GetNetwork())
	fmt.Println(strings.Repeat("-", 80))
	
	if len(utxos) == 0 {
		fmt.Println("No unspent UTXOs found")
	} else {
		fmt.Printf("Found %d unspent UTXOs\n", len(utxos))
		fmt.Printf("Total Balance: %d satoshis (%.8f BSV)\n", totalBalance, float64(totalBalance)/1e8)
		
		// Calculate average UTXO size
		avgSize := totalBalance / uint64(len(utxos))
		fmt.Printf("Average UTXO size: %d satoshis\n", avgSize)
		
		// Show if these can be used for blasting
		if len(utxos) > 0 {
			oldestUTXO := utxos[0]
			for _, u := range utxos {
				if u.BlockHeight < oldestUTXO.BlockHeight {
					oldestUTXO = u
				}
			}
			fmt.Printf("Oldest UTXO: Block %d, %d sats (will be used for next split)\n", 
				oldestUTXO.BlockHeight, oldestUTXO.Amount)
		}
		
		fmt.Println(strings.Repeat("-", 80))
		
		// Show first 10 UTXOs in detail
		maxShow := 10
		if len(utxos) < maxShow {
			maxShow = len(utxos)
		}
		
		for i := 0; i < maxShow; i++ {
			utxo := utxos[i]
			fmt.Printf("%d. TX: %s:%d\n", i+1, utxo.TxHash, utxo.Vout)
			fmt.Printf("   Amount: %d sats | Block Height: %d\n", utxo.Amount, utxo.BlockHeight)
		}
		
		if len(utxos) > maxShow {
			fmt.Printf("\n... and %d more UTXOs\n", len(utxos)-maxShow)
		}
	}
	
	fmt.Println(strings.Repeat("=", 80))
	return nil
}