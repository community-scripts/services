package main

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

type scriptInfo struct {
	Slug, Type, Path, RunsOn string
}

type rawFetcher func(path string) ([]byte, bool)

type errScriptMissing struct{ Path string }

func (e errScriptMissing) Error() string {
	if e.Path == "" {
		return "no script path"
	}
	return e.Path + " is not on main"
}

var (
	whitespaceRun    = regexp.MustCompile(`\s+`)
	validScriptPath  = regexp.MustCompile(`^[A-Za-z0-9._/-]+\.sh$`)
	slugPattern      = regexp.MustCompile(`^[a-z0-9._-]+$`)
	appliancePattern = regexp.MustCompile(`(^|-)(pbs|pmg|pdm)([0-9-]|\.sh$)`)
	commandProbe     = regexp.MustCompile(`command -v \S*`)
	pveCommand       = regexp.MustCompile("(^|[\\s;&|(`$])(pct|qm|pveam|pvesm|pvesh|pveversion)\\s")
)

// issueField returns the first answered line under a "### <label>" heading.
func issueField(body, label string) string {
	inSection := false
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r", ""), "\n") {
		if strings.HasPrefix(line, "###") {
			if inSection {
				return ""
			}
			inSection = strings.HasPrefix(strings.TrimSpace(strings.TrimLeft(line, "#")), label)
			continue
		}
		if !inSection {
			continue
		}
		if t := strings.TrimSpace(line); t != "" && t != "_No response_" {
			return t
		}
	}
	return ""
}

func slugFromName(name string) string {
	return whitespaceRun.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
}

func scriptTypeOf(field, slug string) string {
	switch {
	case strings.HasPrefix(field, "CT"):
		return "ct"
	case strings.HasPrefix(field, "VM"):
		return "vm"
	case strings.HasPrefix(field, "Addon"):
		return "addon"
	case strings.HasPrefix(field, "PVE Tool"):
		return "pve"
	case strings.HasPrefix(field, "Other"):
		return "other"
	}
	if strings.Contains(slug, "-vm") {
		return "vm"
	}
	return "ct"
}

func resolveScript(title, body string, fetch rawFetcher) (scriptInfo, error) {
	name := issueField(body, "Name of the Script")
	if name == "" {
		name = title
	}
	slug := slugFromName(name)
	typ := scriptTypeOf(issueField(body, "Script Type"), slug)

	p := strings.TrimPrefix(whitespaceRun.ReplaceAllString(issueField(body, "Script path"), ""), "/")
	if !validScriptPath.MatchString(p) || strings.Contains(p, "..") {
		p = ""
	}
	var script []byte
	ok := false
	if p == "" && slugPattern.MatchString(slug) {
		switch typ {
		case "ct":
			p = "ct/" + slug + ".sh"
		case "vm":
			p = "vm/" + slug + ".sh"
			if script, ok = fetch(p); !ok {
				p = "vm/" + strings.TrimSuffix(slug, "-vm") + "-vm.sh"
			}
		case "addon":
			p = "tools/addon/" + slug + ".sh"
		case "pve":
			p = "tools/pve/" + slug + ".sh"
		}
	}
	if p == "" {
		return scriptInfo{}, errScriptMissing{}
	}
	if !ok {
		if script, ok = fetch(p); !ok {
			return scriptInfo{}, errScriptMissing{Path: p}
		}
	}
	return scriptInfo{Slug: slug, Type: typ, Path: p, RunsOn: runsOn(typ, p, issueField(body, "Where does it run?"), script)}, nil
}

func applianceOf(p string) string {
	m := appliancePattern.FindStringSubmatch(path.Base(p))
	if m == nil {
		return ""
	}
	return m[2]
}

func usesPveCommands(script []byte) bool {
	for _, line := range strings.Split(string(script), "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		if pveCommand.MatchString(commandProbe.ReplaceAllString(line, "")) {
			return true
		}
	}
	return false
}

func runsOn(typ, p, where string, script []byte) string {
	if typ == "ct" || typ == "vm" {
		return "pve"
	}
	switch where {
	case "Proxmox VE host":
		return "pve"
	case "Inside an LXC or VM":
		return "guest"
	case "Proxmox Backup Server":
		return "pbs"
	case "Proxmox Mail Gateway":
		return "pmg"
	case "Proxmox Datacenter Manager":
		return "pdm"
	}
	appliance := applianceOf(p)
	switch {
	case strings.HasPrefix(p, "tools/pve/"):
		if appliance != "" {
			return appliance
		}
		return "pve"
	case usesPveCommands(script):
		return "pve"
	case appliance != "":
		return appliance
	case strings.HasPrefix(p, "tools/addon/"):
		return "guest"
	}
	return "unknown"
}

func anyString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func testingMessage(title string, info scriptInfo, rawBase string, jsonBody []byte) string {
	var where string
	switch info.RunsOn + ":" + info.Type {
	case "pve:ct":
		where = "in the Proxmox VE shell (or on an Incus host) to create a container"
	case "pve:vm":
		where = "in the Proxmox VE shell to create a VM"
	default:
		switch info.RunsOn {
		case "pve":
			where = "in the Proxmox VE shell"
		case "guest":
			where = "inside the LXC or VM that should get it"
		case "pbs":
			where = "in the Proxmox Backup Server shell"
		case "pmg":
			where = "in the Proxmox Mail Gateway shell"
		case "pdm":
			where = "in the Proxmox Datacenter Manager shell"
		default:
			where = "as described in the issue"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The %s script is ready for testing. Run it %s:\n", title, where)
	fmt.Fprintf(&b, "```\nbash -c \"$(curl -fsSL %s/%s)\"\n```\n", rawBase, info.Path)

	if info.Type != "pve" && len(jsonBody) > 0 {
		var meta struct {
			Credentials struct {
				Username any `json:"username"`
				Password any `json:"password"`
			} `json:"default_credentials"`
			Notes []struct {
				Text string `json:"text"`
			} `json:"notes"`
		}
		if json.Unmarshal(jsonBody, &meta) == nil {
			user, pass := anyString(meta.Credentials.Username), anyString(meta.Credentials.Password)
			if user != "" || pass != "" {
				b.WriteString("\nDefault credentials:\n")
				if user != "" {
					fmt.Fprintf(&b, "Username: %s\n", user)
				}
				if pass != "" {
					fmt.Fprintf(&b, "Password: %s\n", pass)
				}
			}
			var notes []string
			for _, n := range meta.Notes {
				if n.Text != "" {
					notes = append(notes, n.Text)
				}
			}
			if len(notes) > 0 {
				fmt.Fprintf(&b, "\n%s\n", strings.Join(notes, "\n"))
			}
		}
	}

	if info.Type == "ct" || info.Type == "vm" {
		b.WriteString("\nNote: This is not in the official repo yet—it's just a dev version! After merging into ProxmoxVE, it will need to be recreated.\n")
	} else {
		b.WriteString("\nNote: This is not in the official repo yet—it's just a dev version!\n")
	}
	return b.String()
}
