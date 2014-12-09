package main

// Takes a reader

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"io"
	"io/ioutil"
	"log"
	"os"
)

// Converts a reader of a zip file into a reader of a tar file.
// Note: reads whole zip file into a buffer, which is unavoidable for the
// moment due to the implementation of zip.Reader.
func zip2tar(in io.Reader) (io.Reader, error) {

	// Read the whole input into a buffer
	var bufIn bytes.Buffer
	_, err := io.Copy(&bufIn, in)
	if err != nil {
		return nil, err
	}

	reader, writer := io.Pipe()

	go func() {
		defer writer.Close()
		tarOut := tar.NewWriter(writer)
		defer tarOut.Close()

		bufInReaderAt := bytes.NewReader(bufIn.Bytes())
		zipIn, err := zip.NewReader(bufInReaderAt, int64(bufIn.Len()))
		if err != nil {
			log.Println("Malformed zip while opening:", err)
			return
		}

		for _, file := range zipIn.File {

			isSymlink := (file.Mode() & os.ModeSymlink) != 0

			var target string
			if isSymlink {
				// File contents are the symlink target
				fd, err := file.Open()
				if err != nil {
					continue
				}
				defer fd.Close()
				tgt, err := ioutil.ReadAll(fd)
				if err != nil {
					continue
				}
				target = string(tgt)
			}

			header, err := tar.FileInfoHeader(file.FileInfo(), target)
			if err != nil {
				log.Println("Error obtaining header:", err, file.Name,
					file.Mode(), int64(file.UncompressedSize64))
				return
			}

			// For some reason FileInfoHeader removes header.Name :(
			header.Name = file.Name

			err = tarOut.WriteHeader(header)
			if err != nil {
				log.Println("Error writing header:", err, file.Name,
					file.Mode(), int64(file.UncompressedSize64))
				return
			}

			if isSymlink ||
				header.Typeflag == tar.TypeDir ||
				file.UncompressedSize64 == 0 {
				// These don't have any content, so skip writing any
				continue
			}

			fd, err := file.Open()
			if err != nil {
				// There isn't anywhere good to send this error at the moment.
				log.Println("Error: malformed zip file while opening input:",
					file.Name, err)
				return
			}

			n, err := io.Copy(tarOut, fd)
			if err != nil {
				// There isn't anywhere good to send this error at the moment.
				log.Println("Malformed zip whilst copying input:",
					err, n, file.Name, file.Mode(), file.UncompressedSize64)
				return
			}
			fd.Close()
		}
	}()

	return reader, nil
}
