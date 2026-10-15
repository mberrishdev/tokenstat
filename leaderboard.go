package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The leaderboard gets daily token totals by tool and model. It never gets
// projects, paths, sessions, commands, or titles.

const defaultServer = "https://tokenstat-leaderboard.vercel.app"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// credentials are what `tokenstat login` keeps on disk.
type credentials struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	Login  string `json:"login"`
}

func configDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tokenstat")
}

func credentialsPath() string { return filepath.Join(configDir(), "leaderboard.json") }

// deviceID returns the random id of this machine, and makes one the first
// time. The leaderboard keeps the rows of each machine apart and adds them
// up, so two laptops on one account both count. The id lives apart from the
// login: a new login on the same machine must not look like a new machine,
// or its days would count twice.
func deviceID() (string, error) {
	path := filepath.Join(configDir(), "device")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(path, []byte(id+"\n"), 0o600)
}

func readCredentials() (credentials, error) {
	var c credentials
	b, err := os.ReadFile(credentialsPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, errors.New("not logged in: run `tokenstat login` first")
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func writeCredentials(c credentials) error {
	path := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// server returns the leaderboard address: the flag, then $TOKENSTAT_SERVER,
// then the saved login, then the default.
func server(flagValue, saved string) string {
	for _, s := range []string{flagValue, os.Getenv("TOKENSTAT_SERVER"), saved, defaultServer} {
		if s != "" {
			return strings.TrimRight(s, "/")
		}
	}
	return defaultServer
}

// runCommand runs a subcommand and reports whether args named one.
func runCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "login":
		return true, cmdLogin(args[1:])
	case "logout":
		return true, cmdLogout()
	case "submit":
		return true, cmdSubmit(args[1:])
	case "help", "-h", "--help":
		fmt.Print(`Usage:
  tokenstat                 open the dashboard
  tokenstat login           sign in to the leaderboard with GitHub
  tokenstat submit          send daily token totals to the leaderboard
  tokenstat submit -dry-run show what would be sent, without sending it
  tokenstat logout          forget the leaderboard login
`)
		return true, nil
	}
	return false, nil
}

// cmdLogin signs in with the GitHub device flow, then trades the GitHub
// token for a leaderboard token. The GitHub token is not kept.
func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	srvFlag := fs.String("server", "", "leaderboard address")
	fs.Parse(args)
	saved, _ := readCredentials()
	srv := server(*srvFlag, saved.Server)

	var cfg struct {
		ClientID string `json:"githubClientId"`
	}
	if err := getJSON(srv+"/api/config", &cfg); err != nil {
		return fmt.Errorf("read leaderboard config from %s: %w (set TOKENSTAT_SERVER or -server to your leaderboard)", srv, err)
	}

	var code struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := postForm("https://github.com/login/device/code", url.Values{"client_id": {cfg.ClientID}}, &code); err != nil {
		return fmt.Errorf("start GitHub login: %w", err)
	}
	fmt.Printf("Open %s and enter the code %s\n", code.VerificationURI, code.UserCode)
	fmt.Println("Waiting for GitHub…")

	interval := time.Duration(max(code.Interval, 5)) * time.Second
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	var ghToken string
	for ghToken == "" {
		if time.Now().After(deadline) {
			return errors.New("the code expired; run `tokenstat login` again")
		}
		time.Sleep(interval)
		var tok struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Interval    int    `json:"interval"`
		}
		err := postForm("https://github.com/login/oauth/access_token", url.Values{
			"client_id":   {cfg.ClientID},
			"device_code": {code.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &tok)
		if err != nil {
			return fmt.Errorf("GitHub login: %w", err)
		}
		switch tok.Error {
		case "":
			ghToken = tok.AccessToken
		case "authorization_pending":
		case "slow_down":
			interval = time.Duration(max(tok.Interval, int(interval/time.Second)+5)) * time.Second
		case "access_denied":
			return errors.New("login was cancelled on GitHub")
		default:
			return fmt.Errorf("GitHub login: %s", tok.Error)
		}
	}

	var res struct {
		Token string `json:"token"`
		Login string `json:"login"`
	}
	if err := postJSON(srv+"/api/auth/github", "", map[string]string{"githubToken": ghToken}, &res); err != nil {
		return fmt.Errorf("sign in to the leaderboard: %w", err)
	}
	if err := writeCredentials(credentials{Server: srv, Token: res.Token, Login: res.Login}); err != nil {
		return err
	}
	fmt.Printf("Logged in as %s. Run `tokenstat submit` to send your totals.\n", res.Login)
	return nil
}

func cmdLogout() error {
	err := os.Remove(credentialsPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("Logged out.")
	return nil
}

// boardRow is one row the leaderboard gets: the tokens of one tool and
// model on one local day.
type boardRow struct {
	Day        string `json:"day"` // YYYY-MM-DD
	Tool       string `json:"tool"`
	Model      string `json:"model"`
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cacheRead"`
	CacheWrite int64  `json:"cacheWrite"`
	Calls      int    `json:"calls"`
}

// dailyTotals sums the calls since the day `since` by day, tool, and model.
func dailyTotals(usage []Usage, since time.Time) []boardRow {
	type key struct{ day, tool, model string }
	sums := map[key]*boardRow{}
	for _, u := range usage {
		if u.Time.Before(since) {
			continue
		}
		tool := [numTools]string{Claude: "claude", Codex: "codex", Copilot: "copilot"}[u.Tool]
		model := u.Model
		if model == "" {
			model = "unknown"
		}
		k := key{u.Time.Format(time.DateOnly), tool, model}
		t := sums[k]
		if t == nil {
			t = &boardRow{Day: k.day, Tool: k.tool, Model: k.model}
			sums[k] = t
		}
		t.Input += u.Input
		t.Output += u.Output
		t.CacheRead += u.CacheRead
		t.CacheWrite += u.CacheWrite
		t.Calls += max(u.Calls, 1)
	}
	out := make([]boardRow, 0, len(sums))
	for _, t := range sums {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.Tool != b.Tool {
			return a.Tool < b.Tool
		}
		return a.Model < b.Model
	})
	return out
}

func cmdSubmit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	days := fs.Int("days", 90, "send the last `n` days")
	dry := fs.Bool("dry-run", false, "print the rows instead of sending them")
	srvFlag := fs.String("server", "", "leaderboard address")
	fs.Parse(args)

	creds, err := readCredentials()
	if err != nil && !*dry {
		return err
	}
	d := load()
	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day()-(*days-1), 0, 0, 0, 0, time.Local)
	rows := dailyTotals(d.usage, since)

	device, err := deviceID()
	if err != nil {
		return fmt.Errorf("device id: %w", err)
	}
	body := map[string]any{"device": device, "days": rows}
	if *dry {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(body)
	}
	if len(rows) == 0 {
		fmt.Println("No usage in the logs to send.")
		return nil
	}
	var res struct {
		Accepted int    `json:"accepted"`
		Profile  string `json:"profile"`
	}
	if err := postJSON(server(*srvFlag, creds.Server)+"/api/submit", creds.Token, body, &res); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	fmt.Printf("Sent %d rows, %s to %s.\n", res.Accepted, rows[0].Day, rows[len(rows)-1].Day)
	if res.Profile != "" {
		fmt.Println("Your profile:", res.Profile)
	}
	return nil
}

func getJSON(u string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	return do(req, out)
}

func postForm(u string, form url.Values, out any) error {
	req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(req, out)
}

func postJSON(u, token string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(req, out)
}

// do sends req and decodes a JSON reply. A reply with an error status
// returns its "error" field.
func do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tokenstat")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s (HTTP %d)", e.Error, resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}
