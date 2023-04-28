package sync2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/jmoiron/sqlx"
	"io"
	"strings"
	"time"
)

type Token struct {
	AccessToken          string
	AccessTokenEncrypted string    `db:"token_encrypted"`
	UserID               string    `db:"user_id"`
	DeviceID             string    `db:"device_id"`
	LastSeen             time.Time `db:"last_seen"`
}

// TokensTable remembers sync v2 tokens
type TokensTable struct {
	db *sqlx.DB
	// A separate secret used to en/decrypt access tokens prior to / after retrieval from the database.
	// This provides additional security as a simple SQL injection attack would be insufficient to retrieve
	// users access tokens due to the encryption key not living inside the database / on that machine at all.
	// https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html#separation-of-keys-and-data
	// We cannot use bcrypt/scrypt as we need the plaintext to do sync requests!
	key256 []byte
}

// NewTokensTable creates the syncv3_sync2_tokens table if it does not already exist.
func NewTokensTable(db *sqlx.DB, secret string) *TokensTable {
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_sync2_tokens (
		token_hash TEXT NOT NULL PRIMARY KEY, -- SHA256(access token)
		token_encrypted TEXT NOT NULL,
		-- TODO: FK constraints to devices table?
		user_id TEXT NOT NULL,
		device_id TEXT NOT NULL,
		last_seen TIMESTAMP WITH TIME ZONE NOT NULL
	);`)

	// derive the key from the secret
	hash := sha256.New()
	hash.Write([]byte(secret))

	return &TokensTable{
		db:     db,
		key256: hash.Sum(nil),
	}
}

func (t *TokensTable) encrypt(token string) string {
	block, err := aes.NewCipher(t.key256)
	if err != nil {
		panic("sync2.DevicesTable encrypt: " + err.Error())
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic("sync2.DevicesTable encrypt: " + err.Error())
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		panic("sync2.DevicesTable encrypt: " + err.Error())
	}
	return hex.EncodeToString(nonce) + " " + hex.EncodeToString(gcm.Seal(nil, nonce, []byte(token), nil))
}
func (t *TokensTable) decrypt(nonceAndEncToken string) (string, error) {
	segs := strings.Split(nonceAndEncToken, " ")
	nonce := segs[0]
	nonceBytes, err := hex.DecodeString(nonce)
	if err != nil {
		return "", fmt.Errorf("decrypt nonce: failed to decode hex: %s", err)
	}
	encToken := segs[1]
	ciphertext, err := hex.DecodeString(encToken)
	if err != nil {
		return "", fmt.Errorf("decrypt token: failed to decode hex: %s", err)
	}
	block, err := aes.NewCipher(t.key256)
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	token, err := aesgcm.Open(nil, nonceBytes, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(token), nil
}

func hashToken(accessToken string) string {
	// important that this is a cryptographically secure hash function to prevent
	// preimage attacks where Eve can use a fake token to hash to an existing device ID
	// on the server.
	hash := sha256.New()
	hash.Write([]byte(accessToken))
	return hex.EncodeToString(hash.Sum(nil))
}

// Token retrieves a tokens row from the database if it exists.
// Errors with sql.NoRowsError if the token does not exist.
// Errors with an unspecified error otherwise.
func (t *TokensTable) Token(plaintextToken string) (*Token, error) {
	tokenHash := hashToken(plaintextToken)
	var token Token
	err := t.db.Get(
		&token,
		`SELECT token_encrypted, user_id, device_id, last_seen FROM syncv3_sync2_tokens WHERE token_hash=$1`,
		tokenHash,
	)
	return &token, err
}

// Insert a new token into the table.
func (t *TokensTable) Insert(plaintextToken, userID, deviceID string, lastSeen time.Time) (*Token, error) {
	hashedToken := hashToken(plaintextToken)
	encToken := t.encrypt(plaintextToken)
	_, err := t.db.Exec(
		`INSERT INTO syncv3_sync2_tokens(token_hash, token_encrypted, user_id, device_id, last_seen)
		VALUES ($1, $2, $3, $4, $5);`,
		hashedToken, encToken, userID, deviceID, lastSeen,
	)
	if err != nil {
		return nil, err
	}
	return &Token{
		AccessToken:          plaintextToken,
		AccessTokenEncrypted: encToken,
		UserID:               userID,
		DeviceID:             deviceID,
		LastSeen:             lastSeen,
	}, nil
}

// MaybeUpdateLastSeen actions a request to update a Token struct with its last_seen value
// in the DB. To avoid spamming the DB with a write every time a sync3 request arrives,
// we only update the last seen timestamp or the if it is at least 24 hours old.
// The timestamp is updated on the Token struct if and only if it is updated in the DB.
func (t *TokensTable) MaybeUpdateLastSeen(token *Token, newLastSeen time.Time) error {
	hashedToken := hashToken(token.AccessToken)
	sinceLastSeen := newLastSeen.Sub(token.LastSeen)
	if sinceLastSeen < (24 * time.Hour) {
		return nil
	}
	_, err := t.db.Exec(
		`UPDATE syncv3_sync2_tokens SET last_seen = $1 WHERE token_hash = $2`,
		newLastSeen, hashedToken,
	)
	if err != nil {
		return err
	}
	token.LastSeen = newLastSeen
	return nil
}
