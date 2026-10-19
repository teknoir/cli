package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/api"
)

// execPlatform resolves vision-station-01 (a robot whose device has another name) and amr-02 (a device).
func execPlatform(t *testing.T) *fakePlatform {
	p := newFakePlatform(t)
	v := standardView()
	v.Target.Device = "vision-station-01"
	p.on("GET", "/config/vision-station-01", 200, jsonOf(t, v))
	v2 := standardView()
	v2.Target = api.ConfigTargetRef{Kind: "device", Name: "amr-02", Device: "amr-02"}
	p.on("GET", "/config/amr-02", 200, jsonOf(t, v2))
	v3 := standardView()
	v3.Target = api.ConfigTargetRef{Kind: "robot", Name: "line-3", Device: "line-3-main", Robot: "line-3"}
	p.on("GET", "/config/line-3", 200, jsonOf(t, v3))
	return p
}

func TestExec(t *testing.T) {
	p := execPlatform(t)
	ex := &fakeExec{}
	e, _ := testEnv(t, p, ex)

	if _, _, err := runCLI(t, e, "exec", "vision-station-01", "--", "uname", "-a"); err != nil {
		t.Fatal(err)
	}
	if got := ex.callList(); len(got) != 2 || got[0] != "OPEN vision-station-01" || got[1] != "uname -a" || !ex.closed {
		t.Errorf("calls %v closed=%v", got, ex.closed)
	}

	// one word is a shell command line, several words are quoted
	ex.calls = nil
	runCLI(t, e, "exec", "vision-station-01", "--", "df -h / | tail -1")
	runCLI(t, e, "exec", "vision-station-01", "--", "echo", "it's here", "$HOME")
	runCLI(t, e, "exec", "vision-station-01", "--", "sudo", "k3s", "kubectl", "get", "pods", "-A")
	got := ex.callList()
	want := []string{"OPEN vision-station-01", "df -h / | tail -1", "OPEN vision-station-01", `echo 'it'\''s here' '$HOME'`, "OPEN vision-station-01", "sudo k3s kubectl get pods -A"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// the device from --device
	ex.calls = nil
	if _, _, err := runCLI(t, e, "--device", "amr-02", "exec", "--", "hostname"); err != nil || ex.callList()[0] != "OPEN amr-02" {
		t.Errorf("%v %v", err, ex.callList())
	}
}

func TestExecPassesExitCodeAndOutput(t *testing.T) {
	p := execPlatform(t)
	ex := &fakeExec{
		run:    func(string) (execResult, error) { return execResult{Stdout: "hello\n", Stderr: "warn\n"}, nil },
		stream: func(string) int { return 3 },
	}
	e, _ := testEnv(t, p, ex)
	out, errs, err := runCLI(t, e, "exec", "vision-station-01", "--", "false")
	if out != "hello\n" || errs != "warn\n" {
		t.Errorf("output %q %q", out, errs)
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 3 || exitCodeOf(err) != 3 {
		t.Errorf("the command's exit status is ours: %v", err)
	}
	if got := ex.callList(); len(got) != 2 {
		t.Errorf("one shell per command: %v", got)
	}
}

func TestExecErrors(t *testing.T) {
	p := execPlatform(t)
	ex := &fakeExec{}
	e, _ := testEnv(t, p, ex)
	cases := map[string][]string{
		"no --":        {"exec", "vision-station-01", "uname"},
		"no command":   {"exec", "vision-station-01", "--"},
		"no device":    {"exec", "--", "uname"},
		"two devices":  {"exec", "a", "b", "--", "uname"},
		"unknown flag": {"exec", "vision-station-01", "--bogus", "--", "uname"},
	}
	for name, args := range cases {
		if _, _, err := runCLI(t, e, args...); exitCodeOf(err) != codeUsage {
			t.Errorf("%s: exit %d: %v", name, exitCodeOf(err), err)
		}
	}
	if len(ex.callList()) != 0 {
		t.Errorf("nothing may run on usage errors: %v", ex.callList())
	}

	// an unknown name is exit 3, before anything is opened
	n := len(ex.callList())
	if _, _, err := runCLI(t, e, "exec", "ghost", "--", "uname"); exitCodeOf(err) != codeNotFound || len(ex.callList()) != n {
		t.Errorf("unknown: exit %d: %v", exitCodeOf(err), err)
	}
	e.newExec = func(_ context.Context, _, _, _ string) (deviceExec, error) {
		return nil, errors.New("amr-02 has no active WireGuard tunnel")
	}
	_, _, err := runCLI(t, e, "exec", "amr-02", "--", "uname")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "no active WireGuard tunnel") {
		t.Errorf("%d: %v", exitCodeOf(err), err)
	}
	// a rejected login is exit 6
	e.newExec = func(_ context.Context, _, _, _ string) (deviceExec, error) {
		return nil, &api.Error{Status: 302, Kind: api.KindAuth, Message: "not logged in or token rejected: run tnctl login"}
	}
	if _, _, err := runCLI(t, e, "exec", "amr-02", "--", "uname"); exitCodeOf(err) != codeAuth {
		t.Errorf("exit %d", exitCodeOf(err))
	}
}

// logsPlatform serves a view with a notebook HelmChart, a whoami Deployment and a CronJob.
func logsPlatform(t *testing.T) *fakePlatform {
	v := standardView()
	v.Configuration.Effective = append(v.Configuration.Effective, api.FleetConfigResource{Kind: "CronJob", Name: "nightly", Namespace: "default", Layer: "robot", Source: "x"})
	return platformWith(t, v)
}

func logsExec(workloads string, podList string) *fakeExec {
	return &fakeExec{run: func(script string) (execResult, error) {
		switch {
		case strings.Contains(script, "helmcharts.helm.cattle.io"):
			return execResult{Stdout: probeOutput(map[string]string{"chart": chartJSON("a: 1", ""), "job": "", "workloads": workloads, "pods": listJSON()})}, nil
		case strings.Contains(script, "get pods -A"):
			return execResult{Stdout: podList}, nil
		case strings.Contains(script, " logs "):
			return execResult{Stdout: "line1\nline2\n"}, nil
		}
		return execResult{}, errors.New("unexpected: " + script)
	}}
}

func lastCall(ex *fakeExec) string {
	c := ex.callList()
	return c[len(c)-1]
}

func TestLogsOfWorkloads(t *testing.T) {
	p := logsPlatform(t)
	ex := logsExec("", "")
	e, _ := testEnv(t, p, ex)

	out, _, err := runCLI(t, e, "logs", "vision-station-01", "Deployment/default/whoami")
	if err != nil || out != "line1\nline2\n" {
		t.Fatalf("%v %q", err, out)
	}
	if got := lastCall(ex); got != "sudo -n k3s kubectl -n default logs deployment/whoami --tail=200" {
		t.Errorf("got %s", got)
	}
	if _, _, err := runCLI(t, e, "logs", "vision-station-01", "whoami", "-c", "whoami", "-f", "--tail", "50", "--since", "10m", "--previous", "--all-containers"); err != nil {
		t.Fatal(err)
	}
	if got := lastCall(ex); got != "sudo -n k3s kubectl -n default logs deployment/whoami -c whoami --all-containers -f --previous --since=10m --tail=50" {
		t.Errorf("flags: %s", got)
	}
	for _, args := range [][]string{
		{"logs", "vision-station-01", "whoami", "--since", "yesterday"},
		{"logs", "vision-station-01", "whoami", "-c", "a;b"},
		{"logs", "vision-station-01", "CronJob/nightly"},
		{"logs", "vision-station-01"},
	} {
		if _, _, err := runCLI(t, e, args...); exitCodeOf(err) != codeUsage {
			t.Errorf("%v: exit %d %v", args, exitCodeOf(err), err)
		}
	}
	if _, _, err := runCLI(t, e, "logs", "vision-station-01", "Deployment/default/nope"); exitCodeOf(err) != codeNotFound {
		t.Errorf("unknown 3-part resource: %v", err)
	}
}

func TestLogsOfHelmChart(t *testing.T) {
	p := logsPlatform(t)
	one := listJSON(deployJSON("notebook", 1, 1, 1, 1, 1, "i"))
	ex := logsExec(one, "")
	e, _ := testEnv(t, p, ex)
	if _, _, err := runCLI(t, e, "logs", "vision-station-01", "HelmChart/default/notebook"); err != nil {
		t.Fatal(err)
	}
	if got := lastCall(ex); !strings.Contains(got, "-n default logs deployment/notebook") {
		t.Errorf("got %s", got)
	}

	other := deployJSON("worker", 1, 1, 1, 1, 1, "i")
	ex = logsExec(listJSON(deployJSON("notebook", 1, 1, 1, 1, 1, "i"), other), "")
	e, _ = testEnv(t, p, ex)
	_, _, err := runCLI(t, e, "logs", "vision-station-01", "HelmChart/default/notebook")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "Deployment/default/notebook") || !strings.Contains(err.Error(), "Deployment/default/worker") {
		t.Errorf("several workloads: %d %v", exitCodeOf(err), err)
	}
	ex = logsExec(listJSON(), "")
	e, _ = testEnv(t, p, ex)
	if _, _, err := runCLI(t, e, "logs", "vision-station-01", "HelmChart/default/notebook"); exitCodeOf(err) != codeNotFound {
		t.Errorf("no workloads: %v", err)
	}
}

func TestLogsOfPods(t *testing.T) {
	p := logsPlatform(t)
	pods := "default   notebook-77f875b6c-45s8v\nkube-system   coredns-abc\nkube-system   coredns-abd\n"
	ex := logsExec("", pods)
	e, _ := testEnv(t, p, ex)

	if _, _, err := runCLI(t, e, "logs", "vision-station-01", "notebook-77f875b6c-45s8v"); err != nil {
		t.Fatal(err)
	}
	if got := lastCall(ex); !strings.HasPrefix(got, "sudo -n k3s kubectl -n default logs pod/notebook-77f875b6c-45s8v") {
		t.Errorf("got %s", got)
	}
	// a unique prefix is enough
	if _, _, err := runCLI(t, e, "logs", "vision-station-01", "notebook-77"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, e, "logs", "vision-station-01", "coredns")
	if exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "kube-system/coredns-abc") || !strings.Contains(err.Error(), "kube-system/coredns-abd") {
		t.Errorf("ambiguous prefix: %d %v", exitCodeOf(err), err)
	}
	if _, _, err := runCLI(t, e, "logs", "vision-station-01", "ghost"); exitCodeOf(err) != codeNotFound {
		t.Errorf("unknown pod: %v", err)
	}
}

func TestLogsPassesKubectlExitCode(t *testing.T) {
	p := logsPlatform(t)
	ex := logsExec("", "")
	ex.stream = func(string) int { return 1 }
	e, _ := testEnv(t, p, ex)
	_, _, err := runCLI(t, e, "logs", "vision-station-01", "Deployment/default/whoami")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Errorf("%v", err)
	}
}

// A robot name works: the platform says which device runs the command.
func TestExecResolvesRobotToDevice(t *testing.T) {
	p := execPlatform(t)
	ex := &fakeExec{}
	e, _ := testEnv(t, p, ex)
	if _, _, err := runCLI(t, e, "exec", "line-3", "--", "hostname"); err != nil || ex.callList()[0] != "OPEN line-3-main" {
		t.Errorf("%v %v", err, ex.callList())
	}
}

// ssh's own exit status 255 is a failure to reach the device (exit 1), not the command's status.
func TestExecAndLogsSSH255IsAnError(t *testing.T) {
	p := execPlatform(t)
	ex := &fakeExec{stream: func(string) int { return 255 }}
	e, _ := testEnv(t, p, ex)
	_, _, err := runCLI(t, e, "exec", "vision-station-01", "--", "uname")
	if exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "could not reach vision-station-01") {
		t.Errorf("exec: %d %v", exitCodeOf(err), err)
	}
}
