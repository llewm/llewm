// Package lantransfer exchanges immutable portable bundles over one temporary,
// explicitly paired TLS connection. It never opens a project database or Git.
package lantransfer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

const protocol = "llewm-lan-v1"
const MaxLifetime = 10 * time.Minute

// Pairing is a bearer credential. Deliver it privately through a trusted channel;
// never include its contents in logs, history, arguments, or project records.
type Pairing struct {
	Protocol          string    `json:"protocol"`
	Address           string    `json:"address"`
	CertificateSHA256 string    `json:"certificate_sha256"`
	Secret            string    `json:"secret"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type Result struct {
	Received        bool   `json:"received"`
	Sent            bool   `json:"sent"`
	Directory       string `json:"directory,omitempty"`
	LineageID       string `json:"lineage_id,omitempty"`
	RecordSetSHA256 string `json:"record_set_sha256,omitempty"`
	ImportRequired  bool   `json:"import_required"`
}

type Server struct {
	listener         net.Listener
	pairing          Pairing
	certificate      tls.Certificate
	outgoing         portable.Bundle
	receiveDirectory string
}

// Address permits only a literal private or loopback address, never a wildcard,
// public endpoint, DNS resolution, proxy, relay, or discovery service.
func Address(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("use a literal LAN IP:port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || !(ip.IsPrivate() || ip.IsLoopback()) {
		return fmt.Errorf("address must be a specific private or loopback IP")
	}
	// Parse a numeric port without invoking a service-name resolver.
	var p uint64
	if port == "" {
		return fmt.Errorf("numeric port required")
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return fmt.Errorf("numeric port required")
		}
		p = p*10 + uint64(c-'0')
		if p > 65535 {
			return fmt.Errorf("invalid port")
		}
	}
	return nil
}

func CheckDestination(directory string) error {
	if directory == "" {
		return fmt.Errorf("new receive directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(absolute); !os.IsNotExist(err) {
		return fmt.Errorf("receive directory must not exist")
	}
	info, err := os.Lstat(filepath.Dir(absolute))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("receive parent must be an existing real directory")
	}
	return nil
}

func NewServer(address string, outgoing portable.Bundle, receiveDirectory string, lifetime time.Duration) (*Server, error) {
	if err := Address(address); err != nil {
		return nil, err
	}
	if lifetime <= 0 || lifetime > MaxLifetime {
		return nil, fmt.Errorf("lifetime must be positive and at most %s", MaxLifetime)
	}
	bundle, err := portable.ValidateBundle(outgoing.ManifestBytes, outgoing.RecordsBytes)
	if err != nil {
		return nil, err
	}
	if err := CheckDestination(receiveDirectory); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: protocol}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(lifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	pin := sha256.Sum256(der)
	return &Server{listener: ln, pairing: Pairing{Protocol: protocol, Address: ln.Addr().String(), CertificateSHA256: hex.EncodeToString(pin[:]), Secret: hex.EncodeToString(secret[:]), ExpiresAt: now.Add(lifetime)}, certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, outgoing: bundle, receiveDirectory: receiveDirectory}, nil
}

func (s *Server) Pairing() Pairing { return s.pairing }
func (s *Server) Close() error     { return s.listener.Close() }

// Serve accepts at most sixteen connections and consumes the pairing on the
// first authenticated attempt, including a malformed or interrupted exchange.
func (s *Server) Serve(ctx context.Context) (Result, error) {
	ctx, cancel := context.WithDeadline(ctx, s.pairing.ExpiresAt)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	defer s.Close()
	for attempts := 0; attempts < 16; attempts++ {
		raw, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{}, err
		}
		result, authenticated, err := s.connection(ctx, raw)
		if authenticated {
			return result, err
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	return Result{}, fmt.Errorf("pairing closed after too many unauthenticated connections")
}

func deadline(ctx context.Context, maximum time.Duration) time.Time {
	value := time.Now().Add(maximum)
	if d, ok := ctx.Deadline(); ok && d.Before(value) {
		return d
	}
	return value
}

func (s *Server) connection(ctx context.Context, raw net.Conn) (Result, bool, error) {
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	_ = raw.SetDeadline(deadline(ctx, 15*time.Second))
	conn := tls.Server(raw, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{s.certificate}})
	if err := conn.HandshakeContext(ctx); err != nil {
		return Result{}, false, err
	}
	// Authenticate before receiving record bodies or revealing the outgoing bundle.
	expected, _ := hex.DecodeString(s.pairing.Secret)
	provided := make([]byte, len(expected))
	if _, err := io.ReadFull(conn, provided); err != nil {
		return Result{}, false, err
	}
	if subtle.ConstantTimeCompare(expected, provided) != 1 {
		return Result{}, false, errors.New("pairing refused")
	}
	_ = raw.SetDeadline(deadline(ctx, 2*time.Minute))
	incoming, err := readBundle(conn, true)
	if err != nil {
		return Result{}, true, err
	}
	result := Result{Sent: true}
	if incoming != nil {
		if incoming.Manifest.LineageID != s.outgoing.Manifest.LineageID {
			return Result{}, true, fmt.Errorf("different lineages; exchange refused")
		}
		if err := portable.PublishBundleDirectory(s.receiveDirectory, *incoming); err != nil {
			return Result{}, true, err
		}
		result = receivedResult(s.receiveDirectory, *incoming)
		result.Sent = true
	}
	if err := writeBundle(conn, &s.outgoing); err != nil {
		return result, true, err
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || ack[0] != 1 {
		return result, true, fmt.Errorf("peer receipt unconfirmed; inspect local receive directory before retrying")
	}
	return result, true, nil
}

// Exchange may omit outgoing for a fresh receiving install. Publication does not
// adopt a lineage or import anything. Each side has its own durable receipt;
// network loss cannot make two machines' filesystem publication atomic.
func Exchange(ctx context.Context, pairing Pairing, outgoing *portable.Bundle, receiveDirectory string) (Result, error) {
	if err := pairing.Validate(); err != nil {
		return Result{}, err
	}
	if err := CheckDestination(receiveDirectory); err != nil {
		return Result{}, err
	}
	if outgoing != nil {
		validated, err := portable.ValidateBundle(outgoing.ManifestBytes, outgoing.RecordsBytes)
		if err != nil {
			return Result{}, err
		}
		outgoing = &validated
	}
	ctx, cancel := context.WithDeadline(ctx, pairing.ExpiresAt)
	defer cancel()
	expected, _ := hex.DecodeString(pairing.CertificateSHA256)
	config := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, // The privately delivered certificate pin replaces public PKI.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return errors.New("peer certificate refused")
			}
			actual := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
				return errors.New("peer certificate refused")
			}
			return nil
		}}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: config}
	raw, err := dialer.DialContext(ctx, "tcp", pairing.Address)
	if err != nil {
		return Result{}, fmt.Errorf("paired connection failed")
	}
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	_ = raw.SetDeadline(deadline(ctx, 2*time.Minute))
	secret, _ := hex.DecodeString(pairing.Secret)
	if _, err := raw.Write(secret); err != nil {
		return Result{}, fmt.Errorf("pairing failed")
	}
	if err := writeBundle(raw, outgoing); err != nil {
		return Result{}, err
	}
	incoming, err := readBundle(raw, false)
	if err != nil {
		return Result{}, err
	}
	if outgoing != nil && outgoing.Manifest.LineageID != incoming.Manifest.LineageID {
		return Result{}, fmt.Errorf("different lineages; exchange refused")
	}
	if err := portable.PublishBundleDirectory(receiveDirectory, *incoming); err != nil {
		return Result{}, err
	}
	result := receivedResult(receiveDirectory, *incoming)
	result.Sent = outgoing != nil
	if _, err := raw.Write([]byte{1}); err != nil {
		return result, fmt.Errorf("received locally; peer acknowledgement failed")
	}
	return result, nil
}

func receivedResult(directory string, bundle portable.Bundle) Result {
	return Result{Received: true, Directory: directory, LineageID: bundle.Manifest.LineageID, RecordSetSHA256: bundle.Manifest.RecordSetSHA256, ImportRequired: true}
}

func (p Pairing) Validate() error {
	if p.Protocol != protocol {
		return errors.New("unsupported LAN pairing protocol")
	}
	if err := Address(p.Address); err != nil {
		return err
	}
	if p.ExpiresAt.Before(time.Now()) || p.ExpiresAt.After(time.Now().Add(MaxLifetime)) {
		return errors.New("pairing expired or lifetime exceeds ten minutes")
	}
	for _, value := range []string{p.Secret, p.CertificateSHA256} {
		body, err := hex.DecodeString(value)
		if err != nil || len(body) != 32 {
			return errors.New("invalid pairing credential")
		}
	}
	return nil
}

// A frame contains exactly two bounded byte strings, never remote file paths.
func writeBundle(w io.Writer, bundle *portable.Bundle) error {
	var manifest, records []byte
	if bundle != nil {
		manifest, records = bundle.ManifestBytes, bundle.RecordsBytes
	}
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(manifest)))
	binary.BigEndian.PutUint32(header[4:], uint32(len(records)))
	for _, body := range [][]byte{header[:], manifest, records} {
		if _, err := io.Copy(w, bytes.NewReader(body)); err != nil {
			return err
		}
	}
	return nil
}

func readBundle(r io.Reader, optional bool) (*portable.Bundle, error) {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("incomplete LAN bundle header")
	}
	m, n := binary.BigEndian.Uint32(header[:4]), binary.BigEndian.Uint32(header[4:])
	if optional && m == 0 && n == 0 {
		return nil, nil
	}
	if m == 0 || m > portable.MaxManifestBytes || n > portable.MaxRecordsBytes {
		return nil, errors.New("LAN bundle exceeds portable bounds or has no manifest")
	}
	manifest, records := make([]byte, int(m)), make([]byte, int(n))
	if _, err := io.ReadFull(r, manifest); err != nil {
		return nil, errors.New("incomplete LAN manifest")
	}
	if _, err := io.ReadFull(r, records); err != nil {
		return nil, errors.New("incomplete LAN records")
	}
	bundle, err := portable.ValidateBundle(manifest, records)
	if err != nil {
		return nil, err
	}
	return &bundle, nil
}

// Pairing files use exclusive publication, restrictive permissions and bounded
// reads. Callers remove the sender's credential when the temporary listener ends.
func WritePairing(path string, pairing Pairing) error {
	body, err := json.Marshal(pairing)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private pairing file: %w", err)
	}
	_, writeErr := f.Write(body)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("write pairing file failed")
	}
	return nil
}

func ReadPairing(path string) (Pairing, error) {
	var p Pairing
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0o077 != 0 {
		return p, errors.New("pairing file must be a private regular file of at most 4096 bytes")
	}
	f, err := os.Open(path)
	if err != nil {
		return p, errors.New("cannot open pairing file")
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return p, errors.New("pairing file changed")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return Pairing{}, errors.New("invalid pairing file")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return Pairing{}, errors.New("unexpected pairing data")
	}
	if err := p.Validate(); err != nil {
		return Pairing{}, err
	}
	return p, nil
}
