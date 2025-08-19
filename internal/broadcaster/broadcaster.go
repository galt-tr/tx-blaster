package broadcaster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Broadcaster struct {
	rpcURL     string
	httpClient *http.Client
}

type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  interface{}     `json:"result"`
	Error   *RPCError       `json:"error"`
	ID      int             `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewBroadcaster(rpcURL string) *Broadcaster {
	return &Broadcaster{
		rpcURL: rpcURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BroadcastTransaction sends a raw transaction to the teranode RPC endpoint
func (b *Broadcaster) BroadcastTransaction(txHex string) (string, error) {
	// Create RPC request for sendrawtransaction
	request := RPCRequest{
		JSONRPC: "2.0",
		Method:  "sendrawtransaction",
		Params:  []interface{}{txHex},
		ID:      1,
	}

	// Marshal request to JSON
	reqBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Send HTTP POST request
	resp, err := b.httpClient.Post(b.rpcURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Parse RPC response
	var rpcResp RPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for RPC error
	if rpcResp.Error != nil {
		return "", fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Extract transaction ID from result
	txID, ok := rpcResp.Result.(string)
	if !ok {
		return "", fmt.Errorf("unexpected result type: %T", rpcResp.Result)
	}

	return txID, nil
}

// BroadcastToHTTP sends a transaction using HTTP API endpoint (alternative method)
func (b *Broadcaster) BroadcastToHTTP(baseURL string, txHex string) (string, error) {
	// This method can be used if teranode exposes an HTTP API for transaction submission
	// Format: POST /api/v1/tx or similar
	
	url := fmt.Sprintf("%s/api/v1/tx", baseURL)
	
	// Create request body
	reqBody := map[string]string{
		"rawtx": txHex,
	}
	
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}
	
	// Send HTTP POST request
	resp, err := b.httpClient.Post(url, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	
	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body))
	}
	
	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	
	// Extract transaction ID
	if txID, ok := result["txid"].(string); ok {
		return txID, nil
	}
	
	return "", fmt.Errorf("transaction ID not found in response")
}