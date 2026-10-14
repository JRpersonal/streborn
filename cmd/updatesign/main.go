// Command updatesign writes and signs the release index a speaker reads when it
// updates itself (see internal/updatesig).
//
//	updatesign keygen
//	    prints a new key pair: the private seed for the Actions secret and the
//	    public key for internal/updatesig.
//	updatesign sign -dir dist -version v1.2.3 [-build B] [-commit C]
//	    reads the seed from STR_UPDATE_SIGNING_KEY, writes dist/update.json and
//	    dist/update.json.sig, and checks the result against the public keys
//	    compiled into the agent, so a release whose key the agent would not
//	    trust fails here instead of on every speaker. -trust KEY adds a key
//	    for a local test release served to a speaker with a source override.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JRpersonal/streborn/internal/updatesig"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: updatesign keygen | sign -dir DIR -version vX.Y.Z")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen()
	case "sign":
		err = sign(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "updatesign:", err)
		os.Exit(1)
	}
}

func keygen() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Println("private seed (STR_UPDATE_SIGNING_KEY):", base64.StdEncoding.EncodeToString(priv.Seed()))
	fmt.Println("public key (internal/updatesig):      ", base64.StdEncoding.EncodeToString(pub))
	return nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	dir := fs.String("dir", "dist", "directory holding the release files")
	version := fs.String("version", "", "release tag, e.g. v1.2.3")
	build := fs.String("build", "", "build stamp")
	commit := fs.String("commit", "", "commit hash")
	trust := fs.String("trust", "", "an extra public key to accept, for a local test release only")
	_ = fs.Parse(args)

	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("STR_UPDATE_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("STR_UPDATE_SIGNING_KEY is missing or not a base64 Ed25519 seed")
	}
	priv := ed25519.NewKeyFromSeed(seed)

	ix := updatesig.Index{Version: *version, Build: *build, Commit: *commit, Files: map[string]updatesig.File{}}
	for _, name := range []string{updatesig.AgentFile, updatesig.EngineFile} {
		f, err := hashFile(filepath.Join(*dir, name))
		if err != nil {
			return err
		}
		ix.Files[name] = f
	}
	payload, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	sig := updatesig.Sign(priv, payload)
	keys := updatesig.PublicKeys
	if *trust != "" {
		keys = append(append([]string{}, keys...), *trust)
	}
	if _, err := updatesig.Verify(payload, sig, keys); err != nil {
		return fmt.Errorf("the signing key does not match any public key in internal/updatesig, speakers would refuse this release: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*dir, updatesig.IndexName), payload, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dir, updatesig.SigName), []byte(sig+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s", payload)
	return nil
}

func hashFile(path string) (updatesig.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return updatesig.File{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return updatesig.File{}, err
	}
	return updatesig.File{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}
