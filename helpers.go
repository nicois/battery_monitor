package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"time"

	"go.uber.org/zap"
)

// Must0 exits if it is given a non-nil error,
// reporting it and a stack trace.
func Must0(err error) {
	if err != nil {
		logger.Fatal("Unexpected unrecoverable error", zap.Error(err))
	}
}

// Must exits if it is given a non-nil error,
// reporting it and a stack trace.
// Otherwise, it returns the first argument
func Must[T any](result T, err error) T {
	if err != nil {
		logger.Fatal("Unexpected unrecoverable error", zap.Error(err))
	}
	return result
}

func Delete[T comparable](collection []T, el T) []T {
	idx := Find(collection, el)
	if idx > -1 {
		return slices.Delete(collection, idx, idx+1)
	}
	return collection
}

func Find[T comparable](collection []T, el T) int {
	for i := range collection {
		if collection[i] == el {
			return i
		}
	}
	return -1
}

func LoadJSON[T any](filename string) (T, error) {
	var data T
	fileData, err := os.ReadFile(filename)
	if err != nil {
		return data, err
	}
	return data, json.Unmarshal(fileData, &data)
}

func WithTimeout[T any](
	ctx context.Context,
	timeout time.Duration,
	f func(context.Context) (T, error),
) (T, error) {
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return f(ctx2)
}
