package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

type Account struct {
	Username     string
	PasswordHash string
	Revision     string
}
type Control struct {
	db     *sql.DB
	dir    string
	authMu sync.Mutex
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{2,31}$`)

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func validPassword(password string, min int) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= min && utf8.RuneCountInString(password) <= 128
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	if !validPassword(password, 0) {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, rounds uint32
	var parallel uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &rounds, &parallel); err != nil || n != 3 || memory < 8192 || memory > 65536 || rounds < 1 || rounds > 10 || parallel < 1 || parallel > 4 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, rounds, memory, parallel, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func openControl(dir string) (*Control, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "control.sqlite3")
	db, err := sql.Open("sqlite", sqliteDSN(path, "_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS account(id INTEGER PRIMARY KEY CHECK(id=1),username TEXT NOT NULL,password_hash TEXT NOT NULL,revision TEXT NOT NULL,updated_at REAL NOT NULL);
 CREATE TABLE IF NOT EXISTS settings(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);`)
	if err == nil {
		err = os.Chmod(path, 0600)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Control{db: db, dir: dir}, nil
}

func (c *Control) account(ctx context.Context) (Account, error) {
	a := Account{}
	err := c.db.QueryRowContext(ctx, "SELECT username,password_hash,revision FROM account WHERE id=1").Scan(&a.Username, &a.PasswordHash, &a.Revision)
	return a, err
}

func (c *Control) bootstrap(username, password string, settings Settings) (bool, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if _, err := c.account(context.Background()); err == nil {
		return false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	generated := password == ""
	var err error
	if generated {
		password, err = randomToken(24)
		if err != nil {
			return false, err
		}
	}
	if !usernamePattern.MatchString(username) || !validPassword(password, 12) {
		return false, errors.New("初始账号或密码格式无效")
	}
	if err = settings.validate(); err != nil {
		return false, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return false, err
	}
	revision, err := randomToken(32)
	if err != nil {
		return false, err
	}
	value, err := json.Marshal(settings)
	if err != nil {
		return false, err
	}
	tx, err := c.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO account VALUES(1,?,?,?,?)", username, hash, revision, unixNow()); err != nil {
		return false, err
	}
	if _, err = tx.Exec("INSERT INTO settings VALUES(1,?)", string(value)); err != nil {
		return false, err
	}
	if generated {
		file, err := os.OpenFile(filepath.Join(c.dir, "initial-admin.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return false, err
		}
		_, writeErr := fmt.Fprintf(file, "Username: %s\nPassword: %s\nChange these in Account Settings after signing in.\n", username, password)
		closeErr := file.Close()
		if writeErr != nil {
			return false, writeErr
		}
		if closeErr != nil {
			return false, closeErr
		}
	}
	return true, tx.Commit()
}

func (c *Control) authenticate(ctx context.Context, username, password string) (Account, bool, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	a, err := c.account(ctx)
	if err != nil {
		return a, false, err
	}
	valid := verifyPassword(a.PasswordHash, password)
	return a, valid && subtle.ConstantTimeCompare([]byte(a.Username), []byte(username)) == 1, nil
}

func (c *Control) changeAccount(ctx context.Context, revision, current, username, password string) (bool, error) {
	if !usernamePattern.MatchString(username) {
		return false, errors.New("用户名须为 3–32 位字母、数字、点、下划线或短横线")
	}
	if password != "" && !validPassword(password, 15) {
		return false, errors.New("新密码须为 15–128 位，可以使用长密码或口令")
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	a, err := c.account(ctx)
	if err != nil {
		return false, err
	}
	if a.Revision != revision || !verifyPassword(a.PasswordHash, current) {
		return false, nil
	}
	if password == "" && username == a.Username {
		return false, errors.New("请填写新的用户名或密码")
	}
	hash := a.PasswordHash
	if password != "" {
		hash, err = hashPassword(password)
		if err != nil {
			return false, err
		}
	}
	next, err := randomToken(32)
	if err != nil {
		return false, err
	}
	result, err := c.db.ExecContext(ctx, "UPDATE account SET username=?,password_hash=?,revision=?,updated_at=? WHERE id=1 AND revision=?", username, hash, next, unixNow(), revision)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed > 0 {
		_ = os.Remove(filepath.Join(c.dir, "initial-admin.txt"))
	}
	return changed > 0, err
}

func (c *Control) resetPassword(ctx context.Context, username, password string) error {
	if !validPassword(password, 15) {
		return errors.New("新密码须为 15–128 位")
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	a, err := c.account(ctx)
	if err != nil {
		return err
	}
	if username == "" {
		username = a.Username
	}
	if !usernamePattern.MatchString(username) {
		return errors.New("用户名格式无效")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	revision, err := randomToken(32)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, "UPDATE account SET username=?,password_hash=?,revision=?,updated_at=? WHERE id=1", username, hash, revision, unixNow())
	if err == nil {
		_ = os.Remove(filepath.Join(c.dir, "initial-admin.txt"))
	}
	return err
}

func (c *Control) loadSettings(ctx context.Context) (Settings, error) {
	var text string
	err := c.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE id=1").Scan(&text)
	if err != nil {
		return Settings{}, err
	}
	return decodeSettings([]byte(text))
}
func (c *Control) saveSettings(ctx context.Context, s Settings) error {
	value, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, "UPDATE settings SET value=? WHERE id=1", string(value))
	return err
}
func unixNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }
