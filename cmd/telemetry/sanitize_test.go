package main

import (
	"strings"
	"testing"
)

func TestSanitizeIPsKeepsVersions(t *testing.T) {
	cases := map[string]string{
		"Setup uv 0.9.5":                       "Setup uv 0.9.5",
		"MariaDB 11.8.3 installed":             "MariaDB 11.8.3 installed",
		"libzvbi0t64:amd64 (0.2.44-2)":         "libzvbi0t64:amd64 (0.2.44-2)",
		"connect to 192.168.1.101 port 22":     "connect to 192.x.x.x port 22",
		"[IP: 151.101.2.132 80]":               "[IP: 151.x.x.x 80]",
		"http://10.0.0.5:8080/api":             "http://10.x.x.x:8080/api",
		"nameserver 1.1.1.1, fallback 8.8.4.4": "nameserver 1.x.x.x, fallback 8.x.x.x",
	}
	for in, want := range cases {
		if got := sanitizeIPs(in); got != want {
			t.Errorf("sanitizeIPs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactSecretsMasksCredentialsAndAddresses(t *testing.T) {
	cases := map[string]string{
		"git clone https://deploy:hunter2@git.example.org/x.git":          "git clone https://***@git.example.org/x.git",
		"REDIS_URL=redis://:s3cret@cache:6379/0":                          "REDIS_URL=redis://***@cache:6379/0",
		`psql -c "CREATE USER app WITH PASSWORD 'p4ss';"`:                 `psql -c "CREATE USER app WITH PASSWORD '***';"`,
		`mariadb -e "CREATE USER 'u'@'localhost' IDENTIFIED BY 'p4ss'"`:   `mariadb -e "CREATE USER 'u'@'localhost' IDENTIFIED BY '***'"`,
		"ALTER USER root IDENTIFIED WITH mysql_native_password BY 'p4ss'": "ALTER USER root IDENTIFIED WITH mysql_native_password BY '***'",
		"DB_PASS=p4ss":                                       "DB_PASS=***",
		"--db-password=p4ss --admin-user admin":              "--db-password=*** --admin-user admin",
		"MYSQL_ROOT_PASSWORD: p4ss":                          "MYSQL_ROOT_PASSWORD: ***",
		`{"api_key":"abc123","name":"x"}`:                    `{"api_key":"***","name":"x"}`,
		`"admin_passwd": "abc 123"`:                          `"admin_passwd": "***"`,
		"Authorization: Bearer eyJhbGciOi.x":                 "Authorization: Bearer ***",
		"curl -H 'X-Api-Key: abc123' https://x":              "curl -H 'X-Api-Key: ***' https://x",
		"GET /v1?access_token=abc123&page=2":                 "GET /v1?access_token=***&page=2",
		"GITHUB_TOKEN=ghp_abc client_secret: xyz":            "GITHUB_TOKEN=*** client_secret: ***",
		"secret_key_base = 9f8e7d":                           "secret_key_base = ***",
		"Created admin jane.doe+lab@example.com":             "Created admin ***@***",
		"inet6 2a01:4f8:c17:1234::1/64 scope global":         "inet6 2a01:x:x:x:x:x:x:x/64 scope global",
		"fe80::be24:11ff:fe12:3456%eth0":                     "fe80:x:x:x:x:x:x:x%eth0",
		"listening on [2001:db8::42]:8080":                   "listening on [2001:x:x:x:x:x:x:x]:8080",
		"connect 2001:db8:1::5: no route":                    "connect 2001:x:x:x:x:x:x:x: no route",
		"link/ether bc:24:11:a1:b2:c3 brd ff:ff:ff:ff:ff:ff": "link/ether bc:24:11:xx:xx:xx brd ff:ff:ff:xx:xx:xx",
		"HWaddr 02-42-AC-11-00-02":                           "HWaddr 02-42-AC-xx-xx-xx",
	}
	for in, want := range cases {
		if got := redactSecrets(in); got != want {
			t.Errorf("redactSecrets(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRedactSecretsLeavesLogTextAlone(t *testing.T) {
	for _, s := range []string{
		"Setting up passwd (1:4.13+dfsg1-1) ...",
		"Setting up libsecret-1-0:amd64 (0.21.4-1) ...",
		"Preparing to unpack .../passwd_1%3a4.13+dfsg1-1_amd64.deb ...",
		"Tests passed: 42, failed: 0",
		"gpu_passthrough=yes max_tokens=4096",
		"sudo: a password is required",
		`psql: error: FATAL:  password authentication failed for user "postgres"`,
		"Enter new password:",
		"Created symlink /etc/systemd/system/multi-user.target.wants/postgresql@17-main.service",
		"git@github.com: Permission denied (publickey).",
		"npm warn deprecated inflight@1.0.6: This module is not supported",
		"error[E0433]: failed to resolve: use of undeclared crate or module `crate::db`",
		"terminate called after throwing an instance of 'std::runtime_error' in Cafe::bad()",
		" 12:34:56 up 3 days,  4:05,  1 user",
		"Get:1 http://deb.debian.org/debian trixie InRelease [140 kB]",
		"Setup uv 0.9.5 at http://10.x.x.x:8080/api",
		"listen [::]:80 default_server",
		"docker pull ghcr.io/org/app@sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945",
	} {
		if got := redactSecrets(s); got != s {
			t.Errorf("redactSecrets changed ordinary text\n  in %q\n got %q", s, got)
		}
	}
}

func TestValidateRedactsFreeText(t *testing.T) {
	in := TelemetryIn{RandomID: "r", Type: "lxc", NSAPP: "x", Status: "failed",
		FailedCommand: `psql -c "ALTER USER app WITH PASSWORD 'p4ss'"`,
		ErrorMessage:  "login as admin@example.com failed",
		ErrorStep:     "Registering with TOKEN=abc",
		Error:         "exit_code=1\n---\nfatal: https://u:p4ss@git.example.org/x.git not found"}
	if err := validate(&in); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{"failed_command": in.FailedCommand, "error_message": in.ErrorMessage,
		"error_step": in.ErrorStep, "error": in.Error} {
		if strings.Contains(s, "p4ss") || strings.Contains(s, "abc") || strings.Contains(s, "admin@") {
			t.Errorf("%s kept a secret: %q", name, s)
		}
	}
}

func TestValidateAnonymisesFailedCommand(t *testing.T) {
	in := TelemetryIn{RandomID: "r", Type: "lxc", NSAPP: "x", Status: "failed",
		FailedCommand: "curl -fsSL http://192.168.1.101/setup.sh", Error: "ping 10.1.2.3 failed"}
	if err := validate(&in); err != nil {
		t.Fatal(err)
	}
	if in.FailedCommand != "curl -fsSL http://192.x.x.x/setup.sh" {
		t.Errorf("failed_command = %q", in.FailedCommand)
	}
	if in.Error != "ping 10.x.x.x failed" {
		t.Errorf("error = %q", in.Error)
	}
}
