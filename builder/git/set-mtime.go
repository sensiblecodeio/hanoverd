package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func SetMTimes(git_dir, checkout_dir, ref string) error {
	// From https://github.com/rosylilly/git-set-mtime with tweaks
	// Copyright (c) 2014 Sho Kusano

	// MIT License

	// Permission is hereby granted, free of charge, to any person obtaining
	// a copy of this software and associated documentation files (the
	// "Software"), to deal in the Software without restriction, including
	// without limitation the rights to use, copy, modify, merge, publish,
	// distribute, sublicense, and/or sell copies of the Software, and to
	// permit persons to whom the Software is furnished to do so, subject to
	// the following conditions:

	// The above copyright notice and this permission notice shall be
	// included in all copies or substantial portions of the Software.

	// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
	// EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
	// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
	// NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
	// LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
	// OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
	// WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

	lsFiles := Command(git_dir, "git", "ls-tree", "-z", "-r", "--name-only", ref)
	lsFiles.Stdout = nil
	out, err := lsFiles.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %v", err)
	}

	dirMTimes := map[string]time.Time{}

	chtime := func(path string, atime, mtime time.Time) error {
		// https://github.com/torvalds/linux/blob/2decb2682f80759f631c8332f9a2a34a02150a03/include/uapi/linux/fcntl.h#L56
		var utimes [2]unix.Timespec
		utimes[0] = unix.NsecToTimespec(atime.UnixNano())
		utimes[1] = unix.NsecToTimespec(mtime.UnixNano())

		if e := unix.UtimesNanoAt(unix.AT_FDCWD, path, utimes[0:], unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return &os.PathError{"futimesat", path, e}
		}
		return nil
	}

	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	for _, file := range files {
		gitLog := Command(git_dir, "git", "log", "-1", "--date=rfc2822",
			"--format=%cd", ref, "--", file)
		gitLog.Stdout = nil

		out, err := gitLog.Output()
		if err != nil {
			return fmt.Errorf("git log: %v", err)
		}

		mStr := strings.TrimSpace(strings.TrimLeft(string(out), "Date:"))
		mTime, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", mStr)
		if err != nil {
			return fmt.Errorf("parse time: %v", err)
		}

		// Loop over each directory in the path to `file`, updating `dirMTimes`
		// to take the most recent time seen.
		dir := filepath.Dir(file)
		for {
			if other, ok := dirMTimes[dir]; ok {
				if mTime.After(other) {
					// file mTime is more recent than previous seen for 'dir'
					dirMTimes[dir] = mTime
				}
			} else {
				// first occurrence of dir
				dirMTimes[dir] = mTime
			}

			// Remove one directory from the path until it isn't changed anymore
			if dir == filepath.Dir(dir) {
				break
			}
			dir = filepath.Dir(dir)
		}

		err = chtime(checkout_dir+"/"+file, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}

		fmt.Printf("%s: %s\n", file, mTime)
	}

	for dir, mTime := range dirMTimes {
		err = chtime(checkout_dir+"/"+dir, mTime, mTime)
		if err != nil {
			return fmt.Errorf("chtimes: %v", err)
		}
		fmt.Printf("%s: %s\n", dir, mTime)
	}
	return nil
}
