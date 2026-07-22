// Command fakevendor is the portable test-only fake vendor CLI
// (FEAT-20260722-002 R0-CX-F8). It replaces the /bin/sh fixture scripts so
// the same fixture text drives every platform, including Windows where no
// POSIX shell exists. It executes exactly the narrow fixture dialect the
// suite uses — positional-arg conditionals, an argument scan loop, echo /
// printf / exit / touch / sleep / self-kill — and fails loudly (exit 97 with
// a diagnostic) on any construct outside that dialect. This is a test
// fixture engine, never a general shell.
//
// The fixture text is read from the file "<own executable path>.fixture".
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// lint switches the interpreter into validate-only traversal: both if
// branches and loop bodies run once, and commands are syntax-checked
// without side effects.
var lint bool

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fakevendor: unsupported fixture construct: "+format+"\n", args...)
	os.Exit(97)
}

func main() {
	// Builtin descendant mode (no fixture read): used by the `spawn` dialect
	// command so lifecycle fixtures can create a grandchild portably.
	if len(os.Args) >= 3 && os.Args[1] == "--fakevendor-sleep" {
		n, err := strconv.ParseFloat(os.Args[2], 64)
		if err != nil {
			fail("--fakevendor-sleep %q", os.Args[2])
		}
		time.Sleep(time.Duration(n * float64(time.Second)))
		os.Exit(0)
	}
	// Lint mode (R1-CX-F5): parse and validate EVERY line and branch of the
	// fixture — including branches execution would not take — without side
	// effects. The installer runs this before a fixture is used, so a dead
	// branch with unsupported syntax fails at install time, not silently at
	// runtime.
	if len(os.Args) >= 3 && os.Args[1] == "--fakevendor-lint" {
		raw, err := os.ReadFile(os.Args[2])
		if err != nil {
			fail("lint: cannot read fixture: %v", err)
		}
		lint = true
		run(strings.Split(string(raw), "\n"), nil)
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		fail("cannot resolve executable: %v", err)
	}
	raw, err := os.ReadFile(exe + ".fixture")
	if err != nil {
		fail("cannot read fixture: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	run(lines, os.Args[1:])
	os.Exit(0)
}

// run executes top-level lines; blocks (for/if) are consumed recursively.
func run(lines []string, args []string) {
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			i++
		case strings.HasPrefix(line, "for a in \"$@\"; do"):
			end := findBlockEnd(lines, i+1, "done")
			body := lines[i+1 : end]
			if lint {
				runBody(body, args, map[string]string{"a": ""})
			} else {
				for _, a := range args {
					runBody(body, args, map[string]string{"a": a})
				}
			}
			i = end + 1
		case strings.HasPrefix(line, "while true; do ") && strings.HasSuffix(line, "; done"):
			body := strings.TrimSuffix(strings.TrimPrefix(line, "while true; do "), "; done")
			for {
				for _, cmd := range splitCommands(body) {
					execSimple(cmd, args, nil)
				}
				if lint {
					break
				}
			}
			i++
		case strings.HasPrefix(line, "if [ "):
			i = execIf(lines, i, args, nil)
		default:
			execSimple(line, args, nil)
			i++
		}
	}
}

func runBody(lines []string, args []string, vars map[string]string) {
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "":
			i++
		case strings.HasPrefix(line, "if [ "):
			i = execIf(lines, i, args, vars)
		default:
			execSimple(line, args, vars)
			i++
		}
	}
}

// execIf handles `if [ COND ]; then ...` in single-line (`...; fi`) and
// block form. Returns the index of the next line to execute.
func execIf(lines []string, i int, args []string, vars map[string]string) int {
	line := strings.TrimSpace(lines[i])
	thenIdx := strings.Index(line, "; then")
	if thenIdx < 0 {
		fail("if without '; then': %q", line)
	}
	cond := evalCond(line[:thenIdx], args, vars)
	rest := strings.TrimSpace(line[thenIdx+len("; then"):])
	if rest != "" { // single-line: CMDS; fi
		if !strings.HasSuffix(rest, "fi") {
			fail("single-line if without trailing fi: %q", line)
		}
		body := strings.TrimSuffix(rest, "fi")
		body = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), ";"))
		if cond || lint {
			for _, cmd := range splitCommands(body) {
				execSimple(cmd, args, vars)
			}
		}
		return i + 1
	}
	end := findBlockEnd(lines, i+1, "fi")
	if cond || lint {
		runBody(lines[i+1:end], args, vars)
	}
	return end + 1
}

// evalCond evaluates `if [ "$X" = V ]` with optional ` ] || [ ` alternates.
func evalCond(cond string, args []string, vars map[string]string) bool {
	cond = strings.TrimPrefix(strings.TrimSpace(cond), "if ")
	for _, alt := range strings.Split(cond, "||") {
		alt = strings.TrimSpace(alt)
		alt = strings.TrimPrefix(alt, "[")
		alt = strings.TrimSuffix(alt, "]")
		parts := strings.SplitN(alt, "=", 2)
		if len(parts) != 2 {
			fail("condition %q", cond)
		}
		left := resolveVar(strings.TrimSpace(parts[0]), args, vars)
		right := unquote(strings.TrimSpace(parts[1]))
		if left == right {
			return true
		}
	}
	return false
}

func resolveVar(token string, args []string, vars map[string]string) string {
	token = strings.Trim(token, "\"")
	if !strings.HasPrefix(token, "$") {
		fail("condition left side %q", token)
	}
	name := token[1:]
	if vars != nil {
		if v, ok := vars[name]; ok {
			return v
		}
	}
	if n, err := strconv.Atoi(name); err == nil {
		if n >= 1 && n <= len(args) {
			return args[n-1]
		}
		return ""
	}
	fail("variable $%s", name)
	return ""
}

// splitCommands splits a single-line body on "; " outside quotes.
func splitCommands(body string) []string {
	var out []string
	var cur strings.Builder
	inS, inD := false, false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\'' && !inD:
			inS = !inS
		case c == '"' && !inS:
			inD = !inD
		case c == ';' && !inS && !inD:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func findBlockEnd(lines []string, from int, terminator string) int {
	var stack []string
	for i := from; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(l, "for ") && strings.HasSuffix(l, "; do"):
			stack = append(stack, "done")
		case strings.HasPrefix(l, "if [ ") && !strings.HasSuffix(l, "fi"):
			stack = append(stack, "fi")
		case l == "fi" || l == "done":
			if len(stack) == 0 {
				if l == terminator {
					return i
				}
				fail("unexpected block close %q (want %s)", l, terminator)
			}
			if stack[len(stack)-1] != l {
				fail("mismatched block close %q", l)
			}
			stack = stack[:len(stack)-1]
		}
	}
	fail("unterminated block (missing %s)", terminator)
	return -1
}

// unquote strips one level of single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

func execSimple(cmd string, args []string, vars map[string]string) {
	cmd = strings.TrimSpace(cmd)
	switch {
	case cmd == "" || cmd == "fi" || cmd == "done":
		return
	case strings.HasPrefix(cmd, "exit"):
		code := 0
		if rest := strings.TrimSpace(strings.TrimPrefix(cmd, "exit")); rest != "" {
			n, err := strconv.Atoi(rest)
			if err != nil {
				fail("exit %q", rest)
			}
			code = n
		}
		if lint {
			return
		}
		os.Exit(code)
	case strings.HasPrefix(cmd, "sleep "):
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(cmd, "sleep ")), 64)
		if err != nil {
			fail("sleep %q", cmd)
		}
		if !lint {
			time.Sleep(time.Duration(n * float64(time.Second)))
		}
	case strings.HasPrefix(cmd, "touch "):
		path := unquote(strings.TrimSpace(strings.TrimPrefix(cmd, "touch ")))
		if path == "" {
			fail("touch without path")
		}
		if lint {
			return
		}
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.Close()
		} else {
			fail("touch: %v", err)
		}
	case strings.HasPrefix(cmd, "spawn "):
		// Test-only descendant: start our own executable in sleep mode and
		// record its pid at <exe>.grandchild — lifecycle fixtures assert the
		// whole tree dies with the confinement boundary.
		if secs := strings.TrimSpace(strings.TrimPrefix(cmd, "spawn ")); lint {
			if _, err := strconv.ParseFloat(secs, 64); err != nil {
				fail("spawn %q", secs)
			}
		} else {
			execSpawn(secs)
		}
	case cmd == "kill -KILL $$":
		if lint {
			return // dialect-valid; POSIX-only at runtime
		}
		selfKill() // POSIX self-SIGKILL; fails loudly on Windows
	case strings.HasPrefix(cmd, "printf "):
		execPrintf(cmd)
	case strings.HasPrefix(cmd, "echo"):
		execEcho(cmd, args)
	default:
		fail("command %q", cmd)
	}
}

func execSpawn(secs string) {
	if _, err := strconv.ParseFloat(secs, 64); err != nil {
		fail("spawn %q", secs)
	}
	exe, err := os.Executable()
	if err != nil {
		fail("spawn: %v", err)
	}
	child := exec.Command(exe, "--fakevendor-sleep", secs)
	if err := child.Start(); err != nil {
		fail("spawn start: %v", err)
	}
	if err := os.WriteFile(exe+".grandchild", []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
		fail("spawn pid record: %v", err)
	}
	// Deliberately not waited: the grandchild must be reaped by the
	// confinement boundary, not by this process.
}

func execPrintf(cmd string) {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "printf "))
	var format string
	switch {
	case strings.HasPrefix(rest, "'%s\\n' "):
		format, rest = "%s\n", strings.TrimPrefix(rest, "'%s\\n' ")
	case strings.HasPrefix(rest, "'%s' "):
		format, rest = "%s", strings.TrimPrefix(rest, "'%s' ")
	default:
		fail("printf %q", cmd)
	}
	if lint {
		return
	}
	fmt.Fprintf(os.Stdout, format, unquote(strings.TrimSpace(rest)))
}

func execEcho(cmd string, args []string) {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "echo"))
	out := os.Stdout
	if strings.HasSuffix(rest, ">&2") {
		out = os.Stderr
		rest = strings.TrimSpace(strings.TrimSuffix(rest, ">&2"))
	}
	if idx := strings.Index(rest, ">>"); idx >= 0 {
		target := strings.TrimSpace(rest[idx+2:])
		payload := strings.TrimSpace(rest[:idx])
		if target == "" {
			fail("echo append without target")
		}
		if lint {
			return // syntax only: env resolution happens at runtime
		}
		path := unquote(target)
		if strings.HasPrefix(path, "$") {
			path = os.Getenv(strings.TrimPrefix(path, "$"))
		}
		if path == "" {
			fail("echo append target %q resolved empty", target)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fail("echo append: %v", err)
		}
		defer f.Close()
		fmt.Fprintln(f, echoPayload(payload, args))
		return
	}
	if lint {
		return
	}
	fmt.Fprintln(out, echoPayload(rest, args))
}

func echoPayload(payload string, args []string) string {
	if payload == "\"$@\"" {
		return strings.Join(args, " ")
	}
	return unquote(payload)
}
