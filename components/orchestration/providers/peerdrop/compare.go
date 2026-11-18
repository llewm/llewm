package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Bind the comparison to the actual DTLS certificates in role order. The
// offerer commits before seeing the guest's nonce so neither endpoint can
// adaptively choose its contribution to hit a particular short code.
func comparison(ctx context.Context, l *link, host bool) (string, error) {
	dtls := l.pc.SCTP().Transport()
	params, err := dtls.GetLocalParameters()
	if err != nil {
		return "", err
	}
	remote := dtls.GetRemoteCertificate()
	if len(remote) == 0 {
		return "", errors.New("missing peer certificate")
	}
	sum := sha256.Sum256(remote)
	guestID := hex.EncodeToString(sum[:])
	var hostID string
	for _, fp := range params.Fingerprints {
		if fp.Algorithm == "sha-256" {
			hostID = strings.ToLower(strings.ReplaceAll(fp.Value, ":", ""))
			break
		}
	}
	if len(hostID) != 64 {
		return "", errors.New("missing SHA-256 local fingerprint")
	}
	if !host {
		hostID, guestID = guestID, hostID
	}
	binding := "llewm-peerdrop-v1/" + hostID + "/" + guestID + "/"
	return exchangeNonces(host, binding, l.channel.Send, func() ([]byte, error) { return l.receive(ctx) })
}

func exchangeNonces(host bool, binding string, send func([]byte) error, receive func() ([]byte, error)) (string, error) {
	var err error
	var local [32]byte
	if _, err := rand.Read(local[:]); err != nil {
		return "", err
	}
	read32 := func() ([]byte, error) {
		b, err := receive()
		if err != nil {
			return nil, err
		}
		if len(b) != 32 {
			return nil, errors.New("unexpected verification frame")
		}
		return b, nil
	}
	var hostNonce, guestNonce []byte
	if host {
		hostNonce = local[:]
		digest := sha256.Sum256(append([]byte(binding+"commit/"), hostNonce...))
		if err := send(digest[:]); err != nil {
			return "", err
		}
		guestNonce, err = read32()
		if err != nil {
			return "", err
		}
		if err := send(hostNonce); err != nil {
			return "", err
		}
	} else {
		guestNonce = local[:]
		commitment, err := read32()
		if err != nil {
			return "", err
		}
		if err := send(guestNonce); err != nil {
			return "", err
		}
		hostNonce, err = read32()
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256(append([]byte(binding+"commit/"), hostNonce...))
		if subtle.ConstantTimeCompare(commitment, digest[:]) != 1 {
			return "", errors.New("peer verification commitment differs")
		}
	}
	digest := sha256.Sum256(append(append([]byte(binding+"compare/"), hostNonce...), guestNonce...))
	code := binary.BigEndian.Uint64(digest[:8]) % 100000000
	return fmt.Sprintf("%04d-%04d", code/10000, code%10000), nil
}
