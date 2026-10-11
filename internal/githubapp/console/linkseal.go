package console

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/truvity/sluis/internal/githubapp"
)

// A link needs the directory's word on addresses that GitHub names only after
// the person's code is spent. The module therefore stops after the exchange and
// hands the issuer the addresses to resolve, with the state it needs to resume
// sealed under a key only the module holds (the link App's client secret): the
// issuer carries it and can read none of it, and a copy cannot be replayed after
// [linkContinueWindow].

// linkContinueWindow is how long a sealed link state is good for.
const linkContinueWindow = 2 * time.Minute

type linkState struct {
	Expires  time.Time            `json:"exp"`
	Tokens   githubapp.UserTokens `json:"tokens"`
	UserID   int64                `json:"user_id"`
	Login    string               `json:"login"`
	Verified []string             `json:"verified"`
}

func linkSealKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("sluis github link continuation v1"))
	return mac.Sum(nil)
}

func sealLinkState(secret string, st linkState) (string, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(linkSealKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, raw, nil)), nil
}

var errLinkState = errors.New("the link state is not valid or has expired")

func openLinkState(secret, sealed string, now time.Time) (linkState, error) {
	var st linkState
	box, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return st, errLinkState
	}
	block, err := aes.NewCipher(linkSealKey(secret))
	if err != nil {
		return st, errLinkState
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(box) < gcm.NonceSize() {
		return st, errLinkState
	}
	raw, err := gcm.Open(nil, box[:gcm.NonceSize()], box[gcm.NonceSize():], nil)
	if err != nil || json.Unmarshal(raw, &st) != nil || !now.Before(st.Expires) {
		return linkState{}, errLinkState
	}
	return st, nil
}
