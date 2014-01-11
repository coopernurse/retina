package retinaws

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
)

var endOfLine = []byte("\r\n")

func RandHex(bytes int) string {
	buf := make([]byte, bytes)
	io.ReadFull(rand.Reader, buf)
	return fmt.Sprintf("%x", buf)
}

func ParseFrame(body []byte) (map[string][]string, []byte) {
	headers := make(map[string][]string)

	pos := 0
	chunkStart := 0
	end := len(body) - 1
	for pos < end {
		if bytes.Equal(body[pos:pos+2], endOfLine) {
			if pos == chunkStart {
				body = body[pos+2:]
				break
			} else {
				line := string(body[chunkStart:pos])
				split := strings.Index(line, ":")
				if split > -1 && (split+1) < len(line) {
					name := strings.TrimSpace(line[0:split])
					val := strings.TrimSpace(line[split+1:])
					arr, ok := headers[name]
					if ok {
						arr = append(arr, val)
					} else {
						arr = []string{val}
					}
					headers[name] = arr
				}

				pos += 2
				chunkStart = pos
			}
		} else {
			pos++
		}
	}

	return headers, body
}

func WriteFrame(headers map[string][]string, data []byte) []byte {
	buf := &bytes.Buffer{}

	if headers != nil {
		for key, vals := range headers {
			for _, val := range vals {
				buf.WriteString(key)
				buf.WriteString(":")
				buf.WriteString(val)
				buf.Write(endOfLine)
			}
		}
	}

	buf.Write(endOfLine)
	if data != nil {
		buf.Write(data)
	}

	return buf.Bytes()
}
