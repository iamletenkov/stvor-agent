// Command fakeubus stands in for /bin/ubus in the OpenWrt end-to-end test
// (make e2e-openwrt): procd is not PID 1 in the container, so the calls the
// procd init scripts make through /lib/functions/procd.sh — ubus call service
// set|add|delete|list|signal — are served here, one process per call, with
// the state in files.
//
//   - set/add starts each instance of the message the way procd would:
//     command, env, user and group, stdin /dev/null, stdout and stderr to
//     <state>/<service>.log (logd of the test), its own session. A running
//     instance of the same name is stopped first. The message of the instance
//     is kept in <state>/<service>/<instance>.json with the pid, so the
//     scenario can check user, no_new_privs and term_timeout as procd got them.
//   - delete sends SIGTERM to the instances (all, or the named one), waits up to
//     their term_timeout and then sends SIGKILL.
//   - list answers {"<service>":{"instances":{"<name>":{"running":true,"pid":N}}}}
//     for the live instances, {} for an unknown service (procd_running reads it).
//   - signal sends the signal of the message.
//
// Not emulated: respawn, no_new_privs (recorded, not applied), triggers. Run
// the container with an init (docker run --init) that reaps the instances.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// stateDir holds the instances; FAKEUBUS_STATE overrides it.
func stateDir() string {
	if d := os.Getenv("FAKEUBUS_STATE"); d != "" {
		return d
	}
	return "/tmp/fakeubus"
}

// instance is the part of a procd instance message the fake acts on.
type instance struct {
	Command     []string          `json:"command"`
	Env         map[string]string `json:"env,omitempty"`
	User        string            `json:"user,omitempty"`
	Group       string            `json:"group,omitempty"`
	TermTimeout int               `json:"term_timeout,omitempty"`
	NoNewPrivs  bool              `json:"no_new_privs,omitempty"`
	Respawn     []string          `json:"respawn,omitempty"`
	Stdout      bool              `json:"stdout,omitempty"`
	Stderr      bool              `json:"stderr,omitempty"`
	// PID is set by the fake.
	PID int `json:"pid"`
}

type message struct {
	Name      string              `json:"name"`
	Instance  string              `json:"instance"`
	Instances map[string]instance `json:"instances"`
	Signal    int                 `json:"signal"`
}

func main() {
	closeInherited()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fakeubus:", err)
		var code exitCode
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		os.Exit(1)
	}
}

// exitCode is an exit status of ubus: 4 — UBUS_STATUS_NOT_FOUND.
type exitCode int

func (c exitCode) Error() string { return "код " + strconv.Itoa(int(c)) }

func run(args []string) error {
	i := 0
	for i < len(args) && args[i] != "call" {
		i++
	}
	if i+2 >= len(args) {
		return nil // not a call: wait_for, listen — nothing to do
	}
	object, method := args[i+1], args[i+2]
	var msg message
	if i+3 < len(args) && strings.TrimSpace(args[i+3]) != "" {
		if err := json.Unmarshal([]byte(args[i+3]), &msg); err != nil {
			return fmt.Errorf("сообщение %s %s: %w", object, method, err)
		}
	}
	if object != "service" {
		return nil
	}
	switch method {
	case "set", "add":
		return set(msg)
	case "delete":
		return del(msg)
	case "list":
		return list(msg)
	case "signal":
		return signal(msg)
	}
	return nil
}

// closeInherited marks every descriptor above stderr close-on-exec: rc.common
// holds its service lock on fd 1000 (flock) while it calls ubus, and an
// instance must not inherit it — procd starts instances itself, with nothing
// of the script.
func closeInherited() {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return
	}
	for _, e := range entries {
		if fd, err := strconv.Atoi(e.Name()); err == nil && fd > 2 {
			syscall.CloseOnExec(fd)
		}
	}
}

func serviceDir(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/") || name == "." || name == ".." {
		return "", fmt.Errorf("имя службы %q", name)
	}
	return filepath.Join(stateDir(), name), nil
}

func set(msg message) error {
	dir, err := serviceDir(msg.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, in := range msg.Instances {
		if err := stopInstance(dir, name); err != nil {
			return err
		}
		if err := start(dir, msg.Name, name, in); err != nil {
			return fmt.Errorf("%s.%s: %w", msg.Name, name, err)
		}
	}
	return nil
}

func start(dir, service, name string, in instance) error {
	if len(in.Command) == 0 {
		return errors.New("нет command")
	}
	cmd := exec.Command(in.Command[0], in.Command[1:]...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	for k, v := range in.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = null.Close() }()
	logf, err := os.OpenFile(filepath.Join(stateDir(), service+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logf.Close() }()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, logf, logf
	attr := &syscall.SysProcAttr{Setsid: true}
	if in.User != "" {
		cred, err := credential(in.User, in.Group)
		if err != nil {
			return err
		}
		attr.Credential = cred
	}
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		return err
	}
	in.PID = cmd.Process.Pid
	_ = cmd.Process.Release()
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600)
}

// credential is uid and gid of user (group, when set, overrides the primary
// one), without supplementary groups — as procd sets them.
func credential(userName, groupName string) (*syscall.Credential, error) {
	u, err := user.Lookup(userName)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, err
	}
	gidStr := u.Gid
	if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return nil, err
		}
		gidStr = g.Gid
	}
	gid, err := strconv.ParseUint(gidStr, 10, 32)
	if err != nil {
		return nil, err
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}, nil
}

func readInstance(path string) (instance, error) {
	var in instance
	data, err := os.ReadFile(path)
	if err != nil {
		return in, err
	}
	err = json.Unmarshal(data, &in)
	return in, err
}

// alive: the process exists and is not a zombie.
func alive(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(stat)
	if j := strings.LastIndexByte(s, ')'); j >= 0 && j+2 < len(s) {
		return s[j+2] != 'Z'
	}
	return true
}

func stopInstance(dir, name string) error {
	path := filepath.Join(dir, name+".json")
	in, err := readInstance(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if alive(in.PID) {
		_ = syscall.Kill(in.PID, syscall.SIGTERM)
		wait := time.Duration(in.TermTimeout) * time.Second
		if wait <= 0 {
			wait = 5 * time.Second
		}
		deadline := time.Now().Add(wait)
		for alive(in.PID) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if alive(in.PID) {
			_ = syscall.Kill(in.PID, syscall.SIGKILL)
		}
	}
	return os.Remove(path)
}

func instanceNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			names = append(names, n)
		}
	}
	return names, nil
}

func del(msg message) error {
	dir, err := serviceDir(msg.Name)
	if err != nil {
		return err
	}
	names, err := instanceNames(dir)
	if errors.Is(err, os.ErrNotExist) {
		return exitCode(4)
	}
	if err != nil {
		return err
	}
	for _, n := range names {
		if msg.Instance != "" && n != msg.Instance {
			continue
		}
		if err := stopInstance(dir, n); err != nil {
			return err
		}
	}
	return nil
}

func list(msg message) error {
	out := map[string]any{}
	services := []string{msg.Name}
	if msg.Name == "" {
		entries, _ := os.ReadDir(stateDir())
		services = nil
		for _, e := range entries {
			if e.IsDir() {
				services = append(services, e.Name())
			}
		}
	}
	for _, s := range services {
		dir, err := serviceDir(s)
		if err != nil {
			return err
		}
		names, err := instanceNames(dir)
		if err != nil {
			continue
		}
		insts := map[string]any{}
		for _, n := range names {
			in, err := readInstance(filepath.Join(dir, n+".json"))
			if err != nil || !alive(in.PID) {
				continue
			}
			insts[n] = map[string]any{"running": true, "pid": in.PID, "command": in.Command}
		}
		out[s] = map[string]any{"instances": insts}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func signal(msg message) error {
	dir, err := serviceDir(msg.Name)
	if err != nil {
		return err
	}
	names, err := instanceNames(dir)
	if err != nil {
		return exitCode(4)
	}
	sig := syscall.Signal(msg.Signal)
	if msg.Signal == 0 {
		sig = syscall.SIGHUP
	}
	for _, n := range names {
		if msg.Instance != "" && n != msg.Instance {
			continue
		}
		in, err := readInstance(filepath.Join(dir, n+".json"))
		if err == nil && alive(in.PID) {
			_ = syscall.Kill(in.PID, sig)
		}
	}
	return nil
}
