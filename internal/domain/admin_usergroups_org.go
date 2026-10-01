package domain

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// The admin.usergroups.* organization methods' documented argument limits.
const (
	// MaxAdminUserGroupUsers is the most users admin.usergroups.addUsers and
	// removeUsers take in one call.
	MaxAdminUserGroupUsers = 15000
	// MaxAdminUserGroupTeams is the most workspaces
	// admin.usergroups.removeTeams takes in one call.
	MaxAdminUserGroupTeams = 100
)

// The reasons an organization group's invalid_users entries carry.
const (
	// UserGroupRefusalGuest is a guest, whom an organization group does not
	// admit. It is the reason Slack's reference shows.
	UserGroupRefusalGuest = "guest_user"
	// UserGroupRefusalNotFound is an uploaded row naming nobody who is an
	// active member of the organization.
	UserGroupRefusalNotFound = "user_not_found"
)

// UserGroupPatch is admin.usergroups.update's arguments: a nil field is one the
// caller omitted, which leaves the property as it is.
type UserGroupPatch struct {
	Name        *string
	Handle      *string
	Description *string
	Visible     *bool
}

// UserGroupMemberRefusal is one user an organization group did not admit.
type UserGroupMemberRefusal struct {
	UserID UserID
	Reason string
}

// UserGroupMembershipResult is the outcome of adding users to an organization
// group: the group afterwards, how many of the named users are members of it,
// and the ones it refused.
type UserGroupMembershipResult struct {
	Group     UserGroup
	Succeeded int
	Invalid   []UserGroupMemberRefusal
}

// UserGroupCSVRow is one row of an admin.usergroups.uploadUsers file, in the
// documented "member id, email" format. Either column may be empty, not both.
type UserGroupCSVRow struct {
	UserID UserID
	Email  string
}

// ParseUserGroupCSV reads an admin.usergroups.uploadUsers file. Each record is
// a member ID and an optional email address; a first record naming the columns
// rather than a member is a header and is skipped. A file that is not CSV, or a
// record with more than the two documented columns or with neither, is
// ErrUnparseableUserGroupFile.
func ParseUserGroupCSV(text string) ([]UserGroupCSVRow, error) {
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	rows := make([]UserGroupCSVRow, 0)
	for first := true; ; first = false {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil || len(record) > 2 {
			return nil, ErrUnparseableUserGroupFile
		}
		row := UserGroupCSVRow{UserID: UserID(strings.TrimSpace(record[0]))}
		if len(record) == 2 {
			row.Email = strings.TrimSpace(record[1])
		}
		if first && isUserGroupCSVHeader(row) {
			continue
		}
		if row.UserID == "" && row.Email == "" {
			return nil, ErrUnparseableUserGroupFile
		}
		rows = append(rows, row)
	}
}

func isUserGroupCSVHeader(row UserGroupCSVRow) bool {
	switch strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(string(row.UserID), "_", " ")), " ")) {
	case "member id", "user id", "id":
		return true
	}
	return false
}
