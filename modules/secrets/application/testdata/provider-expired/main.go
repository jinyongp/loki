// This fixture is compiled INSIDE the hash-verified provider source tree. It
// uses provider-owned vault APIs to create a synthetic expired slot, because
// the public CLI intentionally has no expiry-edit command. Production Loki
// neither links these packages nor parses or rewrites provider vault files.
package main

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/jinyongp/devtools/internal/vault"
)

func run() error {
	if len(os.Args) != 3 {
		return vault.ErrInvalid
	}
	ctx := context.Background()
	root := filepath.Join(os.Args[1], "data")
	passBytes, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if err != nil || len(passBytes) > 65536 {
		return vault.ErrInvalid
	}
	defer clear(passBytes)
	pass := vault.NewMaterial(passBytes)
	defer pass.Destroy()
	id, err := vault.NewID()
	if err != nil {
		return err
	}
	master, err := (vault.KeyFileStore{Directory: filepath.Join(root, "vault")}).Initialize(ctx, id, "passphrase", vault.PassphraseProtector{VaultID: id, Passphrase: pass})
	if err != nil {
		return err
	}
	defer master.Destroy()
	key, err := vault.NewKey()
	if err != nil {
		return err
	}
	defer key.Destroy()
	p, err := vault.NewSealedProfile(os.Args[2], id, master, key)
	if err != nil {
		return err
	}
	if _, err := p.Set("EXPIRED", vault.Secret, "", "synthetic-expired-canary", key); err != nil {
		return err
	}
	expiry := int64(1)
	p.Values[0].Slots[0].ExpiresAt = &expiry
	if err := p.Sign(key); err != nil {
		return err
	}
	store := vault.FileStore{Root: root, VaultID: id, CatalogKey: master}
	if err := store.Initialize(ctx); err != nil {
		return err
	}
	return store.Create(ctx, p, key)
}

func main() {
	if run() != nil {
		os.Stderr.WriteString("synthetic expiry fixture failed\n")
		os.Exit(1)
	}
}
