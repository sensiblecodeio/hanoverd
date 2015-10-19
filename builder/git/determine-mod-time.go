package git

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// Return the most recent committed timestamp of each file in the whole of
// history. It's faster than invoking 'git log -1' on each file.
func CommitTimes(gitDir, revision string) (map[string]time.Time, error) {
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

			var filename string
			parts := strings.Split(line, "\t")
			op := parts[0]

			switch {
			case len(op) == 0:
				return nil, fmt.Errorf("Unexpected blank git operation: %q", parts)
			case len(parts) == 3 && (op[0] == 'R' || op[0] == 'C'):
				filename = parts[2]
			case len(parts) != 2:
				return nil, fmt.Errorf("Expected ^[A-Z]\\t(.*)$, got %q", line)
			default:
				filename = parts[1]
			}

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
