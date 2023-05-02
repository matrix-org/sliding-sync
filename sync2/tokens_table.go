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
	AccessTokenHash      string
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
	if err != nil {
		return nil, err
	}
	token.AccessToken = plaintextToken
	token.AccessTokenHash = tokenHash
	return &token, nil
}

// TokenForPoller represents a row of the tokens table, together with any data
// maintained by pollers for that token's device.
type TokenForPoller struct {
	*Token
	Since string `db:"since"`
}

func (t *TokensTable) TokenForEachDevice() (tokens []TokenForPoller, err error) {
	// Fetches the most recently seen token for each device, see e.g.
	// https://www.postgresql.org/docs/11/sql-select.html#SQL-DISTINCT
	err = t.db.Select(
		&tokens,
		`SELECT DISTINCT ON (user_id, device_id) token_encrypted, user_id, device_id, last_seen, since
		FROM syncv3_sync2_tokens JOIN syncv3_sync2_devices USING (user_id, device_id)
		ORDER BY user_id, device_id, last_seen DESC
	`)
	if err != nil {
		return
	}
	for _, token := range tokens {
		token.AccessToken, err = t.decrypt(token.AccessTokenEncrypted)
		if err != nil {
			// Ignore decryption failure.
			continue
		}
		token.AccessTokenHash = hashToken(token.AccessToken)
	}
	return
}

// Insert a new token into the table.
func (t *TokensTable) Insert(plaintextToken, userID, deviceID string, lastSeen time.Time) (*Token, error) {
	hashedToken := hashToken(plaintextToken)
	encToken := t.encrypt(plaintextToken)
	_, err := t.db.Exec(
		`INSERT INTO syncv3_sync2_tokens(token_hash, token_encrypted, user_id, device_id, last_seen)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (token_hash) DO NOTHING;`,
		hashedToken, encToken, userID, deviceID, lastSeen,
	)
	if err != nil {
		return nil, err
	}
	return &Token{
		AccessToken:     plaintextToken,
		AccessTokenHash: hashedToken,
		// Note: if this token already exists in the DB, encToken will differ from
		// the DB token_encrypted column. (t.encrypt is nondeterministic, see e.g.
		// https://en.wikipedia.org/wiki/Probabilistic_encryption).
		// The rest of the program should ignore this field; it only lives here so
		// we can Scan the DB row into the Tokens struct. Could make it private?
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
	sinceLastSeen := newLastSeen.Sub(token.LastSeen)
	if sinceLastSeen < (24 * time.Hour) {
		return nil
	}
	_, err := t.db.Exec(
		`UPDATE syncv3_sync2_tokens SET last_seen = $1 WHERE token_hash = $2`,
		newLastSeen, token.AccessTokenHash,
	)
	if err != nil {
		return err
	}
	token.LastSeen = newLastSeen
	return nil
}

func (t *TokensTable) GetTokenAndSince(userID, deviceID, tokenHash string) (accessToken, since string, err error) {
	var encToken, gotUserID, gotDeviceID string
	query := `SELECT token_encrypted, since, user_id, device_id
	FROM syncv3_sync2_tokens JOIN syncv3_sync2_devices USING (user_id, device_id)
	WHERE token_hash = $1;`
	err = t.db.QueryRow(query, tokenHash).Scan(&encToken, &since, &gotUserID, &gotDeviceID)
	if err != nil {
		return
	}
	if gotUserID != userID || gotDeviceID != deviceID {
		err = fmt.Errorf(
			"token (hash %s) found with user+device mismatch: got (%s, %s), expected (%s, %s)",
			tokenHash, gotUserID, gotDeviceID, userID, deviceID,
		)
		return
	}
	accessToken, err = t.decrypt(encToken)
	return
}

// Delete looks up a token by its hash and deletes the row. If no token exists with the
// given hash, a warning is logged but no error is returned.
func (t *TokensTable) Delete(accessTokenHash string) error {
	result, err := t.db.Exec(
		`DELETE FROM syncv3_sync2_tokens WHERE token_hash = $1`,
		accessTokenHash,
	)
	if err != nil {
		return err
	}

	ra, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if ra != 1 {
		logger.Warn().Msgf("Tokens.Delete: expected to delete one token, but actually deleted %d", ra)
	}
	return nil
}
