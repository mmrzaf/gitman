// backup-check verifies restored secret values without exposing plaintext.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/repo"
)

func main() {
	repository := flag.String("repository", "", "repository name")
	secret := flag.String("secret", "", "secret name")
	fingerprint := flag.String("sha256", "", "expected plaintext SHA-256")
	flag.Parse()
	if err := check(*repository, *secret, *fingerprint); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check(repository, secret, fingerprint string) error {
	expected, err := hex.DecodeString(fingerprint)
	if err != nil || len(expected) != sha256.Size || repository == "" || secret == "" {
		return fmt.Errorf("provide a repository, secret name and SHA-256 fingerprint")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := postgres.Connect(ctx, cfg.DatabaseURL, postgres.Options{})
	if err != nil {
		return err
	}
	defer db.Close()
	store := git.NewStore(cfg.ReposPath())
	defer store.Close()
	service := repo.NewService(db, store, cfg.SecretKey)
	repositoryData, err := service.GetByName(ctx, repository)
	if err != nil {
		return err
	}
	values, err := service.RunSecrets(ctx, repositoryData.ID)
	if err != nil {
		return err
	}
	value, found := values[secret]
	sum := sha256.Sum256([]byte(value))
	if !found || !bytes.Equal(sum[:], expected) {
		return fmt.Errorf("restored secret does not match the fixture")
	}
	return nil
}
