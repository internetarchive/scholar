package convert

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
)

type Skip struct {
	err error
}

func (s Skip) Error() string {
	return s.err.Error()
}

var (
	ErrSkipNoTitle             = Skip{err: errors.New("no title")}
	ErrSkipCrossrefReleaseType = Skip{err: errors.New("blacklisted crossref release type")}
	ErrSkipNoDOI               = Skip{err: errors.New("no doi")}
)

// TODO: need to pass various conversion options to functions

// hashString returns a hex-encoded hash of a string.
func hashString(s string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, s)
	return fmt.Sprintf("%x", h.Sum(nil))
}
