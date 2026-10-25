package main

import (
	"fmt"
	"io"
	"os"

	"github.com/turin-dev/aven/internal/dotenv"
)

// maxDotenvBytes는 --import로 읽을 .env 파일의 최대 크기다.
const maxDotenvBytes = 4 << 20

// readDotenvFile은 .env 파일(또는 "-"이면 표준 입력)을 읽는다. 봉인 변수 주석(# KEY=<sealed>)은 주석이라 무시된다.
func readDotenvFile(path string) (map[string]string, error) {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxDotenvBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDotenvBytes {
		return nil, fmt.Errorf("%s: larger than 4 MiB", path)
	}
	m, err := dotenv.Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// resolvedValue는 GET .../variables/resolved 항목이다.
type resolvedValue struct {
	Value  string `json:"value"`
	Sealed bool   `json:"sealed"`
}

// visibleResolved는 봉인 항목을 빼고 값만 남긴다. 뺀 이름을 함께 돌려준다(aven run/shell은 봉인 값을 받을 수 없다).
func visibleResolved(raw map[string]resolvedValue) (map[string]string, []string) {
	out := make(map[string]string, len(raw))
	var sealed []string
	for k, v := range raw {
		if v.Sealed {
			sealed = append(sealed, k)
			continue
		}
		out[k] = v.Value
	}
	return out, sealed
}
