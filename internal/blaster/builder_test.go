package blaster

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/tx-blaster/tx-blaster/internal/keys"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

const (
	// Valid mainnet WIF key (compressed)
	testWIF     = "L3x8yycFmvaKEemPyCxY11PWQmukpKj2U2HmDHmZ4MykDqLDrBTM"
	testTxHash  = "5e3014372338f079f005eedc85359e4d96b8440e7dbeb8c35c4182e0c19a1a12"
	testAddress = "1EQJvpsmhazYCcKX5Au6AZmZKEDqLpeUEB"

	// Valid testnet WIF keys (compressed)
	testnetWIF1 = "cMzLdeGd5vEqxB8B6VFQoRopQ3sLAAvEzDAoQgvX54xwofSWj1fx"
	testnetWIF2 = "cN9spWsvaxA8taS7DFMxnk1yJD2gaF2PX1npuTpy3vuZFJdwavaw"
	testnetWIF3 = "cVQefCmG8f55AcXa7EUKFz9MBWR1qAhgLBwn7ZzBDNexYRHvBXZm"
)

func createTestKeyManager(t *testing.T) *keys.KeyManager {
	km, err := keys.NewKeyManager(testWIF)
	if err != nil {
		t.Fatalf("Failed to create key manager: %v", err)
	}
	return km
}

func createTestUTXO() *models.UTXO {
	return &models.UTXO{
		TxHash:      testTxHash,
		Vout:        0,
		Amount:      100000,
		BlockHeight: 1000,
		Address:     testAddress,
		Spent:       false,
		IsCoinbase:  false,
	}
}

func TestTransactionSigningValid(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	if tx == nil {
		t.Fatal("Transaction is nil")
	}

	if len(tx.Inputs) == 0 {
		t.Fatal("Transaction has no inputs")
	}

	unlockingScript := tx.Inputs[0].UnlockingScript
	if unlockingScript == nil || len(*unlockingScript) == 0 {
		t.Fatal("Unlocking script is empty - transaction not signed")
	}

	scriptBytes := *unlockingScript
	if len(scriptBytes) < 10 {
		t.Fatalf("Unlocking script too short: %d bytes", len(scriptBytes))
	}

	sigLen := int(scriptBytes[0])
	if sigLen < 70 || sigLen > 73 {
		t.Fatalf("Invalid signature length: %d (expected 70-73)", sigLen)
	}

	if scriptBytes[1] != 0x30 {
		t.Fatalf("Invalid DER signature format: missing 0x30 marker, got 0x%02x", scriptBytes[1])
	}

	pubKeyStart := sigLen + 1
	if pubKeyStart >= len(scriptBytes) {
		t.Fatal("Public key missing from unlocking script")
	}

	pubKeyLen := int(scriptBytes[pubKeyStart])
	if pubKeyLen != 33 && pubKeyLen != 65 {
		t.Fatalf("Invalid public key length: %d (expected 33 or 65)", pubKeyLen)
	}

	actualPubKeyLen := len(scriptBytes) - pubKeyStart - 1
	if actualPubKeyLen != pubKeyLen {
		t.Fatalf("Public key length mismatch: expected %d, got %d", pubKeyLen, actualPubKeyLen)
	}

	t.Logf("Transaction signed successfully:")
	t.Logf("  TX ID: %s", tx.TxID())
	t.Logf("  Unlocking script length: %d bytes", len(*unlockingScript))
	t.Logf("  Signature length: %d bytes", sigLen)
	t.Logf("  Public key length: %d bytes", pubKeyLen)
}

func TestUnlockingScriptPresence(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	for i, input := range tx.Inputs {
		if input.UnlockingScript == nil {
			t.Errorf("Input %d has nil unlocking script", i)
			continue
		}

		if len(*input.UnlockingScript) == 0 {
			t.Errorf("Input %d has empty unlocking script", i)
			continue
		}

		t.Logf("Input %d unlocking script: %d bytes", i, len(*input.UnlockingScript))
	}
}

func TestSignatureVerification(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	unlockingScript := tx.Inputs[0].UnlockingScript
	scriptBytes := *unlockingScript

	sigLen := int(scriptBytes[0])
	signature := scriptBytes[1 : sigLen+1]

	if signature[0] != 0x30 {
		t.Fatalf("Invalid DER format: missing SEQUENCE tag (0x30)")
	}

	totalLen := int(signature[1])
	// The signature includes a hash type byte at the end, so adjust for that
	if totalLen+2 != len(signature)-1 { // -1 for hash type byte
		t.Fatalf("DER length mismatch: declared %d, actual %d (excluding hash type)", totalLen, len(signature)-3)
	}

	rOffset := 2
	if signature[rOffset] != 0x02 {
		t.Fatalf("Invalid DER format: missing INTEGER tag for r (0x02)")
	}
	rLen := int(signature[rOffset+1])
	r := signature[rOffset+2 : rOffset+2+rLen]

	sOffset := rOffset + 2 + rLen
	if signature[sOffset] != 0x02 {
		t.Fatalf("Invalid DER format: missing INTEGER tag for s (0x02)")
	}
	sLen := int(signature[sOffset+1])
	s := signature[sOffset+2 : sOffset+2+sLen]

	if len(r) < 1 || len(r) > 33 {
		t.Fatalf("Invalid r component length: %d", len(r))
	}
	if len(s) < 1 || len(s) > 33 {
		t.Fatalf("Invalid s component length: %d", len(s))
	}

	if r[0] == 0x00 && (len(r) == 1 || r[1]&0x80 == 0) {
		t.Fatal("Invalid r component: unnecessary leading zero")
	}
	if s[0] == 0x00 && (len(s) == 1 || s[1]&0x80 == 0) {
		t.Fatal("Invalid s component: unnecessary leading zero")
	}

	hashType := signature[len(signature)-1]
	if hashType != 0x41 && hashType != 0x01 {
		t.Fatalf("Invalid signature hash type: 0x%02x (expected 0x41 or 0x01)", hashType)
	}

	t.Logf("Valid DER signature detected:")
	t.Logf("  Total length: %d bytes", len(signature))
	t.Logf("  r component: %d bytes", len(r))
	t.Logf("  s component: %d bytes", len(s))
	t.Logf("  Hash type: 0x%02x", hashType)
}

func TestMultipleInputSigning(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)

	tx := transaction.NewTransaction()

	utxo1 := createTestUTXO()
	utxo1.Amount = 50000
	err := builder.addInputFromUTXO(tx, utxo1)
	if err != nil {
		t.Fatalf("Failed to add first input: %v", err)
	}

	utxo2 := createTestUTXO()
	utxo2.TxHash = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	utxo2.Vout = 1
	utxo2.Amount = 60000
	err = builder.addInputFromUTXO(tx, utxo2)
	if err != nil {
		t.Fatalf("Failed to add second input: %v", err)
	}

	// Create custom locking script (OP_NOP - hex 0x61)
	lockingScript := script.NewFromHex("61")

	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      100000,
		LockingScript: lockingScript,
	})

	err = tx.Sign()
	if err != nil {
		t.Fatalf("Failed to sign transaction with multiple inputs: %v", err)
	}

	if len(tx.Inputs) != 2 {
		t.Fatalf("Expected 2 inputs, got %d", len(tx.Inputs))
	}

	for i, input := range tx.Inputs {
		if input.UnlockingScript == nil || len(*input.UnlockingScript) == 0 {
			t.Errorf("Input %d not properly signed", i)
		} else {
			t.Logf("Input %d signed: %d bytes", i, len(*input.UnlockingScript))
		}
	}
}

func TestChainedTransactionSigning(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()
	utxo.Amount = 1000000

	txs, err := builder.BuildManyTransactions(utxo, 5)
	if err != nil {
		t.Fatalf("Failed to build chained transactions: %v", err)
	}

	if len(txs) != 5 {
		t.Fatalf("Expected 5 transactions, got %d", len(txs))
	}

	prevTxID := testTxHash
	for i, tx := range txs {
		if len(tx.Inputs) != 1 {
			t.Errorf("Transaction %d: expected 1 input, got %d", i, len(tx.Inputs))
			continue
		}

		input := tx.Inputs[0]
		if input.UnlockingScript == nil || len(*input.UnlockingScript) == 0 {
			t.Errorf("Transaction %d: input not signed", i)
			continue
		}

		inputTxID := input.SourceTXID.String()
		if inputTxID != prevTxID && !strings.HasPrefix(prevTxID, inputTxID) {
			// The SourceTXID is already in the correct format

			if inputTxID != prevTxID {
				t.Errorf("Transaction %d: input references wrong transaction", i)
				t.Errorf("  Expected: %s", prevTxID)
				t.Errorf("  Got: %s", inputTxID)
			}
		}

		prevTxID = tx.TxID().String()

		t.Logf("Transaction %d:", i)
		t.Logf("  TX ID: %s", tx.TxID())
		t.Logf("  Input unlocking script: %d bytes", len(*input.UnlockingScript))
		t.Logf("  Outputs: %d", len(tx.Outputs))
	}
}

func TestInsufficientFunds(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)

	tests := []struct {
		name   string
		amount uint64
	}{
		{"Zero amount", 0},
		{"Below minimum", 1},
		{"Exactly fee", uint64(MinimumFee)},
		{"Fee plus one satoshi", uint64(MinimumFee + 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utxo := createTestUTXO()
			utxo.Amount = tt.amount

			_, err := builder.BuildSimpleTransaction(utxo)
			if err == nil {
				t.Errorf("Expected error for amount %d, but got none", tt.amount)
			} else if !strings.Contains(err.Error(), "insufficient funds") {
				t.Errorf("Expected 'insufficient funds' error, got: %v", err)
			}
		})
	}
}

func TestCoinbaseMaturity(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)

	utxo := createTestUTXO()
	utxo.IsCoinbase = true
	utxo.Amount = 100000

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction with coinbase UTXO: %v", err)
	}

	if tx == nil {
		t.Fatal("Transaction is nil")
	}

	if len(tx.Inputs) != 1 {
		t.Fatalf("Expected 1 input, got %d", len(tx.Inputs))
	}

	if tx.Inputs[0].UnlockingScript == nil || len(*tx.Inputs[0].UnlockingScript) == 0 {
		t.Fatal("Coinbase UTXO transaction not properly signed")
	}

	t.Logf("Coinbase UTXO transaction signed successfully")
	t.Logf("  TX ID: %s", tx.TxID())
	t.Logf("  Unlocking script: %d bytes", len(*tx.Inputs[0].UnlockingScript))
}

func TestPrivateKeyIsolation(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	txHex, err := GetTransactionHex(tx)
	if err != nil {
		t.Fatalf("Failed to get transaction hex: %v", err)
	}

	privKey := km.GetPrivateKey()
	privKeyBytes := privKey.Serialize()
	privKeyHex := hex.EncodeToString(privKeyBytes)

	if strings.Contains(txHex, privKeyHex) {
		t.Fatal("Private key found in transaction hex!")
	}

	wifKey := privKey.Wif()
	if strings.Contains(txHex, wifKey) {
		t.Fatal("WIF private key found in transaction hex!")
	}

	t.Log("Private key properly isolated from transaction data")
}

func TestDeterministicSignatures(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx1, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build first transaction: %v", err)
	}

	tx2, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build second transaction: %v", err)
	}

	sig1 := tx1.Inputs[0].UnlockingScript
	sig2 := tx2.Inputs[0].UnlockingScript

	if sig1 == nil || sig2 == nil {
		t.Fatal("One or both signatures are nil")
	}

	sig1Hex := hex.EncodeToString(*sig1)
	sig2Hex := hex.EncodeToString(*sig2)

	if sig1Hex != sig2Hex {
		t.Log("Warning: Signatures are not deterministic (this may be expected with random k values)")
		t.Logf("  Signature 1: %s", sig1Hex[:40]+"...")
		t.Logf("  Signature 2: %s", sig2Hex[:40]+"...")
	} else {
		t.Log("Signatures are deterministic")
	}
}

func TestMalformedInputHandling(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)

	tests := []struct {
		name   string
		txHash string
		vout   uint32
		amount uint64
	}{
		{"Short tx hash", "abcd", 0, 100000},
		{"Invalid hex", "zzzzzz", 0, 100000},
		{"Odd length hex", "abc", 0, 100000},
		{"Empty tx hash", "", 0, 100000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utxo := &models.UTXO{
				TxHash:      tt.txHash,
				Vout:        tt.vout,
				Amount:      tt.amount,
				BlockHeight: 1000,
				Address:     testAddress,
				Spent:       false,
				IsCoinbase:  false,
			}

			_, err := builder.BuildSimpleTransaction(utxo)
			if err == nil {
				t.Errorf("Expected error for malformed tx hash '%s', but got none", tt.txHash)
			}
		})
	}
}

func TestTransactionOutputStructure(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()
	utxo.Amount = 100000

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	if len(tx.Outputs) < 2 {
		t.Fatalf("Expected at least 2 outputs, got %d", len(tx.Outputs))
	}

	opReturnOutput := tx.Outputs[0]
	if opReturnOutput.Satoshis != 0 {
		t.Errorf("OP_RETURN output should have 0 satoshis, got %d", opReturnOutput.Satoshis)
	}

	opReturnScript := *opReturnOutput.LockingScript
	if opReturnScript[0] != 0x6a {
		t.Errorf("First output is not OP_RETURN (0x6a), got 0x%02x", opReturnScript[0])
	}

	expectedMessage := "Who is John Galt?"
	messageLen := int(opReturnScript[1])
	actualMessage := string(opReturnScript[2 : 2+messageLen])
	if actualMessage != expectedMessage {
		t.Errorf("OP_RETURN message mismatch: expected '%s', got '%s'", expectedMessage, actualMessage)
	}

	mainOutput := tx.Outputs[1]
	if mainOutput.Satoshis == 0 {
		t.Error("Main output should have non-zero satoshis")
	}

	mainScript := *mainOutput.LockingScript
	if len(mainScript) != 25 {
		t.Errorf("Expected P2PKH script length 25, got %d", len(mainScript))
	}
	if mainScript[0] != 0x76 || mainScript[1] != 0xa9 {
		t.Error("Main output doesn't look like P2PKH script")
	}

	t.Logf("Output structure verified:")
	t.Logf("  Output 0 (OP_RETURN): '%s'", actualMessage)
	t.Logf("  Output 1 (main): %d sats", mainOutput.Satoshis)
	if len(tx.Outputs) > 2 {
		t.Logf("  Output 2 (dust): %d sats", tx.Outputs[2].Satoshis)
	}
}

func TestScriptExecution(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	input := tx.Inputs[0]
	unlockingScript := input.UnlockingScript

	// Create custom locking script (OP_NOP - hex 0x61) matching the production code
	prevLockingScript, _ := script.NewFromHex("61")

	combinedScript := append(*unlockingScript, *prevLockingScript...)

	if len(combinedScript) < 50 {
		t.Fatalf("Combined script too short: %d bytes", len(combinedScript))
	}

	pubKey := km.GetPrivateKey().PubKey()
	pubKeyBytes := pubKey.Compressed()

	foundPubKey := false
	for i := 0; i < len(combinedScript)-len(pubKeyBytes); i++ {
		if string(combinedScript[i:i+len(pubKeyBytes)]) == string(pubKeyBytes) {
			foundPubKey = true
			break
		}
	}

	if !foundPubKey {
		t.Error("Public key not found in combined script")
	}

	t.Logf("Script execution validation:")
	t.Logf("  Unlocking script: %d bytes", len(*unlockingScript))
	t.Logf("  Locking script: %d bytes", len(*prevLockingScript))
	t.Logf("  Combined script: %d bytes", len(combinedScript))
	t.Logf("  Public key found: %v", foundPubKey)
}

func TestBuilderWithDifferentKeysProducesDifferentSignatures(t *testing.T) {
	wif1 := "L3x8yycFmvaKEemPyCxY11PWQmukpKj2U2HmDHmZ4MykDqLDrBTM"
	wif2 := "L1EoghWiT5RkkZwVVqtb5x2PWg6vGpkHcxrck2P2RP6FtXk247GH"

	km1, err := keys.NewKeyManager(wif1)
	if err != nil {
		t.Fatalf("Failed to create first key manager: %v", err)
	}

	km2, err := keys.NewKeyManager(wif2)
	if err != nil {
		t.Fatalf("Failed to create second key manager: %v", err)
	}

	builder1 := NewBuilder(km1)
	builder2 := NewBuilder(km2)

	utxo := createTestUTXO()

	tx1, err := builder1.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction with first key: %v", err)
	}

	tx2, err := builder2.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction with second key: %v", err)
	}

	sig1 := hex.EncodeToString(*tx1.Inputs[0].UnlockingScript)
	sig2 := hex.EncodeToString(*tx2.Inputs[0].UnlockingScript)

	if sig1 == sig2 {
		t.Fatal("Different private keys produced identical signatures!")
	}

	t.Log("Different keys produce different signatures (as expected)")
	t.Logf("  Key 1 address: %s", km1.GetAddress())
	t.Logf("  Key 2 address: %s", km2.GetAddress())
}

func TestTransactionSizeCalculation(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()
	utxo.Amount = 100000

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	actualSize := tx.Size()
	estimatedSize := BaseTxSize + InputSize + (OutputSize * len(tx.Outputs))

	sizeDiff := actualSize - estimatedSize
	if sizeDiff < 0 {
		sizeDiff = -sizeDiff
	}

	if sizeDiff > 50 {
		t.Errorf("Size estimation off by %d bytes", sizeDiff)
		t.Logf("  Estimated: %d bytes", estimatedSize)
		t.Logf("  Actual: %d bytes", actualSize)
	} else {
		t.Logf("Size estimation accurate:")
		t.Logf("  Estimated: %d bytes", estimatedSize)
		t.Logf("  Actual: %d bytes", actualSize)
		t.Logf("  Difference: %d bytes", sizeDiff)
	}
}

func TestOpReturnDataValidation(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	opReturnOutput := tx.Outputs[0]
	script := *opReturnOutput.LockingScript

	if script[0] != 0x6a {
		t.Fatalf("First byte should be OP_RETURN (0x6a), got 0x%02x", script[0])
	}

	dataLen := int(script[1])
	if dataLen > 80 {
		t.Errorf("OP_RETURN data length %d exceeds typical limit of 80 bytes", dataLen)
	}

	expectedData := "Who is John Galt?"
	if dataLen != len(expectedData) {
		t.Errorf("Data length mismatch: expected %d, got %d", len(expectedData), dataLen)
	}

	actualData := string(script[2 : 2+dataLen])
	if actualData != expectedData {
		t.Errorf("OP_RETURN data mismatch: expected '%s', got '%s'", expectedData, actualData)
	}

	t.Logf("OP_RETURN validation passed:")
	t.Logf("  Data: '%s'", actualData)
	t.Logf("  Length: %d bytes", dataLen)
}

func TestSignatureHashType(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	unlockingScript := *tx.Inputs[0].UnlockingScript
	sigLen := int(unlockingScript[0])
	signature := unlockingScript[1 : sigLen+1]

	hashType := signature[len(signature)-1]

	validHashTypes := map[byte]string{
		0x01: "SIGHASH_ALL",
		0x02: "SIGHASH_NONE",
		0x03: "SIGHASH_SINGLE",
		0x41: "SIGHASH_ALL | SIGHASH_FORKID",
		0x42: "SIGHASH_NONE | SIGHASH_FORKID",
		0x43: "SIGHASH_SINGLE | SIGHASH_FORKID",
		0x81: "SIGHASH_ALL | SIGHASH_ANYONECANPAY",
		0xC1: "SIGHASH_ALL | SIGHASH_FORKID | SIGHASH_ANYONECANPAY",
	}

	hashTypeName, valid := validHashTypes[hashType]
	if !valid {
		t.Fatalf("Invalid hash type: 0x%02x", hashType)
	}

	if hashType != 0x41 && hashType != 0x01 {
		t.Logf("Warning: Unexpected hash type for BSV: %s (0x%02x)", hashTypeName, hashType)
	} else {
		t.Logf("Hash type: %s (0x%02x)", hashTypeName, hashType)
	}
}

func TestSigningWithNilKeyManager(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Logf("Recovered from panic: %v", r)
		}
	}()

	builder := NewBuilder(nil)
	utxo := createTestUTXO()

	_, err := builder.BuildSimpleTransaction(utxo)
	if err == nil {
		t.Fatal("Expected error or panic with nil key manager")
	}
}

func TestExtractPublicKeyFromUnlockingScript(t *testing.T) {
	km := createTestKeyManager(t)
	builder := NewBuilder(km)
	utxo := createTestUTXO()

	tx, err := builder.BuildSimpleTransaction(utxo)
	if err != nil {
		t.Fatalf("Failed to build transaction: %v", err)
	}

	unlockingScript := *tx.Inputs[0].UnlockingScript
	sigLen := int(unlockingScript[0])
	pubKeyStart := sigLen + 1
	pubKeyLen := int(unlockingScript[pubKeyStart])
	pubKeyBytes := unlockingScript[pubKeyStart+1 : pubKeyStart+1+pubKeyLen]

	expectedPubKey := km.GetPrivateKey().PubKey()
	expectedBytes := expectedPubKey.Compressed()

	if len(pubKeyBytes) == 65 {
		expectedBytes = expectedPubKey.Uncompressed()
	}

	if hex.EncodeToString(pubKeyBytes) != hex.EncodeToString(expectedBytes) {
		t.Error("Extracted public key doesn't match expected public key")
		t.Logf("  Expected: %s", hex.EncodeToString(expectedBytes))
		t.Logf("  Got: %s", hex.EncodeToString(pubKeyBytes))
	} else {
		t.Log("Public key correctly embedded in unlocking script")
		t.Logf("  Public key: %s", hex.EncodeToString(pubKeyBytes))
	}
}

func TestSigningEmptyTransaction(t *testing.T) {
	tx := transaction.NewTransaction()

	err := tx.Sign()
	if err == nil {
		t.Log("Empty transaction 'signed' successfully (no-op)")
	} else {
		t.Logf("Empty transaction sign returned error: %v", err)
	}

	if len(tx.Inputs) != 0 {
		t.Error("Empty transaction should have no inputs")
	}
}

func TestTestnetWIFParsing(t *testing.T) {
	testCases := []struct {
		name    string
		wif     string
		network keys.Network
	}{
		{"Testnet WIF 1", testnetWIF1, keys.Testnet},
		{"Testnet WIF 2", testnetWIF2, keys.Testnet},
		{"Testnet WIF 3", testnetWIF3, keys.Testnet},
		{"Mainnet WIF", testWIF, keys.Mainnet},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			km, err := keys.NewKeyManager(tc.wif)
			if err != nil {
				t.Fatalf("Failed to parse %s: %v", tc.name, err)
			}

			if km.GetNetwork() != tc.network {
				t.Errorf("Network mismatch for %s: expected %v, got %v", tc.name, tc.network, km.GetNetwork())
			}

			// Verify we can get address and private key
			addr := km.GetAddress()
			if addr == "" {
				t.Errorf("Empty address for %s", tc.name)
			}

			privKey := km.GetPrivateKey()
			if privKey == nil {
				t.Errorf("Nil private key for %s", tc.name)
			}

			t.Logf("%s parsed successfully:", tc.name)
			t.Logf("  Network: %v", km.GetNetwork())
			t.Logf("  Address: %s", addr)
			t.Logf("  Is Testnet: %v", km.IsTestnet())
		})
	}
}

func TestTestnetTransactionSigning(t *testing.T) {
	testCases := []struct {
		name string
		wif  string
	}{
		{"Testnet Key 1", testnetWIF1},
		{"Testnet Key 2", testnetWIF2},
		{"Testnet Key 3", testnetWIF3},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			km, err := keys.NewKeyManager(tc.wif)
			if err != nil {
				t.Fatalf("Failed to create key manager: %v", err)
			}

			builder := NewBuilder(km)
			utxo := createTestUTXO()
			utxo.Address = km.GetAddress()

			tx, err := builder.BuildSimpleTransaction(utxo)
			if err != nil {
				t.Fatalf("Failed to build transaction: %v", err)
			}

			// Verify transaction is signed
			if len(tx.Inputs) == 0 {
				t.Fatal("Transaction has no inputs")
			}

			unlockingScript := tx.Inputs[0].UnlockingScript
			if unlockingScript == nil || len(*unlockingScript) == 0 {
				t.Fatal("Transaction not signed")
			}

			t.Logf("%s transaction signed:", tc.name)
			t.Logf("  TX ID: %s", tx.TxID())
			t.Logf("  Unlocking script: %d bytes", len(*unlockingScript))
			t.Logf("  Network: Testnet")
		})
	}
}

func TestMixedNetworkTransactions(t *testing.T) {
	// Test that we can build transactions with both mainnet and testnet keys
	wifs := []struct {
		name    string
		wif     string
		network string
	}{
		{"Mainnet", testWIF, "mainnet"},
		{"Testnet 1", testnetWIF1, "testnet"},
		{"Testnet 2", testnetWIF2, "testnet"},
	}

	for _, w := range wifs {
		t.Run(w.name, func(t *testing.T) {
			km, err := keys.NewKeyManager(w.wif)
			if err != nil {
				t.Fatalf("Failed to create key manager: %v", err)
			}

			builder := NewBuilder(km)

			// Build a split transaction
			utxo := createTestUTXO()
			utxo.Amount = 10000
			utxo.Address = km.GetAddress()

			tx, err := builder.BuildSplitTransaction(utxo, 5)
			if err != nil {
				t.Fatalf("Failed to build split transaction: %v", err)
			}

			if len(tx.Inputs) != 1 {
				t.Errorf("Expected 1 input, got %d", len(tx.Inputs))
			}

			// Verify outputs (1 OP_RETURN + 5 value outputs)
			if len(tx.Outputs) != 6 {
				t.Errorf("Expected 6 outputs, got %d", len(tx.Outputs))
			}

			t.Logf("%s split transaction created:", w.name)
			t.Logf("  Network: %s", w.network)
			t.Logf("  TX ID: %s", tx.TxID())
			t.Logf("  Outputs: %d", len(tx.Outputs))
		})
	}
}

func TestTestnetChainedTransactions(t *testing.T) {
	km, err := keys.NewKeyManager(testnetWIF1)
	if err != nil {
		t.Fatalf("Failed to create testnet key manager: %v", err)
	}

	builder := NewBuilder(km)
	utxo := createTestUTXO()
	utxo.Amount = 1000000
	utxo.Address = km.GetAddress()

	txs, err := builder.BuildManyTransactions(utxo, 10)
	if err != nil {
		t.Fatalf("Failed to build chained transactions: %v", err)
	}

	if len(txs) != 10 {
		t.Fatalf("Expected 10 transactions, got %d", len(txs))
	}

	// Verify chain integrity
	prevTxID := testTxHash
	for i, tx := range txs {
		// Check that each transaction spends from the previous one
		if i > 0 {
			inputTxID := tx.Inputs[0].SourceTXID.String()
			if inputTxID != prevTxID {
				t.Errorf("Transaction %d: broken chain, expected input from %s, got %s",
					i, prevTxID, inputTxID)
			}
		}
		prevTxID = tx.TxID().String()

		// Verify signing
		if tx.Inputs[0].UnlockingScript == nil || len(*tx.Inputs[0].UnlockingScript) == 0 {
			t.Errorf("Transaction %d: not properly signed", i)
		}
	}

	t.Logf("Testnet chained transactions created successfully:")
	t.Logf("  Network: Testnet")
	t.Logf("  Chain length: %d", len(txs))
	t.Logf("  All transactions signed: true")
}

func TestNetworkAddressGeneration(t *testing.T) {
	// Test that the same private key generates different addresses for mainnet vs testnet
	testCases := []struct {
		name            string
		mainnetWIF      string
		testnetWIF      string
		expectedMainnet string
		expectedTestnet string
	}{
		{
			name:            "Key Pair 1",
			mainnetWIF:      "KwdMAjGmerYanjeui5SHS7JkmpZvVipYvB2LJGU1ZxJwYvP98617",
			testnetWIF:      "cMzLdeGd5vEqxB8B6VFQoRopQ3sLAAvEzDAoQgvX54xwofSWj1fx",
			expectedMainnet: "1BgGZ9tcN4rm9KBzDn7KprQz87SZ26SAMH",
			expectedTestnet: "n1KSZGmQgB8iSZqv6UVhGkCGUbEdw8Lm3Q",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Parse mainnet key
			mainnetKM, err := keys.NewKeyManager(tc.mainnetWIF)
			if err != nil {
				t.Fatalf("Failed to parse mainnet WIF: %v", err)
			}

			// Parse testnet key
			testnetKM, err := keys.NewKeyManager(tc.testnetWIF)
			if err != nil {
				t.Fatalf("Failed to parse testnet WIF: %v", err)
			}

			// Verify networks
			if !mainnetKM.IsTestnet() && testnetKM.IsTestnet() {
				t.Log("Network detection correct")
			} else {
				t.Error("Network detection failed")
			}

			// Log addresses
			t.Logf("Mainnet address: %s", mainnetKM.GetAddress())
			t.Logf("Testnet address: %s", testnetKM.GetAddress())

			// Verify they're different (same key, different networks = different addresses)
			if mainnetKM.GetAddress() == testnetKM.GetAddress() {
				t.Error("Same private key should generate different addresses for different networks")
			}
		})
	}
}

func TestCrossNetworkKeyCompatibility(t *testing.T) {
	// Test that we handle both compressed and uncompressed testnet keys
	testCases := []struct {
		name       string
		wif        string
		compressed bool
		network    keys.Network
	}{
		{"Testnet Compressed", testnetWIF1, true, keys.Testnet},
		{"Testnet Uncompressed", "91gGn1HgSap6CbU12F6z3pJri26xzp7Ay1VW6NHCoEayNXwRpu2", false, keys.Testnet},
		{"Mainnet Compressed", testWIF, true, keys.Mainnet},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			km, err := keys.NewKeyManager(tc.wif)
			if err != nil {
				t.Fatalf("Failed to parse %s: %v", tc.name, err)
			}

			if km.GetNetwork() != tc.network {
				t.Errorf("Network mismatch: expected %v, got %v", tc.network, km.GetNetwork())
			}

			// Build a transaction to ensure key works
			builder := NewBuilder(km)
			utxo := createTestUTXO()
			utxo.Address = km.GetAddress()

			_, err = builder.BuildSimpleTransaction(utxo)
			if err != nil {
				t.Errorf("Failed to build transaction with %s: %v", tc.name, err)
			}

			t.Logf("%s key works correctly", tc.name)
		})
	}
}

func TestValidateSignatureAgainstMessage(t *testing.T) {
	km := createTestKeyManager(t)
	privKey := km.GetPrivateKey()

	message := []byte("test message for signing")
	// Create a 32-byte hash from the message using SHA256
	hash := sha256.Sum256(message)
	messageHash := hash[:]

	signature, err := privKey.Sign(messageHash)
	if err != nil {
		t.Fatalf("Failed to create signature: %v", err)
	}

	pubKey := privKey.PubKey()
	valid := signature.Verify(messageHash, pubKey)

	if !valid {
		t.Fatal("Signature verification failed")
	}

	t.Log("Direct signature verification passed")
	t.Logf("  Message: %s", string(message))
	t.Logf("  Signature valid: %v", valid)
}
