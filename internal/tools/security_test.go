package tools

import "testing"

func TestCheckCommand(t *testing.T) {
	p := DefaultSecurityPolicy()
	blocked := []string{
		"rm -rf /",
		"sudo reboot",
		"echo $(whoami)",
		"cat /etc/passwd | dd if=/dev/zero",
		"foo `id`",
	}
	for _, c := range blocked {
		if err := p.CheckCommand(c); err == nil {
			t.Errorf("命令应被拦截但通过了: %q", c)
		}
	}
	allowed := []string{"ls -la", "cat go.mod", "grep foo bar.txt"}
	for _, c := range allowed {
		if err := p.CheckCommand(c); err != nil {
			t.Errorf("命令应被允许但被拦截: %q (%v)", c, err)
		}
	}
}

func TestAllowedCommandsWhitelist(t *testing.T) {
	p := SecurityPolicy{AllowedCommands: []string{"ls", "cat"}}
	if err := p.CheckCommand("ls -la"); err != nil {
		t.Errorf("ls 应被允许: %v", err)
	}
	if err := p.CheckCommand("rm foo"); err == nil {
		t.Error("rm 不在白名单应被拦截")
	}
}

func TestCheckPath(t *testing.T) {
	p := SecurityPolicy{
		IncludePaths: []string{"/tmp"},
		ExcludePaths: []string{"/tmp/secret"},
	}
	if err := p.CheckPath("/tmp/a.txt"); err != nil {
		t.Errorf("/tmp 下应允许: %v", err)
	}
	if err := p.CheckPath("/etc/passwd"); err == nil {
		t.Error("/etc 不在 include 应被拦截")
	}
	if err := p.CheckPath("/tmp/secret/x"); err == nil {
		t.Error("exclude 应优先拦截")
	}
}
