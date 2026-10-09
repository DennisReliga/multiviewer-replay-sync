// MultiViewer Replay Sync: watch F1 replays in MultiViewer in sync with friends.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const app = "multiviewer-replay-sync"

var version = "dev" // set at build time: -ldflags "-X main.version=..."

func usage() string {
	cmd := func(args, text string) string { return fmt.Sprintf("  %-48s%s", app+" "+args, text) }
	return "MultiViewer Replay Sync - watch F1 replays in sync with friends\n\n" +
		cmd("", "no arguments: asks what to do") + "\n" +
		cmd(fmt.Sprintf("host [--port %d]", defaultPort), "open a room and share your IP") + "\n" +
		cmd("join <ip>[:port]", "join someone's room (IPv6: [addr]:port)") + `

Options:
  --name <name>        name shown to others (default: your user name)
  --password <text>    host: require this password; join: the room's password
  --player <id>        which MultiViewer player to follow (default: the main feed)
  -v, --version        show the version
  -h, --help           show this help

Everyone in the room can pause, resume and seek; it is mirrored to all.`
}

func logf(format string, args ...any) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

var stdin = bufio.NewReader(os.Stdin)

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// fatal prints the error, waits for Enter when started by double-click (so the window doesn't vanish), and exits.
func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "\nError: %s\n", msg)
	if isTerminal() {
		fmt.Print("Press Enter to exit...")
		stdin.ReadString('\n')
	}
	os.Exit(1)
}

func isAddrInUse(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == syscall.EADDRINUSE || errno == 10048) // 10048 = WSAEADDRINUSE
}

func defaultName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		if i := strings.LastIndex(name, `\`); i >= 0 { // Windows: DOMAIN\user
			name = name[i+1:]
		}
		return name
	}
	for _, v := range []string{"USERNAME", "USER"} {
		if s := os.Getenv(v); s != "" {
			return s
		}
	}
	return "someone"
}

// ---------------------------------------------------------------- saved answers

type config struct {
	Mode   string `json:"mode,omitempty"`
	Name   string `json:"name,omitempty"`
	Target string `json:"target,omitempty"`
}

func configFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, app, "config.json")
}

func loadConfig() config {
	var c config
	if b, err := os.ReadFile(configFile()); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func saveConfig(c config) {
	f := configFile()
	os.MkdirAll(filepath.Dir(f), 0o755)
	b, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(f, b, 0o644)
}

// ask shows a question with an optional default in brackets; Enter keeps the default.
func ask(question, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", question, def)
	} else {
		fmt.Printf("%s: ", question)
	}
	line, err := stdin.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	if err == io.EOF && line == "" {
		return "", err
	}
	return line, nil
}

func prompt() (mode, name, target, password string) {
	cfg := loadConfig()
	fmt.Print("MultiViewer Replay Sync\n\n")
	def := cfg.Mode
	if def == "" {
		def = "join"
	}
	answer, _ := ask("Host a room or join one? (host/join)", def)
	mode = "join"
	if strings.HasPrefix(strings.ToLower(answer), "h") {
		mode = "host"
	}
	if cfg.Name == "" {
		cfg.Name = defaultName()
	}
	name, _ = ask("Your name", cfg.Name)
	if mode == "join" {
		for target == "" {
			var err error
			if target, err = ask("Host's IP address (e.g. 10.147.17.5)", cfg.Target); err != nil {
				fatal("No host address given.")
			}
		}
	}
	if mode == "host" {
		password, _ = ask("Room password (optional, Enter for none)", "")
	} else {
		password, _ = ask("Room password (Enter if none)", "")
	}
	if target == "" {
		target = cfg.Target
	}
	saveConfig(config{Mode: mode, Name: name, Target: target}) // the password is deliberately not saved
	return
}

// ---------------------------------------------------------------- command line

type options struct {
	port, name, password, player string
	help, version                bool
	set                          map[string]bool
}

func parseArgs(args []string) ([]string, options, error) {
	o := options{set: map[string]bool{}}
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			o.help = true
			continue
		case "-v", "--version":
			o.version = true
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		key, val, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !strings.HasPrefix(a, "--") || !map[string]bool{"port": true, "name": true, "password": true, "player": true}[key] {
			return nil, o, fmt.Errorf("Unknown option '%s'", a)
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, o, fmt.Errorf("Option '--%s <value>' argument missing", key)
			}
			i++
			val = args[i]
		}
		o.set[key] = true
		switch key {
		case "port":
			o.port = val
		case "name":
			o.name = val
		case "password":
			o.password = val
		case "player":
			o.player = val
		}
	}
	return positionals, o, nil
}

func parsePort(v string) (int, error) {
	port, err := strconv.Atoi(v)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("Invalid port: %s", v)
	}
	return port, nil
}

func printAddresses(port int) {
	type addr struct {
		iface, ip string
		zt        bool
	}
	var found []addr
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			found = append(found, addr{ifc.Name, ipnet.IP.String(), regexp.MustCompile(`(?i)zerotier`).MatchString(ifc.Name)})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].zt && !found[j].zt })
	fmt.Println("\nTell your friends to join with:")
	if len(found) == 0 {
		fmt.Println("  (no network address found - are you connected?)")
	}
	for _, a := range found {
		ip := a.ip
		if port != defaultPort {
			ip += ":" + strconv.Itoa(port)
		}
		marker := ""
		if a.zt {
			marker = "<- ZeroTier "
		}
		fmt.Printf("  %s   %s(%s)\n", ip, marker, a.iface)
	}
	fmt.Printf("If they can't connect, allow TCP port %d through your firewall (see README).\n\n", port)
}

func main() {
	positionals, o, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n\n%s\n", err, usage())
		os.Exit(1)
	}
	if o.version {
		fmt.Println(version)
		return
	}
	command, target := "", ""
	if len(positionals) > 0 {
		command = positionals[0]
	}
	if len(positionals) > 1 {
		target = positionals[1]
	}
	if o.help || (command != "" && command != "host" && command != "join") {
		fmt.Println(usage())
		return
	}

	var mode, name, password string
	if command == "" {
		mode, name, target, password = prompt()
	} else {
		mode, name, password = command, o.name, o.password
		if !o.set["name"] {
			name = defaultName()
		}
	}
	name = cleanName(name)

	if mode == "host" {
		portText := strconv.Itoa(defaultPort)
		if o.set["port"] {
			portText = o.port
		}
		port, err := parsePort(portText)
		if err != nil {
			fatal(err.Error())
		}
		hostToken, err := startServer(port, password)
		if err != nil {
			fatal(err.Error())
		}
		printAddresses(port)
		runClient(fmt.Sprintf("ws://127.0.0.1:%d", port), name, password, hostToken, o.player)
		return
	}
	if target == "" {
		fmt.Println(usage())
		os.Exit(1)
	}
	runClient(roomURL(target), name, password, "", o.player)
}
