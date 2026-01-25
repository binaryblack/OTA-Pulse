// Package signature provides firmware signature verification capabilities
package signature

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Algorithm represents a supported signing algorithm
type Algorithm string

const (
	// AlgorithmRSA4096 represents RSA with 4096-bit key
	AlgorithmRSA4096 Algorithm = "RSA-4096"
	// AlgorithmECDSAP384 represents ECDSA with P-384 curve
	AlgorithmECDSAP384 Algorithm = "ECDSA-P384"
)

// VerificationResult represents the result of signature verification
type VerificationResult struct {
	Valid         bool
	Algorithm     Algorithm
	FirmwareHash  string
	ErrorMessage  string
	KeyFingerprint string
}

// Verifier handles firmware signature verification
type Verifier struct {
	publicKeys map[string]crypto.PublicKey // fingerprint -> public key
	algorithms map[string]Algorithm         // fingerprint -> algorithm
}

// NewVerifier creates a new signature verifier
func NewVerifier() *Verifier {
	return &Verifier{
		publicKeys: make(map[string]crypto.PublicKey),
		algorithms: make(map[string]Algorithm),
	}
}

// LoadPublicKey loads a public key from PEM file
func (v *Verifier) LoadPublicKey(path string) error {
	log.WithField("path", path).Info("Loading public key")

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read public key file: %w", err)
	}

	return v.LoadPublicKeyFromPEM(data)
}

// LoadPublicKeyFromPEM loads a public key from PEM bytes
func (v *Verifier) LoadPublicKeyFromPEM(pemData []byte) error {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return fmt.Errorf("failed to decode PEM block")
	}

	// Try parsing as PKIX (generic public key format)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	// Determine algorithm and calculate fingerprint
	var algorithm Algorithm
	switch key := pub.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() == 4096 {
			algorithm = AlgorithmRSA4096
		} else {
			return fmt.Errorf("unsupported RSA key size: %d bits", key.N.BitLen())
		}
	case *ecdsa.PublicKey:
		if key.Params().Name == "P-384" {
			algorithm = AlgorithmECDSAP384
		} else {
			return fmt.Errorf("unsupported ECDSA curve: %s", key.Params().Name)
		}
	default:
		return fmt.Errorf("unsupported public key type: %T", pub)
	}

	fingerprint := calculateFingerprint(pemData)
	v.publicKeys[fingerprint] = pub
	v.algorithms[fingerprint] = algorithm

	log.WithFields(log.Fields{
		"algorithm":   algorithm,
		"fingerprint": fingerprint,
	}).Info("Public key loaded successfully")

	return nil
}

// LoadPublicKeysFromDirectory loads all public keys from a directory
func (v *Verifier) LoadPublicKeysFromDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".pub") {
			path := fmt.Sprintf("%s/%s", dir, name)
			if err := v.LoadPublicKey(path); err != nil {
				log.WithError(err).WithField("file", name).Warn("Failed to load public key")
				continue
			}
			count++
		}
	}

	if count == 0 {
		return fmt.Errorf("no valid public keys found in directory")
	}

	log.WithField("count", count).Info("Loaded public keys from directory")
	return nil
}

// VerifyFirmware verifies a firmware file's signature
func (v *Verifier) VerifyFirmware(firmwarePath, signatureData string) (*VerificationResult, error) {
	if len(v.publicKeys) == 0 {
		return nil, fmt.Errorf("no public keys loaded")
	}

	// Calculate firmware hash
	firmwareHash, err := calculateFileHash(firmwarePath)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate firmware hash: %w", err)
	}

	// Decode base64 signature
	signatureBytes, err := base64.StdEncoding.DecodeString(signatureData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode signature: %w", err)
	}

	log.WithFields(log.Fields{
		"firmware_path": firmwarePath,
		"firmware_hash": firmwareHash,
		"signature_len": len(signatureBytes),
	}).Info("Verifying firmware signature")

	// Try verifying with each loaded public key
	for fingerprint, pubKey := range v.publicKeys {
		algorithm := v.algorithms[fingerprint]
		
		log.WithFields(log.Fields{
			"algorithm":   algorithm,
			"fingerprint": fingerprint[:16] + "...",
		}).Debug("Attempting verification with key")

		valid, err := verifySignature(pubKey, algorithm, firmwareHash, signatureBytes)
		if err != nil {
			log.WithError(err).Debug("Verification failed with this key")
			continue
		}

		if valid {
			log.Info("Signature verification successful")
			return &VerificationResult{
				Valid:          true,
				Algorithm:      algorithm,
				FirmwareHash:   firmwareHash,
				KeyFingerprint: fingerprint,
			}, nil
		}
	}

	// No key successfully verified the signature
	return &VerificationResult{
		Valid:        false,
		FirmwareHash: firmwareHash,
		ErrorMessage: "signature verification failed with all loaded keys",
	}, nil
}

// verifySignature verifies a signature using the given public key
func verifySignature(pubKey crypto.PublicKey, algorithm Algorithm, hash string, signature []byte) (bool, error) {
	hashBytes, err := decodeHexHash(hash)
	if err != nil {
		return false, err
	}

	switch algorithm {
	case AlgorithmRSA4096:
		rsaKey, ok := pubKey.(*rsa.PublicKey)
		if !ok {
			return false, fmt.Errorf("key is not RSA")
		}
		
		// Verify using PSS padding (as used in backend)
		err := rsa.VerifyPSS(rsaKey, crypto.SHA256, hashBytes, signature, &rsa.PSSOptions{
			SaltLength: rsa.PSSSaltLengthAuto,
			Hash:       crypto.SHA256,
		})
		return err == nil, nil

	case AlgorithmECDSAP384:
		ecdsaKey, ok := pubKey.(*ecdsa.PublicKey)
		if !ok {
			return false, fmt.Errorf("key is not ECDSA")
		}
		
		// ECDSA signature verification
		return ecdsa.VerifyASN1(ecdsaKey, hashBytes, signature), nil

	default:
		return false, fmt.Errorf("unsupported algorithm: %s", algorithm)
	}
}

// calculateFileHash calculates SHA-256 hash of a file
func calculateFileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// calculateFingerprint calculates SHA-256 fingerprint of public key
func calculateFingerprint(pemData []byte) string {
	hash := sha256.Sum256(pemData)
	return fmt.Sprintf("sha256:%x", hash)
}

// decodeHexHash decodes a hex string hash to bytes
func decodeHexHash(hexHash string) ([]byte, error) {
	hashBytes := make([]byte, len(hexHash)/2)
	for i := 0; i < len(hexHash); i += 2 {
		var b byte
		_, err := fmt.Sscanf(hexHash[i:i+2], "%02x", &b)
		if err != nil {
			return nil, fmt.Errorf("invalid hex hash: %w", err)
		}
		hashBytes[i/2] = b
	}
	return hashBytes, nil
}

// GetLoadedKeys returns information about loaded public keys
func (v *Verifier) GetLoadedKeys() []map[string]string {
	keys := make([]map[string]string, 0, len(v.publicKeys))
	for fingerprint, _ := range v.publicKeys {
		keys = append(keys, map[string]string{
			"fingerprint": fingerprint,
			"algorithm":   string(v.algorithms[fingerprint]),
		})
	}
	return keys
}

// GetSupportedAlgorithms returns list of supported algorithms
func GetSupportedAlgorithms() []Algorithm {
	return []Algorithm{AlgorithmRSA4096, AlgorithmECDSAP384}
}
