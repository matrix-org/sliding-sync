package sync2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/getsentry/sentry-go"
	"io"
	"os"
	"strings"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/rs/zerolog"
)

var log = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type Device struct {
	UserID               string `db:"user_id"`
	DeviceID             string `db:"device_id"`
	Since                string `db:"since"`
	AccessToken          string
	AccessTokenEncrypted string `db:"v2_token_encrypted"`
}

// Storage remembers sync v2 tokens per-device
type Storage struct {
	db *sqlx.DB
	// A separate secret used to en/decrypt access tokens prior to / after retrieval from the database.
	// This provides additional security as a simple SQL injection attack would be insufficient to retrieve
	// users access tokens due to the encryption key not living inside the database / on that machine at all.
	// https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html#separation-of-keys-and-data
	// We cannot use bcrypt/scrypt as we need the plaintext to do sync requests!
	key256 []byte
}

func NewStore(postgresURI, secret string) *Storage {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		sentry.CaptureException(err)
		// TODO: if we panic(), will sentry have a chance to flush the event?
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_sync2_devices (
		device_id TEXT PRIMARY KEY, -- user_id + Unit Separator + actual device_id from homeserver
		user_id TEXT NOT NULL,      -- populated from /whoami
		v2_token_encrypted TEXT NOT NULL,
		since TEXT NOT NULL
	);`)

	// derive the key from the secret
	hash := sha256.New()
	hash.Write([]byte(secret))

	return &Storage{
		db:     db,
		key256: hash.Sum(nil),
	}
}

func (s *Storage) Teardown() {
	err := s.db.Close()
	if err != nil {
		panic("V2Storage.Teardown: " + err.Error())
	}
}

func (s *Storage) encrypt(token string) string {
	block, err := aes.NewCipher(s.key256)
	if err != nil {
		panic("sync2.Storage encrypt: " + err.Error())
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic("sync2.Storage encrypt: " + err.Error())
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		panic("sync2.Storage encrypt: " + err.Error())
	}
	return hex.EncodeToString(nonce) + " " + hex.EncodeToString(gcm.Seal(nil, nonce, []byte(token), nil))
}
func (s *Storage) decrypt(nonceAndEncToken string) (string, error) {
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
	block, err := aes.NewCipher(s.key256)
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

func (s *Storage) Device(deviceID string) (*Device, error) {
	var d Device
	err := s.db.Get(&d, `SELECT device_id, user_id, since, v2_token_encrypted FROM syncv3_sync2_devices WHERE device_id=$1`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup device '%s': %s", deviceID, err)
	}
	d.AccessToken, err = s.decrypt(d.AccessTokenEncrypted)
	return &d, err
}

// DeviceByPlaintextAccessToken looks up an existing device by an access token.
// If we fail to do so, this returns a nil *Device, along with either
//   - an sql.ErrNoRows error if no such device exists, or
//   - an unspecified error otherwise.
func (s *Storage) DeviceByPlaintextAccessToken(plainToken string) (*Device, error) {
	var d Device
	encToken := s.encrypt(plainToken)
	err := s.db.Get(&d, `SELECT device_id, user_id, since, v2_token_encrypted FROM syncv3_sync2_devices WHERE v2_token_encrypted=$1`, encToken)
	if err != nil {
		return nil, err
	}
	d.AccessToken = plainToken
	return &d, err
}

func (s *Storage) AllDevices() (devices []Device, err error) {
	err = s.db.Select(&devices, `SELECT device_id, user_id, since, v2_token_encrypted FROM syncv3_sync2_devices`)
	if err != nil {
		return
	}
	for i := range devices {
		devices[i].AccessToken, _ = s.decrypt(devices[i].AccessTokenEncrypted)
	}
	return
}

func (s *Storage) RemoveDevice(deviceID string) error {
	_, err := s.db.Exec(
		`DELETE FROM syncv3_sync2_devices WHERE device_id = $1`, deviceID,
	)
	log.Info().Str("device", deviceID).Msg("Deleting device")
	return err
}

func (s *Storage) InsertDevice(userID, deviceID, accessToken string) (*Device, error) {
	var device Device
	device.AccessToken = accessToken
	device.AccessTokenEncrypted = s.encrypt(accessToken)
	err := sqlutil.WithTransaction(s.db, func(txn *sqlx.Tx) error {
		// make sure there is a device entry for this device ID. If one already exists, don't clobber
		// the since value else we'll forget our position!
		row := txn.QueryRow(`
			INSERT INTO syncv3_sync2_devices(device_id, since, user_id, v2_token_encrypted) VALUES($1,$2,$3,$4)
			ON CONFLICT (device_id) DO
			-- overwrite with the latest access token, but keep the old since value
			UPDATE SET v2_token_encrypted = excluded.v2_token_encrypted
			RETURNING since`,
			deviceID, "", userID, device.AccessTokenEncrypted,
		)
		err := row.Scan(&device.Since)
		if err != nil {
			return err
		}
		device.UserID = userID
		device.DeviceID = deviceID
		return nil
	})
	return &device, err
}

func (s *Storage) UpdateDeviceSince(deviceID, since string) error {
	_, err := s.db.Exec(`UPDATE syncv3_sync2_devices SET since = $1 WHERE device_id = $2`, since, deviceID)
	return err
}
