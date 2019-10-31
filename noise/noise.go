package noise

import (
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"

	"github.com/SkycoinProject/skycoin/src/util/logging"

	"github.com/flynn/noise"

	"github.com/SkycoinProject/dmsg/cipher"
)

var noiseLogger = logging.MustGetLogger("noise") // TODO: initialize properly or remove

// nonceSize is the noise cipher state's nonce size in bytes.
const nonceSize = 8

// Config hold noise parameters.
type Config struct {
	LocalPK   cipher.PubKey // Local instance static public key.
	LocalSK   cipher.SecKey // Local instance static secret key.
	RemotePK  cipher.PubKey // Remote instance static public key.
	Initiator bool          // Whether the local instance initiates the connection.
}

// Noise handles the handshake and the frame's cryptography.
// All operations on Noise are not guaranteed to be thread-safe.
type Noise struct {
	pk   cipher.PubKey
	sk   cipher.SecKey
	init bool

	pattern noise.HandshakePattern
	hs      *noise.HandshakeState
	enc     *noise.CipherState
	dec     *noise.CipherState

	encNonce uint64 // increment after encryption
	decNonce uint64 // expect increment with each subsequent packet
}

// New creates a new Noise with:
//	- provided pattern for handshake.
//	- Secp256k1 for the curve.
func New(pattern noise.HandshakePattern, config Config) (*Noise, error) {
	nc := noise.Config{
		CipherSuite: noise.NewCipherSuite(Secp256k1{}, noise.CipherChaChaPoly, noise.HashSHA256),
		Random:      rand.Reader,
		Pattern:     pattern,
		Initiator:   config.Initiator,
		StaticKeypair: noise.DHKey{
			Public:  config.LocalPK[:],
			Private: config.LocalSK[:],
		},
	}
	if !config.RemotePK.Null() {
		nc.PeerStatic = config.RemotePK[:]
	}

	hs, err := noise.NewHandshakeState(nc)
	if err != nil {
		return nil, err
	}
	return &Noise{
		pk:      config.LocalPK,
		sk:      config.LocalSK,
		init:    config.Initiator,
		pattern: pattern,
		hs:      hs,
	}, nil
}

// KKAndSecp256k1 creates a new Noise with:
//	- KK pattern for handshake.
//	- Secp256k1 for the curve.
func KKAndSecp256k1(config Config) (*Noise, error) {
	return New(noise.HandshakeKK, config)
}

// XKAndSecp256k1 creates a new Noise with:
//  - XK pattern for handshake.
//	- Secp256 for the curve.
func XKAndSecp256k1(config Config) (*Noise, error) {
	return New(noise.HandshakeXK, config)
}

// HandshakeMessage generates handshake message for a current handshake state.
func (ns *Noise) HandshakeMessage() (res []byte, err error) {
	if ns.hs.MessageIndex() < len(ns.pattern.Messages)-1 {
		res, _, _, err = ns.hs.WriteMessage(nil, nil)
		return
	}

	res, ns.dec, ns.enc, err = ns.hs.WriteMessage(nil, nil)
	return res, err
}

// ProcessMessage processes a received handshake message and appends the payload.
func (ns *Noise) ProcessMessage(msg []byte) (err error) {
	if ns.hs.MessageIndex() < len(ns.pattern.Messages)-1 {
		_, _, _, err = ns.hs.ReadMessage(nil, msg)
		return
	}

	_, ns.enc, ns.dec, err = ns.hs.ReadMessage(nil, msg)
	return err
}

// LocalStatic returns the local static public key.
func (ns *Noise) LocalStatic() cipher.PubKey {
	return ns.pk
}

// RemoteStatic returns the remote static public key.
func (ns *Noise) RemoteStatic() cipher.PubKey {
	pk, err := cipher.NewPubKey(ns.hs.PeerStatic())
	if err != nil {
		panic(err)
	}
	return pk
}

// EncryptUnsafe encrypts plaintext without interlocking, should only
// be used with external lock.
func (ns *Noise) EncryptUnsafe(plaintext []byte) []byte {
	nonce := atomic.AddUint64(&ns.encNonce, 1)

	buf := make([]byte, nonceSize)
	binary.BigEndian.PutUint64(buf, nonce)

	return append(buf, ns.enc.Cipher().Encrypt(nil, nonce, nil, plaintext)...)
}

// DecryptUnsafe decrypts ciphertext without interlocking, should only
// be used with external lock.
func (ns *Noise) DecryptUnsafe(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return make([]byte, 0), nil
	}
	if len(ciphertext) < nonceSize {
		panic("noise decrypt unsafe: cipher text cannot be less than 8 bytes")
	}

	recvSeq := binary.BigEndian.Uint64(ciphertext[:nonceSize])
	lastSeq := atomic.AddUint64(&ns.decNonce, 1) - 1

	if recvSeq <= lastSeq {
		noiseLogger.Warnf("received decryption sequence (%d) is not higher than previous (%d)", recvSeq, lastSeq)
		return nil, nil // TODO(evanlinjin): Maybe we should return error here.
	}
	//ns.decNonce = recvSeq
	atomic.CompareAndSwapUint64(&ns.decNonce, lastSeq, recvSeq)

	return ns.dec.Cipher().Decrypt(nil, recvSeq, nil, ciphertext[nonceSize:])
}

// HandshakeFinished indicate whether handshake was completed.
func (ns *Noise) HandshakeFinished() bool {
	return ns.hs.MessageIndex() == len(ns.pattern.Messages)
}
