package tools

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Refusals a regular chatter's tool call gets instead of running. The
// text goes to the model, which answers the person in its own voice.
const (
	refuseSkillFiles = "refused: private configuration. A skill's files (scripts, references, " +
		"resources) belong to the agent and are not shown, listed or copied for anyone but its owner. " +
		"Run the skill's script to use it. Don't try another way to read them; tell the person, in " +
		"your own voice, that this part isn't something you can share."
	refuseFileDrop = "refused: public file-sharing services aren't used for anyone's files. " +
		"Reference the file in your reply as [name](/workspace/<file>): the runtime delivers it, and on " +
		"WeChat it also comes with a download link of our own, valid 7 days."
	refuseInfraProbe = "refused: private infrastructure. The service's own network details " +
		"(egress/public IP, host, cloud provider, region, metadata) are not shared. " +
		"Don't try another way to find them; tell the person, in your own voice, that you can't share " +
		"where you run. (Their own IP is something they can check on their own device.)"
)

// ipProbeHosts answer "what is my IP" or expose cloud metadata. Looking
// one up from the sandbox or from web_fetch reports where WE run.
var ipProbeHosts = []string{
	"ipify.org", "ifconfig.me", "ifconfig.co", "ifconfig.io", "ifcfg.me", "icanhazip.com",
	"ipinfo.io", "ip.sb", "ipecho.net", "ident.me", "checkip.amazonaws.com", "checkip.dyndns",
	"myip.com", "ip-api.com", "ipapi.co", "ipapi.is", "httpbin.org/ip", "whatismyip",
	"ipip.net", "cip.cc", "ip.cn", "ip138.com", "myexternalip.com", "wtfismyip.com", "ip.me",
	"ipwho.is", "ipwhois.", "ip.gs", "seeip.org", "2ip.", "ip.tool.lu", "myip.la", "3322.net",
	"3322.org", "myip.opendns.com", "o-o.myaddr.l.google.com", "whoami.akamai.net",
	"whoami.cloudflare", "ipleak.net", "browserleaks.com", "iplocation.net", "db-ip.com",
	"ipgeolocation.io", "ipdata.co", "ipstack.com", "geojs.io", "ip.nf", "ip.la",
	"metadata.google.internal", "169.254.169.254", "100.100.100.200", "fd00:ec2::254",
}

// fileDropHosts are public "upload and get a link" services. A chatter's
// files (and the agent's outputs) never go there: anyone with the link
// can read them, and the runtime already delivers files itself.
var fileDropHosts = []string{
	"gofile.io", "tmpfiles.org", "catbox.moe", "litterbox.", "file.io", "bashupload",
	"temp.sh", "transfer.sh", "0x0.st", "pixeldrain.com", "uguu.se", "filebin.net",
	"anonfiles", "ufile.io", "sendspace.com", "wetransfer.com", "dropmefiles",
	"tempfile", "keep.sh", "oshi.at", "x0.at", "paste.rs", "termbin.com", "ix.io",
}

func mentionsFileDrop(s string) bool {
	s = strings.ToLower(s)
	for _, h := range fileDropHosts {
		if strings.Contains(s, h) {
			return true
		}
	}
	return false
}

func mentionsIPProbe(s string) bool {
	s = strings.ToLower(s)
	for _, h := range ipProbeHosts {
		if strings.Contains(s, h) {
			return true
		}
	}
	return false
}

// underBundledSkills: the sandbox's /skills mount holds the agent's,
// owner's and system skills. A chatter's own skills live under the
// workspace (relative skills/...), which stays readable.
func underBundledSkills(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "/") {
		return false
	}
	c := path.Clean(p)
	return c == "/skills" || strings.HasPrefix(c, "/skills/")
}

var (
	skillsRef = regexp.MustCompile(`(^|[\s'"=:(])/skills(/|\s|$|['"])`)
	// Commands that print, copy, pack or search files. Running a skill
	// (python /skills/x/main.py) doesn't need any of them.
	fileReaderCmd = regexp.MustCompile(`(?i)(^|[\s;&|(` + "`" + `])(cat|head|tail|less|more|sed|awk|gawk|grep|egrep|rg|ag|cp|mv|scp|rsync|tar|zip|7z|gzip|base64|base32|xxd|hexdump|od|strings|ls|find|tree|du|stat|file|vi|vim|nano|emacs|diff|cmp|wc|nl|rev|tac|dd|tee|ln|install|curl|wget|nc|openssl|split|fold|cut|sort|uniq|column|jq|yq|bat|type|realpath|readlink|zcat|bzcat)(\s|$)`)
	quoted        = regexp.MustCompile(`'[^']*'|"(?:[^"\\]|\\.)*"`)
	// Inline code can open any file.
	inlineCode = regexp.MustCompile(`(?i)(python3?|node|bun|deno|ruby|perl|php)\s+(-c|-e|-p|--eval|-)(\s|$)|<<|\bopen\s*\(|read_?text|readfile|fs\.read|\bexec\s*\(|\beval\s*\(|\bsource\s|(^|[\s;&|])\.\s+/`)
)

// readsBundledSkills: the command touches /skills and does something
// other than run a script there.
func readsBundledSkills(cmd string) bool {
	if !skillsRef.MatchString(cmd) {
		return false
	}
	// Quoted text is data (a skill's JSON or query argument may well say
	// "find" or "sort"); match commands on what's left.
	bare := quoted.ReplaceAllString(cmd, "''")
	return fileReaderCmd.MatchString(bare) || inlineCode.MatchString(bare)
}

// GuestRefusal returns the refusal for a regular chatter's tool call that
// would read the agent's bundled skill files or probe the service's own
// network identity, or "" when the call may run. Owner and channel-admin
// turns never come here.
func GuestRefusal(name string, raw json.RawMessage) string {
	var a struct {
		Path    string `json:"path"`
		Command string `json:"command"`
		URL     string `json:"url"`
	}
	_ = json.Unmarshal(raw, &a)
	switch name {
	case "read_file", "list_dir", "edit_file", "write_file", "apply_patch":
		if underBundledSkills(a.Path) {
			return refuseSkillFiles
		}
		if name == "apply_patch" && skillsRef.MatchString(string(raw)) {
			return refuseSkillFiles
		}
	case "exec":
		if mentionsIPProbe(a.Command) {
			return refuseInfraProbe
		}
		if readsBundledSkills(a.Command) {
			return refuseSkillFiles
		}
		if mentionsFileDrop(a.Command) {
			return refuseFileDrop
		}
	case "web_fetch":
		if u, err := url.Parse(strings.TrimSpace(a.URL)); err == nil && mentionsIPProbe(u.Host+u.Path) {
			return refuseInfraProbe
		}
	}
	return ""
}

// CallerIsAdmin reports whether this turn's chatter is the owner or a
// channel admin (see SetCallerIsAdmin).
func (r *Registry) CallerIsAdmin() bool { return r != nil && r.callerIsAdmin }
