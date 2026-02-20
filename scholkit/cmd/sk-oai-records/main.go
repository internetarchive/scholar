// sk-oai-records was used as a first step to go from concatenated metha OAI
// XML file (invalid, hard to parse) to properly separated XML documents (using
// ASCII record separator). The issue was that there was an imbalance of
// opening and closing record tags in the raw concatenated data; ultimalely
// conversions failed after 128M record, while the there were actually about
// 495M records in the dataset.
//
// This script finds a record and writes it out, and adding "RECORD SEPARATOR"
// (https://en.wikipedia.org/wiki/C0_and_C1_control_codes#Field_separators) in
// between.
//
// In 02/2025 the raw input was 1.3TB of XML and this script could run at about
// 1GB/s, so conversion of the whole file takes about 30min.
package main

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"os"
)

var (
	start = []byte("<record ")
	end   = []byte("</record>")
	sep   = []byte{'\n', 30, '\n'} // RECORD SEPARATOR
)

func main() {
	var (
		br        = bufio.NewReader(os.Stdin)
		readBuf   = make([]byte, 8192)
		buf       []byte
		insideTag = false
		bw        = bufio.NewWriter(os.Stdout)
		i         int
	)
	defer bw.Flush()
LOOP:
	for {
		n, err := br.Read(readBuf)
		if err == io.EOF || n == 0 {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		buf = append(buf, readBuf[:n]...)
		for len(buf) > 0 {
			if !insideTag {
				i = bytes.Index(buf, start)
				if i == -1 {
					buf = buf[0:0]
					continue LOOP
				}
				insideTag = true
				buf = buf[i:]
			} else {
				i = bytes.Index(buf, end)
				if i == -1 {
					continue LOOP
				}
				record := buf[:i+len(end)]
				_, _ = bw.Write(record)
				_, _ = bw.Write(sep)
				buf = buf[i+len(end):]
				insideTag = false
			}
		}
	}
}
