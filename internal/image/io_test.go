package image

import (
	"bytes"
	stdio "io"
)

func io(body string) stdio.ReadCloser {
	return stdio.NopCloser(bytes.NewBufferString(body))
}
