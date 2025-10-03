package teranode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Block struct {
	Header           *BlockHeader `json:"header"`
	CoinbaseTx       *Transaction `json:"coinbase_tx"`
	TransactionCount uint64       `json:"transaction_count"`
	SizeInBytes      uint64       `json:"size_in_bytes"`
	Height           uint32       `json:"height"`
	ID               uint32       `json:"id"`
}

type BlockHeader struct {
	Version        uint32 `json:"version"`
	HashPrevBlock  string `json:"hash_prev_block"`
	HashMerkleRoot string `json:"hash_merkle_root"`
	Timestamp      uint32 `json:"timestamp"`
	Bits           string `json:"bits"`
	Nonce          uint32 `json:"nonce"`
}

type Transaction struct {
	Inputs   []Input  `json:"inputs"`
	Outputs  []Output `json:"outputs"`
	Version  uint32   `json:"version"`
	LockTime uint32   `json:"lockTime"`
	TxID     string   `json:"txid"`
}

// IsCoinbase returns true if this is a coinbase transaction
func (t *Transaction) IsCoinbase() bool {
	// A coinbase transaction has exactly one input with a null hash (all zeros) and index 0xffffffff
	if len(t.Inputs) != 1 {
		return false
	}
	
	input := t.Inputs[0]
	// Check if previous tx is all zeros or empty (coinbase marker)
	return input.PreviousTxID == "" || 
		input.PreviousTxID == "0000000000000000000000000000000000000000000000000000000000000000" ||
		(input.PreviousTxID == "" && input.PreviousTxIndex == 0xffffffff)
}

type Input struct {
	PreviousTxID    string `json:"previousTxId"`
	PreviousTxIndex uint32 `json:"previousTxOutIndex"`
	UnlockingScript string `json:"unlockingScript"`
	SequenceNumber  uint32 `json:"sequenceNumber"`
}

type Output struct {
	Satoshis      uint64 `json:"satoshis"`
	LockingScript string `json:"lockingScript"`
}

type BlocksResponse struct {
	Data       []BlockInfo `json:"data"`
	Pagination struct {
		Offset       int `json:"offset"`
		Limit        int `json:"limit"`
		TotalRecords int `json:"total_records"`
	} `json:"pagination"`
}

type BlockInfo struct {
	SeenAt           string `json:"seen_at"`
	Height           uint32 `json:"height"`
	Orphaned         bool   `json:"orphaned"`
	BlockHeader      string `json:"block_header"`
	Hash             string `json:"hash,omitempty"` // Some endpoints might include this
	Miner            string `json:"miner"`
	CoinbaseValue    uint64 `json:"coinbase_value"`
	TransactionCount uint64 `json:"transaction_count"`
	Size             uint64 `json:"size"`
}

type BestBlockHeader struct {
	Version        uint32 `json:"version"`
	HashPrevBlock  string `json:"hash_prev_block"`
	HashMerkleRoot string `json:"hash_merkle_root"`
	Timestamp      uint32 `json:"timestamp"`
	Bits           string `json:"bits"`
	Nonce          uint32 `json:"nonce"`
	Hash           string `json:"hash"`
	Height         uint32 `json:"height"`
	TxCount        uint64 `json:"tx_count"`
	SizeInBytes    uint64 `json:"size_in_bytes"`
	Miner          string `json:"miner"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) GetBlockByHeight(height uint32) (*Block, error) {
	// First try the direct height endpoint
	url := fmt.Sprintf("%s/api/v1/block/height/%d/json", c.baseURL, height)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var block Block
		if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
			return nil, fmt.Errorf("failed to decode block: %w", err)
		}
		// Ensure height is set
		if block.Height == 0 {
			block.Height = height
		}
		return &block, nil
	}

	// If direct height endpoint fails, try using blocks list API
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusInternalServerError {
		// Get best block height first
		bestHeader, err := c.GetBestBlockHeader()
		if err != nil {
			return nil, fmt.Errorf("failed to get best block header: %w", err)
		}

		if height > bestHeader.Height {
			return nil, fmt.Errorf("height %d exceeds current blockchain height %d", height, bestHeader.Height)
		}

		// Calculate offset from tip
		offset := int(bestHeader.Height - height)

		// Get blocks at this offset
		blocksResp, err := c.GetBlocks(10, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to get blocks list: %w", err)
		}

		// Find a non-orphaned block at the requested height
		for _, blockInfo := range blocksResp.Data {
			if blockInfo.Height == height && !blockInfo.Orphaned {
				// We have block header, now get the full block
				if blockInfo.Hash != "" {
					return c.GetBlockByHash(blockInfo.Hash)
				}
				// Calculate hash from header if not provided
				if blockInfo.BlockHeader != "" {
					blockHeaderBytes, err := hex.DecodeString(blockInfo.BlockHeader)
					if err != nil {
						continue
					}
					hash := c.calculateBlockHash(blockHeaderBytes)
					block, err := c.GetBlockByHash(hash)
					if err == nil {
						if block.Height == 0 {
							block.Height = height
						}
						return block, nil
					}
				}
			}
		}

		return nil, fmt.Errorf("no valid block found at height %d", height)
	}

	body, _ := io.ReadAll(resp.Body)
	return nil, fmt.Errorf("failed to get block at height %d: status=%d, body=%s", height, resp.StatusCode, string(body))
}

func (c *Client) GetBlocks(limit int, offset int) (*BlocksResponse, error) {
	url := fmt.Sprintf("%s/api/v1/blocks?limit=%d&offset=%d", c.baseURL, limit, offset)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get blocks: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var blocksResp BlocksResponse
	if err := json.NewDecoder(resp.Body).Decode(&blocksResp); err != nil {
		return nil, fmt.Errorf("failed to decode blocks response: %w", err)
	}

	return &blocksResp, nil
}

func (c *Client) GetBestBlockHeader() (*BestBlockHeader, error) {
	url := fmt.Sprintf("%s/api/v1/bestblockheader/json", c.baseURL)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get best block header: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get best block header: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var header BestBlockHeader
	if err := json.NewDecoder(resp.Body).Decode(&header); err != nil {
		return nil, fmt.Errorf("failed to decode best block header: %w", err)
	}

	return &header, nil
}

func (c *Client) GetBlockByHash(hash string) (*Block, error) {
	url := fmt.Sprintf("%s/api/v1/block/%s/json", c.baseURL, hash)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get block by hash: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get block by hash: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var block Block
	if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
		return nil, fmt.Errorf("failed to decode block: %w", err)
	}

	return &block, nil
}

// GetBlockHeaderByHeight gets just the block header at a specific height
func (c *Client) GetBlockHeaderByHeight(height uint32) (*BlockHeader, string, error) {
	// First, get blocks at this height
	// We need to calculate offset from the tip
	bestHeader, err := c.GetBestBlockHeader()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get best block header: %w", err)
	}

	if height > bestHeader.Height {
		return nil, "", fmt.Errorf("height %d exceeds current blockchain height %d", height, bestHeader.Height)
	}

	offset := int(bestHeader.Height - height)

	// Get multiple blocks at this height to handle potential orphans
	// Request up to 10 blocks at this offset to find one on the main chain
	url := fmt.Sprintf("%s/api/v1/blocks?limit=10&offset=%d", c.baseURL, offset)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get block info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("failed to get block info: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var blocksResp BlocksResponse
	if err := json.NewDecoder(resp.Body).Decode(&blocksResp); err != nil {
		return nil, "", fmt.Errorf("failed to decode blocks response: %w", err)
	}

	if len(blocksResp.Data) == 0 {
		return nil, "", fmt.Errorf("no block found at height %d", height)
	}

	// Try each block returned to find one that's on the main chain
	for _, blockInfo := range blocksResp.Data {
		if blockInfo.Height != height {
			continue // Skip blocks at wrong height
		}
		
		// If hash is provided in the response, verify it exists
		if blockInfo.Hash != "" {
			// Quick check if this block exists (not orphaned)
			// We'll let the scanner handle the actual verification
			return nil, blockInfo.Hash, nil
		}
		
		// Otherwise, return the header bytes for hash calculation
		if blockInfo.BlockHeader != "" {
			header := &BlockHeader{
				// We'd need to parse the bytes, but for now just return the hash
			}
			return header, blockInfo.BlockHeader, nil
		}
	}
	
	// If we couldn't find a valid block at this height
	return nil, "", fmt.Errorf("no valid block found at height %d (all may be orphaned)", height)
}

func (c *Client) GetTransaction(txHash string) (*Transaction, error) {
	url := fmt.Sprintf("%s/api/v1/tx/%s/json", c.baseURL, txHash)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get transaction: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var tx Transaction
	if err := json.NewDecoder(resp.Body).Decode(&tx); err != nil {
		return nil, fmt.Errorf("failed to decode transaction: %w", err)
	}

	return &tx, nil
}

// calculateBlockHash calculates the block hash from header bytes
func (c *Client) calculateBlockHash(headerBytes []byte) string {
	// Bitcoin block hash is double SHA256 of the header
	firstHash := sha256.Sum256(headerBytes)
	secondHash := sha256.Sum256(firstHash[:])

	// Reverse the byte order for display (Bitcoin uses little-endian)
	reversed := make([]byte, len(secondHash))
	for i := 0; i < len(secondHash); i++ {
		reversed[i] = secondHash[len(secondHash)-1-i]
	}

	return hex.EncodeToString(reversed)
}
