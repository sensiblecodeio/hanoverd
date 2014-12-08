package main

import (
	"io"
	"net/http"
)

// An io.Writer which wraps a http.ResponseWriter so that every write flushes.
type flushWriter struct {
	f http.Flusher
	w io.Writer
}

func NewFlushWriter(w http.ResponseWriter) *flushWriter {
	return &flushWriter{w.(http.Flusher), w}
}

func (fw *flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return
}
