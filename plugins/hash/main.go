// Command hash is toy's exec plugin example: it hashes a file or a text with
// several algorithms, reporting progress on large files and stopping on Cancel.
package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"strings"

	"toy/sdk/toyplugin"
)

func main() {
	toyplugin.Handle("hash", run)
	toyplugin.Serve()
}

func run(c *toyplugin.Call) (any, error) {
	names := []string{"MD5", "SHA-1", "SHA-256", "SHA-512"}
	hashes := []hash.Hash{md5.New(), sha1.New(), sha256.New(), sha512.New()}
	all := io.MultiWriter(hashes[0], hashes[1], hashes[2], hashes[3])

	if path := strings.TrimSpace(c.String("file")); path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		c.Log("hashing %s (%d bytes)", path, st.Size())
		buf := make([]byte, 1<<20)
		var done int64
		for {
			if c.Context.Err() != nil {
				return nil, errors.New("cancelled")
			}
			n, err := f.Read(buf)
			all.Write(buf[:n])
			done += int64(n)
			if st.Size() > 0 {
				c.Progress(float64(done) / float64(st.Size()))
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
		}
	} else {
		io.WriteString(all, c.String("text"))
	}

	rows := make([][]string, len(names))
	digests := make([]string, len(names))
	for i, h := range hashes {
		digests[i] = hex.EncodeToString(h.Sum(nil))
		if c.Bool("upper") {
			digests[i] = strings.ToUpper(digests[i])
		}
		rows[i] = []string{names[i], digests[i]}
	}
	return map[string]any{
		"digests": map[string]any{"columns": []string{"算法", "摘要"}, "rows": rows},
		"sha256":  digests[2],
	}, nil
}
