package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const lines = 249618379

func main() {
	seen := map[string]bool{}

	s := bufio.NewScanner(os.Stdin)

	count := 0
	for s.Scan() {
		l := s.Text()
		if !strings.HasPrefix(l, "/blobs.fatcat.wiki") {
			continue
		}
		if ok := seen[l]; !ok {
			seen[l] = true
			fmt.Println(strings.Replace(l, "/blobs.fatcat.wiki/thumbnail/pdf/", "", 1))
		}
		if count%10000 == 0 {
			fmt.Fprintf(os.Stderr, "%d/%d\r", count, lines)
		}
		count++
	}

	if err := s.Err(); err != nil {
		panic(err)
	}
}
