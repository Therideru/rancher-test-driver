package driver

import (
    "crypto/rand"
    "crypto/rsa"
    "crypto/x509"
    "encoding/pem"
    "fmt"
    "os"
    "path/filepath"

    "golang.org/x/crypto/ssh"
    "github.com/rancher/machine/libmachine/log"
)

// generateSSHKey creates a new SSH keypair, stores the private key under
// d.StorePath, and returns the public key bytes for uploading.
func (d *Driver) generateSSHKey() ([]byte, error) {
    // 1) Generate RSA key
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil {
        return nil, fmt.Errorf("generating RSA key: %w", err)
    }

    // 2) PEM-encode private key
    privDER := x509.MarshalPKCS1PrivateKey(key)
    privBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER}
    privBytes := pem.EncodeToMemory(privBlock)

    // 3) Create OpenSSH public key
    pub, err := ssh.NewPublicKey(&key.PublicKey)
    if err != nil {
        return nil, fmt.Errorf("creating public key: %w", err)
    }
    pubBytes := ssh.MarshalAuthorizedKey(pub)

    // 4) Write private key to disk under d.StorePath
    storePath := d.StorePath
    keyPath := filepath.Join(storePath, "id_rsa")
    if err := os.MkdirAll(storePath, 0700); err != nil {
        return nil, fmt.Errorf("creating store directory: %w", err)
    }
    if err := os.WriteFile(keyPath, privBytes, 0600); err != nil {
        return nil, fmt.Errorf("writing private key: %w", err)
    }

    d.SSHKeyPath = keyPath
    log.Debugf("Wrote SSH private key to %s", keyPath)

    return pubBytes, nil
}