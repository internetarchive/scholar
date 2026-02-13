// sk-id converts fatcat ids to uuids and back, ported from fcid.py The classic
// fatcat database used UUID as primary keys and converted them to a base32
// string in urls.
//
// $ ./sk-id -f container_2ujzwjsay5aohfmwlpyiyhmb7a
// d5139b26-40c7-40e3-9596-5bf08c1d81f8
//
// $ ./sk-id -f container_2ujzwjsay5aohfmwlpyiyhmb7a
// d5139b26-40c7-40e3-9596-5bf08c1d81f8
//
// $ ./sk-id -u d5139b26-40c7-40e3-9596-5bf08c1d81f8
// 2ujzwjsay5aohfmwlpyiyhmb7a
//
// TODO(martin): allow to pipe lines of ids in

package main

import (
	"encoding/base32"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/miku/scholkit"
)

var (
	fromFatcat  = flag.String("f", "", "from fatcat id, e.g. container_2ujzwjsay5aohfmwlpyiyhmb7a")
	fromUUID    = flag.String("u", "", "from uuid, e.g. d5139b26-40c7-40e3-9596-5bf08c1d81f8")
	showVersion = flag.Bool("version", false, "show version")
)

var ErrInvalidLength = errors.New("invalid length")

func main() {
	flag.Parse()
	if *showVersion {
		fmt.Println(scholkit.Version)
		os.Exit(0)
	}
	var (
		result string
		err    error
	)

	switch {
	case *fromFatcat != "":
		result, err = fcid2uuid(*fromFatcat)
	case *fromUUID != "":
		result, err = uuid2fcid(*fromUUID)
	default:
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result)
}

func fcid2uuid(fcid string) (string, error) {
	var (
		parts = strings.Split(fcid, "_")
		last  = parts[len(parts)-1]
	)
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

func uuid2fcid(id string) (string, error) {
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
