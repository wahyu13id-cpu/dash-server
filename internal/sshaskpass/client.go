package sshaskpass

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"
)

// ErrNoCredentials means the broker had nothing to answer with, so the helper
// exits non-zero and ssh falls back to the terminal.
var ErrNoCredentials = errors.New("askpass: kredensial tidak tersedia")

// Ask resolves one prompt through the broker using the environment that was
// set on the ssh child process. It is called by the "askpass" subcommand, which
// ssh invokes with the prompt as its only argument.
func Ask(prompt string) (string, error) {
	sock := os.Getenv(EnvSocket)
	token := os.Getenv(EnvToken)
	if sock == "" || token == "" {
		return "", ErrNoCredentials
	}
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return "", ErrNoCredentials
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := json.NewEncoder(conn).Encode(Request{Token: token, Prompt: prompt}); err != nil {
		return "", ErrNoCredentials
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return "", ErrNoCredentials
	}
	if resp.Status != "ok" || resp.Secret == "" {
		return "", ErrNoCredentials
	}
	return resp.Secret, nil
}

// Environment returns the environment variables the ssh child needs for the
// askpass helper to work. An empty token means no credentials are stored, and
// the caller should leave ssh alone so it prompts in the terminal.
//
// Note that OpenSSH runs SSH_ASKPASS with exec(), so the value must be a bare
// executable path: "<path> <args>" would be treated as one (nonexistent) file.
// The AskpassEnv value is therefore the binary itself and HelperEnv is what
// tells it that this invocation is the helper.
func Environment(askpassPath, sock, token string) []string {
	if askpassPath == "" || sock == "" || token == "" {
		return nil
	}
	return []string{
		AskpassEnv + "=" + askpassPath,
		RequireEnv + "=" + RequireValue,
		EnvSocket + "=" + sock,
		EnvToken + "=" + token,
		HelperEnv + "=" + HelperValue,
	}
}

// IsHelperInvocation reports whether this process was started as the askpass
// helper (as opposed to a server, a shell command, or a CLI subcommand).
func IsHelperInvocation() bool {
	return os.Getenv(HelperEnv) == HelperValue
}
