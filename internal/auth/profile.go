package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"dashboard/internal/audit"
)

// Username policy. Names are stored lowercase (see NormalizeUsername) and are
// the login identifier, so they must survive being typed by hand: no spaces,
// no leading/trailing punctuation, no ambiguous casing.
const (
	UsernameMinLen      = 3
	UsernameMaxLen      = 32
	maxDisplayNameRunes = 64
)

// actionPasswordChange is the audit action for anything that touches a
// password, including a failed re-authentication on a sensitive operation.
const actionPasswordChange = "auth.password_change"

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*[a-z0-9]$`)

// ErrInvalidUsername is returned when a login name fails the policy.
var ErrInvalidUsername = errors.New("username must be 3-32 characters, start and end with a letter or digit, and use only letters, digits, dot, dash or underscore")

// ErrInvalidDisplayName is returned when a display name is unreasonably long.
var ErrInvalidDisplayName = fmt.Errorf("display name must be at most %d characters", maxDisplayNameRunes)

// ErrUsernameTaken is returned when another account already uses the name.
var ErrUsernameTaken = errors.New("that username is already taken")

// ValidateUsername enforces the login-name policy. The input must already be
// normalized (lowercase, no surrounding whitespace).
func ValidateUsername(username string) error {
	if len(username) < UsernameMinLen || len(username) > UsernameMaxLen {
		return ErrInvalidUsername
	}
	if !usernamePattern.MatchString(username) {
		return ErrInvalidUsername
	}
	return nil
}

// ProfileUpdate carries the self-service profile fields a user may change.
// A nil field is left untouched, so a caller can send only what changed. An
// empty (but non-nil) DisplayName clears it; an empty Password is ignored.
type ProfileUpdate struct {
	Username    *string
	DisplayName *string
	Password    *string
}

// ProfileResult reports what an update actually did. Token is set only when
// the password changed, because that rotates every session.
type ProfileResult struct {
	User            *User
	Token           string
	UsernameChanged bool
	PasswordChanged bool
	DisplayChanged  bool
}

// Changed reports whether anything was written.
func (r *ProfileResult) Changed() bool {
	return r != nil && (r.UsernameChanged || r.PasswordChanged || r.DisplayChanged)
}

// UpdateProfile applies self-service profile changes after re-checking the
// account password, which is required even for a display-name-only edit: it
// proves the person at the keyboard owns the account, not just the session.
//
// Changing the password revokes every session of the user and returns a fresh
// token so the caller can keep the current browser signed in.
func (s *Service) UpdateProfile(ctx context.Context, user *User, currentPassword string, update ProfileUpdate, userAgent, ip string) (*ProfileResult, error) {
	if user == nil {
		return nil, ErrInvalidCredentials
	}
	if err := s.reauth(ctx, user, currentPassword, audit.ActionProfileUpdate); err != nil {
		return nil, err
	}

	result := &ProfileResult{}

	username := user.Username
	if update.Username != nil {
		next := NormalizeUsername(*update.Username)
		if next != username {
			if err := ValidateUsername(next); err != nil {
				return nil, err
			}
			taken, err := s.store.UsernameTaken(ctx, next, user.ID)
			if err != nil {
				return nil, err
			}
			if taken {
				return nil, ErrUsernameTaken
			}
			username = next
			result.UsernameChanged = true
		}
	}

	displayName := user.DisplayName
	if update.DisplayName != nil {
		next := strings.TrimSpace(*update.DisplayName)
		if len([]rune(next)) > maxDisplayNameRunes {
			return nil, ErrInvalidDisplayName
		}
		if next != displayName {
			displayName = next
			result.DisplayChanged = true
		}
	}

	var passwordHash string
	if update.Password != nil && *update.Password != "" {
		hash, err := HashPassword(*update.Password)
		if err != nil {
			return nil, ErrWeakPassword
		}
		passwordHash = hash
		result.PasswordChanged = true
	}

	if !result.Changed() {
		result.User = user
		return result, nil
	}

	if err := s.store.UpdateProfile(ctx, user.ID, username, displayName, passwordHash); err != nil {
		return nil, err
	}

	if result.PasswordChanged {
		if err := s.store.DeleteUserSessions(ctx, user.ID); err != nil {
			return nil, err
		}
		token, err := s.createSession(ctx, user, userAgent, ip)
		if err != nil {
			return nil, err
		}
		result.Token = token
		s.audit.Log(ctx, audit.Entry{
			UserID: user.ID, Username: username, Action: actionPasswordChange,
			IP: ip, Detail: "changed from the profile page; other sessions ended", Success: true,
		})
	}

	// Read the row back so the caller gets the stored values (and the new
	// login name) instead of the ones it just sent.
	updated, err := s.store.GetUserByID(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	result.User = updated

	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: username, Action: audit.ActionProfileUpdate,
		Target: username, IP: ip, Detail: describeProfileChange(user.Username, result), Success: true,
	})
	return result, nil
}

// describeProfileChange lists the changed fields for the audit trail. It never
// includes a password, only the fact that it changed.
func describeProfileChange(previousUsername string, result *ProfileResult) string {
	fields := make([]string, 0, 3)
	if result.UsernameChanged {
		fields = append(fields, fmt.Sprintf("username %s -> %s", previousUsername, result.User.Username))
	}
	if result.DisplayChanged {
		fields = append(fields, "display name")
	}
	if result.PasswordChanged {
		fields = append(fields, "password")
	}
	return strings.Join(fields, ", ")
}
