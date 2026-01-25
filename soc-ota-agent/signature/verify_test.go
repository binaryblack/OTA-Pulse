package signature

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"os"
	"testing"
)

// Test helper: Generate RSA key pair
func generateRSAKeyPair(t *testing.T) (*rsa.PrivateKey, []byte) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	pubKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal public key: %v", err)
	}

	pubKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubKeyBytes,
	})

	return privateKey, pubKeyPEM
}

// Test helper: Generate ECDSA key pair
func generateECDSAKeyPair(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate ECDSA key: %v", err)
	}

	pubKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal public key: %v", err)
	}

	pubKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubKeyBytes,
	})

	return privateKey, pubKeyPEM
}

// Test helper: Create test firmware file
func createTestFirmware(t *testing.T) (string, string) {
	tmpfile, err := ioutil.TempFile("", "firmware-*.bin")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer tmpfile.Close()

	// Write test data
	data := []byte("Test firmware content for signature verification")
	if _, err := tmpfile.Write(data); err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}

	// Calculate hash
	hash := sha256.Sum256(data)
	hashHex := fmt.Sprintf("%x", hash)

	return tmpfile.Name(), hashHex
}

// Test: Load valid RSA public key
func TestLoadRSAPublicKey(t *testing.T) {
	_, pubKeyPEM := generateRSAKeyPair(t)

	verifier := NewVerifier()
	err := verifier.LoadPublicKeyFromPEM(pubKeyPEM)

	if err != nil {
		t.Errorf("Failed to load RSA public key: %v", err)
	}

	if len(verifier.publicKeys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(verifier.publicKeys))
	}

	// Check algorithm
	for _, algo := range verifier.algorithms {
		if algo != AlgorithmRSA4096 {
			t.Errorf("Expected RSA-4096, got %s", algo)
		}
	}
}

// Test: Load valid ECDSA public key
func TestLoadECDSAPublicKey(t *testing.T) {
	_, pubKeyPEM := generateECDSAKeyPair(t)

	verifier := NewVerifier()
	err := verifier.LoadPublicKeyFromPEM(pubKeyPEM)

	if err != nil {
		t.Errorf("Failed to load ECDSA public key: %v", err)
	}

	if len(verifier.publicKeys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(verifier.publicKeys))
	}

	// Check algorithm
	for _, algo := range verifier.algorithms {
		if algo != AlgorithmECDSAP384 {
			t.Errorf("Expected ECDSA-P384, got %s", algo)
		}
	}
}

// Test: Load multiple public keys
func TestLoadMultipleKeys(t *testing.T) {
	_, rsaPubKeyPEM := generateRSAKeyPair(t)
	_, ecdsaPubKeyPEM := generateECDSAKeyPair(t)

	verifier := NewVerifier()

	if err := verifier.LoadPublicKeyFromPEM(rsaPubKeyPEM); err != nil {
		t.Fatalf("Failed to load RSA key: %v", err)
	}

	if err := verifier.LoadPublicKeyFromPEM(ecdsaPubKeyPEM); err != nil {
		t.Fatalf("Failed to load ECDSA key: %v", err)
	}

	if len(verifier.publicKeys) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(verifier.publicKeys))
	}
}

// Test: Load public key from file
func TestLoadPublicKeyFromFile(t *testing.T) {
	_, pubKeyPEM := generateRSAKeyPair(t)

	// Write to temp file
	tmpfile, err := ioutil.TempFile("", "pubkey-*.pem")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	if _, err := tmpfile.Write(pubKeyPEM); err != nil {
		t.Fatalf("Failed to write public key: %v", err)
	}
	tmpfile.Close()

	verifier := NewVerifier()
	if err := verifier.LoadPublicKey(tmpfile.Name()); err != nil {
		t.Errorf("Failed to load public key from file: %v", err)
	}
}

// Test: Invalid PEM data
func TestLoadInvalidPEM(t *testing.T) {
	verifier := NewVerifier()
	err := verifier.LoadPublicKeyFromPEM([]byte("Not a valid PEM"))

	if err == nil {
		t.Error("Expected error for invalid PEM, got nil")
	}
}

// Test: Calculate file hash
func TestCalculateFileHash(t *testing.T) {
	firmwarePath, expectedHash := createTestFirmware(t)
	defer os.Remove(firmwarePath)

	hash, err := calculateFileHash(firmwarePath)
	if err != nil {
		t.Errorf("Failed to calculate hash: %v", err)
	}

	if hash != expectedHash {
		t.Errorf("Hash mismatch: expected %s, got %s", expectedHash, hash)
	}
}

// Test: RSA signature verification (valid)
func TestVerifyRSASignatureValid(t *testing.T) {
	privateKey, pubKeyPEM := generateRSAKeyPair(t)
	firmwarePath, hashHex := createTestFirmware(t)
	defer os.Remove(firmwarePath)

	// Sign the hash
	hashBytes, _ := decodeHexHash(hashHex)
	signature, err := rsa.SignPSS(rand.Reader, privateKey, crypto.SHA256, hashBytes, &rsa.PSSOptions{
		SaltLength: rsa.PSSSaltLengthAuto,
		Hash:       crypto.SHA256,
	})
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}

	signatureB64 := base64.StdEncoding.EncodeToString(signature)

	// Verify
	verifier := NewVerifier()
	if err := verifier.LoadPublicKeyFromPEM(pubKeyPEM); err != nil {
		t.Fatalf("Failed to load public key: %v", err)
	}

	result, err := verifier.VerifyFirmware(firmwarePath, signatureB64)
	if err != nil {
		t.Errorf("Verification error: %v", err)
	}

	if !result.Valid {
		t.Errorf("Expected valid signature, got invalid")
	}

	if result.Algorithm != AlgorithmRSA4096 {
		t.Errorf("Expected RSA-4096, got %s", result.Algorithm)
	}
}

// Test: ECDSA signature verification (valid)
func TestVerifyECDSASignatureValid(t *testing.T) {
	privateKey, pubKeyPEM := generateECDSAKeyPair(t)
	firmwarePath, hashHex := createTestFirmware(t)
	defer os.Remove(firmwarePath)

	// Sign the hash
	hashBytes, _ := decodeHexHash(hashHex)
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, hashBytes)
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}

	signatureB64 := base64.StdEncoding.EncodeToString(signature)

	// Verify
	verifier := NewVerifier()
	if err := verifier.LoadPublicKeyFromPEM(pubKeyPEM); err != nil {
		t.Fatalf("Failed to load public key: %v", err)
	}

	result, err := verifier.VerifyFirmware(firmwarePath, signatureB64)
	if err != nil {
		t.Errorf("Verification error: %v", err)
	}

	if !result.Valid {
		t.Errorf("Expected valid signature, got invalid")
	}

	if result.Algorithm != AlgorithmECDSAP384 {
		t.Errorf("Expected ECDSA-P384, got %s", result.Algorithm)
	}
}

// Test: Invalid signature
func TestVerifySignatureInvalid(t *testing.T) {
	_, pubKeyPEM := generateRSAKeyPair(t)
	firmwarePath, _ := createTestFirmware(t)
	defer os.Remove(firmwarePath)

	// Create invalid signature
	invalidSig := base64.StdEncoding.EncodeToString([]byte("invalid signature data"))

	verifier := NewVerifier()
	if err := verifier.LoadPublicKeyFromPEM(pubKeyPEM); err != nil {
		t.Fatalf("Failed to load public key: %v", err)
	}

	result, err := verifier.VerifyFirmware(firmwarePath, invalidSig)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if result.Valid {
		t.Error("Expected invalid signature, got valid")
	}
}

// Test: Tampered firmware
func TestVerifyTamperedFirmware(t *testing.T) {
	privateKey, pubKeyPEM := generateRSAKeyPair(t)

	// Create original firmware
	tmpfile1, err := ioutil.TempFile("", "firmware-original-*.bin")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile1.Name())

	originalData := []byte("Original firmware content")
	tmpfile1.Write(originalData)
	tmpfile1.Close()

	// Sign original
	hash := sha256.Sum256(originalData)
	hashBytes := hash[:]
	signature, err := rsa.SignPSS(rand.Reader, privateKey, crypto.SHA256, hashBytes, nil)
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}
	signatureB64 := base64.StdEncoding.EncodeToString(signature)

	// Create tampered firmware
	tmpfile2, err := ioutil.TempFile("", "firmware-tampered-*.bin")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile2.Name())

	tamperedData := []byte("Tampered firmware content - MALICIOUS")
	tmpfile2.Write(tamperedData)
	tmpfile2.Close()

	// Verify tampered firmware with original signature
	verifier := NewVerifier()
	if err := verifier.LoadPublicKeyFromPEM(pubKeyPEM); err != nil {
		t.Fatalf("Failed to load public key: %v", err)
	}

	result, err := verifier.VerifyFirmware(tmpfile2.Name(), signatureB64)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if result.Valid {
		t.Error("Tampered firmware should have invalid signature")
	}
}

// Test: No public keys loaded
func TestVerifyWithNoKeys(t *testing.T) {
	firmwarePath, _ := createTestFirmware(t)
	defer os.Remove(firmwarePath)

	verifier := NewVerifier()

	_, err := verifier.VerifyFirmware(firmwarePath, "dGVzdA==") // base64 "test"
	if err == nil {
		t.Error("Expected error when no keys loaded")
	}
}

// Test: Get supported algorithms
func TestGetSupportedAlgorithms(t *testing.T) {
	algorithms := GetSupportedAlgorithms()

	if len(algorithms) != 2 {
		t.Errorf("Expected 2 supported algorithms, got %d", len(algorithms))
	}

	hasRSA := false
	hasECDSA := false
	for _, algo := range algorithms {
		if algo == AlgorithmRSA4096 {
			hasRSA = true
		}
		if algo == AlgorithmECDSAP384 {
			hasECDSA = true
		}
	}

	if !hasRSA {
		t.Error("RSA-4096 not in supported algorithms")
	}
	if !hasECDSA {
		t.Error("ECDSA-P384 not in supported algorithms")
	}
}

// Test: Get loaded keys info
func TestGetLoadedKeys(t *testing.T) {
	_, rsaPubKeyPEM := generateRSAKeyPair(t)
	_, ecdsaPubKeyPEM := generateECDSAKeyPair(t)

	verifier := NewVerifier()
	verifier.LoadPublicKeyFromPEM(rsaPubKeyPEM)
	verifier.LoadPublicKeyFromPEM(ecdsaPubKeyPEM)

	keys := verifier.GetLoadedKeys()

	if len(keys) != 2 {
		t.Errorf("Expected 2 keys info, got %d", len(keys))
	}

	for _, keyInfo := range keys {
		if keyInfo["fingerprint"] == "" {
			t.Error("Fingerprint should not be empty")
		}
		if keyInfo["algorithm"] == "" {
			t.Error("Algorithm should not be empty")
		}
	}
}
