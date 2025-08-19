package models

import "time"

type UTXO struct {
	TxHash      string    `json:"tx_hash"`
	Vout        uint32    `json:"vout"`
	Amount      uint64    `json:"amount"`
	BlockHeight uint32    `json:"block_height"`
	Address     string    `json:"address"`
	Spent       bool      `json:"spent"`
	IsCoinbase  bool      `json:"is_coinbase"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}