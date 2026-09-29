// Command signtool signs InfraScope release archives for verified self-updates.
//
// GoReleaser runs it for every archive (see .goreleaser.yml):
//
//	signtool -in beszel_linux_amd64.tar.gz -out beszel_linux_amd64.tar.gz.sig
//
// The base64 ed25519 private key (32-byte seed or 64-byte key) is read from
// the environment variable named by -key-env (INFRASCOPE_RELEASE_SIGNING_KEY),
// a repository secret. Builds embed the matching public key through
// -X github.com/henrygd/beszel/internal/ghupdate.releasePublicKey=<base64>.
//
// signtool -generate prints a new key pair; signtool -public prints the public
// key of the private key in the environment.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"

	"github.com/henrygd/beszel/internal/ghupdate"
)

func main() {
	in := flag.String("in", "", "archive to sign")
	out := flag.String("out", "", "signature file to write")
	keyEnv := flag.String("key-env", "INFRASCOPE_RELEASE_SIGNING_KEY", "environment variable holding the base64 private key")
	generate := flag.Bool("generate", false, "print a new key pair")
	public := flag.Bool("public", false, "print the public key of the private key")
	flag.Parse()

	if *generate {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		exitOnError(err)
		fmt.Printf("private key (secret %s): %s\n", *keyEnv, base64.StdEncoding.EncodeToString(privateKey.Seed()))
		fmt.Printf("public key (INFRASCOPE_RELEASE_PUBLIC_KEY): %s\n", base64.StdEncoding.EncodeToString(publicKey))
		return
	}

	encoded := os.Getenv(*keyEnv)
	if encoded == "" {
		exitOnError(fmt.Errorf("%s is not set: releases must be signed", *keyEnv))
	}
	privateKey, err := ghupdate.ParsePrivateKey(encoded)
	exitOnError(err)

	if *public {
		fmt.Println(base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)))
		return
	}
	if *in == "" || *out == "" {
		exitOnError(fmt.Errorf("-in and -out are required"))
	}
	archive, err := os.ReadFile(*in)
	exitOnError(err)
	exitOnError(os.WriteFile(*out, []byte(ghupdate.SignArchive(privateKey, archive)), 0644))
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "signtool:", err)
		os.Exit(1)
	}
}
