package connectionprofile

import (
	"bytes"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"

	"github.com/llewm/llewm/components/orchestration/internal/coordination"
	"github.com/llewm/llewm/components/orchestration/internal/transport"
)

// Profiles can be shared. Never follow filesystem references supplied by a profile.
type Profile struct {
	Version        int    `json:"version"`
	Endpoint       string `json:"endpoint"`
	Project        string `json:"project"`
	Authority      string `json:"authority"`
	CACertificates string `json:"ca_certificates,omitempty"`
}

func (p *Profile) Validate() error {
	if p.Version != 1 || !coordination.ValidBrokerID(p.Project) || len(p.Authority) != 48 {
		return errors.New("invalid broker profile version, project or authority")
	}
	if _, err := hex.DecodeString(p.Authority); err != nil {
		return errors.New("invalid broker authority")
	}
	if err := transport.ValidateSharedBrokerEndpoint(p.Endpoint); err != nil {
		return err
	}
	raw := bytes.TrimSpace([]byte(p.CACertificates))
	if len(raw) > 1<<20 {
		return errors.New("profile CA exceeds 1 MiB")
	}
	var clean bytes.Buffer
	for len(raw) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 {
			return errors.New("profile CA must contain public certificates")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return errors.New("invalid profile certificate")
		}
		if err := pem.Encode(&clean, block); err != nil {
			return err
		}
		raw = bytes.TrimSpace(rest)
	}
	p.CACertificates = clean.String()
	return nil
}

func Read(path string) (Profile, error) {
	var p Profile
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return p, err
	}
	if len(data) > limit {
		return p, errors.New("profile exceeds 2 MiB")
	}
	if err := transport.Decode(bytes.NewReader(data), &p); err != nil {
		return p, err
	}
	err = p.Validate()
	return p, err
}

func Write(path string, p Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(p)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}
