// Package fcid converts fatcat identifiers to UUIDs and back. The classic
// fatcat database used UUIDs as primary keys and encoded them as 26-character
// base32 strings in URLs and idents (e.g. container_2ujzwjsay5aohfmwlpyiyhmb7a).
//
// The fatcat v2 API takes UUIDs in its paths, but the Elasticsearch
// fatcat_container index returns base32 idents, so we convert between the two.
//
// Copied from scholkit/cmd/sk-id (which lives in package main and is not
// importable); ultimately ported from fcid.py.
package fcid

import (
	"encoding/base32"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalidLength is returned when a fatcat ident's base32 tail is not the
// expected 26 characters.
var ErrInvalidLength = errors.New("invalid ident length")

// ToUUID converts a fatcat ident to its canonical UUID string. The ident may
// carry a type prefix (e.g. "container_2ujzw..."), which is stripped.
func ToUUID(fcid string) (string, error) {
	parts := strings.Split(fcid, "_")
	last := parts[len(parts)-1]
	if len(last) != 26 {
		return "", ErrInvalidLength
	}
	last = strings.ToUpper(last) + "======"
	b, err := base32.StdEncoding.DecodeString(last)
	if err != nil {
		return "", err
	}
	u, err := uuid.FromBytes(b)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// FromUUID converts a canonical UUID string to a 26-character fatcat base32
// ident (without any type prefix).
func FromUUID(id string) (string, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return "", err
	}
	b, err := u.MarshalBinary()
	if err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.EncodeToString(b))[:26], nil
}
