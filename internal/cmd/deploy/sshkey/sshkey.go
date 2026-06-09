// Package sshkey implements `apigw deploy ssh-key` — show or generate the
// deploy SSH key used to clone private repos.
//
// One ed25519 key is shared across all deploys (the operator adds it as a
// Deploy Key on each GitHub repo). ed25519 is small, fast, and supported by
// every modern git host; we never use RSA.
package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdSSHKey(f *cmdutil.Factory) *cobra.Command {
	var regenerate bool
	cmd := &cobra.Command{
		Use:   "ssh-key",
		Short: "Show or generate the apigw deploy SSH key",
		Args:  cobra.NoArgs,
		Example: `  $ apigw deploy ssh-key                 # print public key (generate if missing)
  $ apigw deploy ssh-key --regenerate    # rotate the key (revokes access to old)`,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(f, regenerate)
		},
	}
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "force regeneration (overwrites existing key)")
	return cmd
}

func run(f *cmdutil.Factory, regenerate bool) error {
	if regenerate {
		if err := os.Remove(deploy.SSHKeyPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove old key: %w", err)
		}
		_ = os.Remove(deploy.SSHPubKeyPath())
	}

	pub, err := ensureKey()
	if err != nil {
		return err
	}

	if f.IOStreams.Quiet() {
		fmt.Fprintln(f.IOStreams.Out, pub)
		return nil
	}

	ios := f.IOStreams
	fmt.Fprintf(ios.Out, "%s deploy SSH key (%s):\n\n",
		tui.Styles.Heading.Render("apigw"),
		tui.Styles.Muted.Render(deploy.SSHPubKeyPath()))
	fmt.Fprintln(ios.Out, tui.Styles.Identifier.Render(pub))
	fmt.Fprintln(ios.Out)
	fmt.Fprintf(ios.Out, "  %s add this as a Deploy Key on each private repo\n",
		tui.Styles.Accent.Render("Next:"))
	fmt.Fprintf(ios.Out, "  %s github.com/<owner>/<repo>/settings/keys/new\n",
		tui.Styles.Muted.Render(tui.GlyphArrow))
	return nil
}

// ensureKey loads the public key from disk, generating an ed25519 keypair
// if none exists. Returns the OpenSSH-format public key string.
func ensureKey() (string, error) {
	if b, err := os.ReadFile(deploy.SSHPubKeyPath()); err == nil {
		return string(b), nil
	}

	if err := os.MkdirAll(filepath.Dir(deploy.SSHKeyPath()), 0o700); err != nil {
		return "", fmt.Errorf("mkdir .ssh: %w", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519: %w", err)
	}

	// Private key — OpenSSH format. Go stdlib doesn't ship an OpenSSH encoder
	// in crypto/ssh, but x/crypto/ssh has MarshalPrivateKey. Use it with a
	// dummy comment.
	pemBlock, err := ssh.MarshalPrivateKey(priv, "apigw deploy key")
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	if err := writeFile(deploy.SSHKeyPath(), pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		return "", err
	}

	// Public key — OpenSSH `ssh-ed25519 AAAA... apigw-deploy` line.
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("public key: %w", err)
	}
	pubLine := string(ssh.MarshalAuthorizedKey(sshPub))
	if err := writeFile(deploy.SSHPubKeyPath(), []byte(pubLine), 0o644); err != nil {
		return "", err
	}
	return pubLine, nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
