package main

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	fernetFixedBytes = 1 + 8 + 16 + 32
	fernetVersion    = 0x80
	turnStateTTL     = time.Hour
)

var (
	minFernetTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	maxFernetTime = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

type fernetInfo struct {
	IssuedAt    time.Time
	CipherBytes int
	Blocks      int
}

func parseTurnState(value string, maxBytes int) (fernetInfo, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fernetInfo{}, errors.New("turn state is empty")
	}
	if len(value) > maxBytes {
		return fernetInfo{}, fmt.Errorf("turn state exceeds %d bytes", maxBytes)
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return fernetInfo{}, errors.New("turn state contains a non-printable character")
		}
	}

	raw, errDecode := decodeBase64URL(value)
	if errDecode != nil {
		return fernetInfo{}, fmt.Errorf("decode Fernet token: %w", errDecode)
	}
	if len(raw) < fernetFixedBytes+16 {
		return fernetInfo{}, errors.New("Fernet token is too short")
	}
	if raw[0] != fernetVersion {
		return fernetInfo{}, fmt.Errorf("unexpected Fernet version 0x%02x", raw[0])
	}

	cipherBytes := len(raw) - fernetFixedBytes
	if cipherBytes < 16 || cipherBytes%16 != 0 {
		return fernetInfo{}, errors.New("Fernet ciphertext is not AES-block aligned")
	}
	seconds := binary.BigEndian.Uint64(raw[1:9])
	if seconds > uint64(maxFernetTime.Unix()) {
		return fernetInfo{}, errors.New("Fernet timestamp is out of range")
	}
	issuedAt := time.Unix(int64(seconds), 0).UTC()
	if issuedAt.Before(minFernetTime) || !issuedAt.Before(maxFernetTime) {
		return fernetInfo{}, errors.New("Fernet timestamp is out of range")
	}
	return fernetInfo{IssuedAt: issuedAt, CipherBytes: cipherBytes, Blocks: cipherBytes / 16}, nil
}

func decodeBase64URL(value string) ([]byte, error) {
	value = strings.TrimRight(value, "=")
	return base64.RawURLEncoding.DecodeString(value)
}
