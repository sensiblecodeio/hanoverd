package git

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// Return the most recent committed timestamp of each file in the whole of
// history. It's faster than invoking 'git log -1' on each file.
func GitCommitTimes(gitDir, revision string) (map[string]time.Time, error) {
	times := map[string]time.Time{}

	cmd := Command(gitDir, "git", "log", "--format=-\n%cd", "--date=rfc2822",
		"--name-status", revision)
	cmd.Stdout = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	const (
		StateFilenames = iota
		StateTimestamp
		StateBlankline
	)

	parseState := StateFilenames
	currentTime := time.Now()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		line := scanner.Text()

		// log.Printf("state %v line %q", parseState, line)
		if line == "-" {
			// Single blank line means StateTimestamp follows
			parseState = StateTimestamp
			continue
		}

		switch parseState {
		case StateFilenames:
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("Expected ^[A-Z]\\t(.*)$, got %q", line)
			}

			filename := parts[1]

			if _, seen := times[filename]; seen {
				// Take only the first timestamp encountered, it's the most
				// recent.
				continue
			}

			times[filename] = currentTime

		case StateTimestamp:
			in := strings.TrimSpace(line)
			currentTime, err = time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", in)
			if err != nil {
				return nil, err
			}
			parseState = StateBlankline

		case StateBlankline:
			parseState = StateFilenames
		}
	}

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return times, nil
}
