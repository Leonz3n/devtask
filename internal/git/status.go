package git

import (
	"bytes"
	"errors"
	"fmt"
)

type WorktreeStatus struct {
	Modified   bool
	Staged     bool
	Untracked  bool
	Conflicted bool
}

func ParseStatusPorcelainV1Z(output []byte) (WorktreeStatus, error) {
	var status WorktreeStatus
	if len(output) == 0 {
		return status, nil
	}
	if output[len(output)-1] != 0 {
		return WorktreeStatus{}, errors.New("Git status porcelain output is not NUL terminated")
	}
	records := bytes.Split(output[:len(output)-1], []byte{0})
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 4 || record[2] != ' ' {
			return WorktreeStatus{}, fmt.Errorf("malformed Git status porcelain record at index %d", index)
		}
		x, y := record[0], record[1]
		if x == '?' && y == '?' {
			status.Untracked = true
		} else if isUnmergedStatus(x, y) {
			status.Conflicted = true
		} else {
			if x != ' ' && x != '!' {
				status.Staged = true
			}
			if y != ' ' && y != '!' {
				status.Modified = true
			}
		}
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			index++
			if index >= len(records) {
				return WorktreeStatus{}, errors.New("Git status porcelain rename/copy record is missing its original path")
			}
		}
	}
	return status, nil
}

func isUnmergedStatus(x, y byte) bool {
	switch [2]byte{x, y} {
	case [2]byte{'D', 'D'}, [2]byte{'A', 'U'}, [2]byte{'U', 'D'}, [2]byte{'U', 'A'}, [2]byte{'D', 'U'}, [2]byte{'A', 'A'}, [2]byte{'U', 'U'}:
		return true
	default:
		return false
	}
}
