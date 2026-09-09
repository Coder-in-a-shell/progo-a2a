package server

import (
	"errors"
	"io"
)

const maxRequestBodyBytes = 10 * 1024 * 1024

var errRequestBodyTooLarge = errors.New("request body exceeds 10 MiB limit")

func readRequestBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRequestBodyBytes {
		return nil, errRequestBodyTooLarge
	}
	return data, nil
}
