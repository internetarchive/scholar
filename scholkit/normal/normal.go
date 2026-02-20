package normal

import (
	"strings"
	"unicode"
)

type Pipeline struct {
	Normalizer []Normalizer
}

func (p *Pipeline) Normalize(s string) string {
	for _, n := range p.Normalizer {
		s = n.Normalize(s)
	}
	return s
}

type Normalizer interface {
	Normalize(string) string
}

type Simple struct{}

func (s *Simple) Normalize(v string) string {
	return strings.ToLower(v)
}

type RemoveWhitespace struct{}

func (s *RemoveWhitespace) Normalize(v string) string {
	var b strings.Builder
	for _, c := range v {
		if unicode.IsSpace(c) {
			continue
		}
		b.WriteRune(c)
	}
	b.WriteRune('\n') // re-add final newline
	return b.String()
}

type LettersOnly struct{}

func (s *LettersOnly) Normalize(v string) string {
	var b strings.Builder
	for _, c := range v {
		if !unicode.IsLetter(c) {
			continue
		}
		b.WriteRune(c)
	}
	b.WriteRune('\n') // re-add final newline
	return b.String()
}

func ProcNormAdapt(f func(string) string) func(b []byte) ([]byte, error) {
	return func(b []byte) ([]byte, error) {
		s := f(string(b))
		return []byte(s), nil
	}
}

func ReplaceNewlineAndTab(s string) string {
	var sb strings.Builder
	for _, c := range s {
		if c == '\n' || c == '\t' {
			sb.WriteString(" ")
		} else {
			sb.WriteRune(c)
		}
	}
	return sb.String()
}
