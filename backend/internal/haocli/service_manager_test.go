package haocli

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

type serviceCommandCall struct {
	privileged bool
	executable string
	argv       []string
}

type fakeServiceCommands struct {
	privilegeErr error
	fail         map[string]error
	calls        []serviceCommandCall
}

func (f *fakeServiceCommands) CheckPrivilege(context.Context, bool, io.Reader) error {
	return f.privilegeErr
}

func (f *fakeServiceCommands) Run(_ context.Context, privileged, _ bool, _ io.Reader, executable string, argv ...string) error {
	call := serviceCommandCall{privileged: privileged, executable: executable, argv: append([]string(nil), argv...)}
	f.calls = append(f.calls, call)
	return f.fail[strings.Join(argv, " ")]
}

func testServiceManager(obs *fakeObserver, commands *fakeServiceCommands) *haoServiceManager {
	return &haoServiceManager{observer: obs, commands: commands, timeout: 25 * time.Millisecond, targetUser: UserObservation{Name: "ubuntu", UID: 1000, Home: "/home/ubuntu"}, input: strings.NewReader(""), systemctl: "/usr/bin/systemctl", journalctl: "/usr/bin/journalctl"}
}

func serviceStatusKey(component string) string {
	return "/usr/bin/systemctl show --no-pager --property=LoadState --property=UnitFileState --property=ActiveState --property=SubState " + canonicalServiceUnits[component]
}

func TestServiceActivationOrdersDependenciesAndSkipsHealthyState(t *testing.T) {
	obs := healthyObserver()
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running"
	obs.runs[serviceStatusKey("gateway")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	commands := &fakeServiceCommands{fail: map[string]error{}}

	result, err := testServiceManager(obs, commands).Activate(context.Background(), []string{"gateway", "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ServiceChange{{Component: "gateway", Operation: "enable"}, {Component: "gateway", Operation: "start"}}
	if !reflect.DeepEqual(result.Completed, want) || len(result.Rollback) != 0 {
		t.Fatalf("result=%+v want completed=%+v", result, want)
	}
	if len(commands.calls) != 2 || strings.Join(commands.calls[0].argv, " ") != "enable "+haoGatewayUnit || strings.Join(commands.calls[1].argv, " ") != "start "+haoGatewayUnit {
		t.Fatalf("calls=%+v", commands.calls)
	}
	for _, call := range commands.calls {
		if !call.privileged || call.executable != "/usr/bin/systemctl" {
			t.Fatalf("unsafe service mutation=%+v", call)
		}
	}
}

func TestHealthyActivationIsNoOpWithoutPrivilege(t *testing.T) {
	obs := healthyObserver()
	for _, component := range []string{"daemon", "gateway"} {
		obs.runs[serviceStatusKey(component)] = "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running"
	}
	commands := &fakeServiceCommands{privilegeErr: errPrivilegeRefused, fail: map[string]error{}}
	result, err := testServiceManager(obs, commands).Activate(context.Background(), []string{"gateway", "daemon"})
	if err != nil || len(result.Completed) != 0 || len(commands.calls) != 0 {
		t.Fatalf("result=%+v err=%v calls=%+v", result, err, commands.calls)
	}
}

func TestServiceActivationStartsDaemonBeforeGateway(t *testing.T) {
	obs := healthyObserver()
	for _, component := range []string{"daemon", "gateway"} {
		obs.runs[serviceStatusKey(component)] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	}
	commands := &fakeServiceCommands{fail: map[string]error{}}
	_, err := testServiceManager(obs, commands).Activate(context.Background(), []string{"gateway", "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"enable " + haoDaemonUnit, "start " + haoDaemonUnit, "enable " + haoGatewayUnit, "start " + haoGatewayUnit}
	if got := commandArgv(commands.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%v want=%v", got, want)
	}
}

func TestServiceStopOrdersGatewayBeforeDaemon(t *testing.T) {
	commands := &fakeServiceCommands{fail: map[string]error{}}
	result, err := testServiceManager(healthyObserver(), commands).Stop(context.Background(), []string{"daemon", "gateway"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"stop " + haoGatewayUnit, "stop " + haoDaemonUnit}
	if got := commandArgv(commands.calls); !reflect.DeepEqual(got, want) || len(result.Completed) != 2 {
		t.Fatalf("calls=%v result=%+v", got, result)
	}
}

func TestServiceStartOrdersDaemonBeforeGateway(t *testing.T) {
	commands := &fakeServiceCommands{fail: map[string]error{}}
	_, err := testServiceManager(healthyObserver(), commands).Start(context.Background(), []string{"gateway", "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start " + haoDaemonUnit, "start " + haoGatewayUnit}
	if got := commandArgv(commands.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%v want=%v", got, want)
	}
}

func TestPartialActivationRollsBackOnlyInvocationChanges(t *testing.T) {
	obs := healthyObserver()
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running"
	obs.runs[serviceStatusKey("gateway")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	commands := &fakeServiceCommands{fail: map[string]error{"start " + haoGatewayUnit: errors.New("boom")}}

	result, err := testServiceManager(obs, commands).Activate(context.Background(), []string{"daemon", "gateway"})
	if err == nil {
		t.Fatal("activation unexpectedly succeeded")
	}
	wantCalls := []string{"enable " + haoGatewayUnit, "start " + haoGatewayUnit, "disable " + haoGatewayUnit}
	if got := commandArgv(commands.calls); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls=%v want=%v", got, wantCalls)
	}
	wantRollback := []ServiceChange{{Component: "gateway", Operation: "disable"}}
	if !reflect.DeepEqual(result.Rollback, wantRollback) {
		t.Fatalf("rollback=%+v want=%+v", result.Rollback, wantRollback)
	}
}

func TestServiceManagerRejectsUnknownUnitsAndPrivilegeRefusalIsExitThree(t *testing.T) {
	commands := &fakeServiceCommands{fail: map[string]error{}, privilegeErr: errPrivilegeRefused}
	manager := testServiceManager(healthyObserver(), commands)
	if _, err := manager.Stop(context.Background(), []string{"ssh"}); err == nil {
		t.Fatal("unknown service was accepted")
	}
	_, err := manager.Stop(context.Background(), []string{"daemon"})
	var typed commandError
	if !errors.As(err, &typed) || typed.Code != "privilege_required" || typed.ExitStatus != 3 || len(commands.calls) != 0 {
		t.Fatalf("err=%+v calls=%+v", err, commands.calls)
	}
}

func TestServiceManagerUnsupportedPlatformsReturnManualResponse(t *testing.T) {
	obs := healthyObserver()
	obs.platform = "darwin"
	manager, support := discoverServiceManager(context.Background(), Deps{Observer: obs, Timeout: time.Second}, obs.user, true)
	if manager != nil || support.Supported || support.Manager != "manual" || len(support.Manual) == 0 {
		t.Fatalf("manager=%v support=%+v", manager, support)
	}
	for _, command := range support.Manual {
		if strings.Contains(command, "sh -c") || strings.Contains(command, "ssh") {
			t.Fatalf("unsafe manual command %q", command)
		}
	}

	obs = healthyObserver()
	obs.user = UserObservation{Name: "root", UID: 0, Home: "/root"}
	manager, support = discoverServiceManager(context.Background(), Deps{Observer: obs, Timeout: time.Second}, obs.user, true)
	if manager != nil || support.Supported || !strings.Contains(support.Reason, "unprivileged") {
		t.Fatalf("root target support=%+v", support)
	}
}

func TestServiceStatusAndJournalUseBoundedFixedArgv(t *testing.T) {
	obs := healthyObserver()
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running"
	journalKey := "/usr/bin/journalctl --no-pager --lines 25 --output short-iso-precise --unit " + haoDaemonUnit
	obs.runs[journalKey] = "bounded journal"
	manager := testServiceManager(obs, &fakeServiceCommands{fail: map[string]error{}})

	states, err := manager.Status(context.Background(), []string{"daemon"})
	if err != nil || len(states) != 1 || !states[0].Loaded || !states[0].Enabled || !states[0].Active || states[0].SubState != "running" {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	journal, err := manager.Journal(context.Background(), "daemon", 25)
	if err != nil || journal != "bounded journal" {
		t.Fatalf("journal=%q err=%v", journal, err)
	}
	if _, err := manager.Journal(context.Background(), "daemon", maxJournalLines+1); err == nil {
		t.Fatal("unbounded journal request was accepted")
	}
}

func TestServiceStatusAndMutationFailuresAreBounded(t *testing.T) {
	obs := healthyObserver()
	obs.runErr[serviceStatusKey("daemon")] = context.DeadlineExceeded
	manager := testServiceManager(obs, &fakeServiceCommands{fail: map[string]error{}})
	if _, err := manager.Status(context.Background(), []string{"daemon"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("status err=%v", err)
	}

	obs = healthyObserver()
	commands := &fakeServiceCommands{fail: map[string]error{"stop " + haoDaemonUnit: context.DeadlineExceeded}}
	manager = testServiceManager(obs, commands)
	if _, err := manager.Stop(context.Background(), []string{"daemon"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mutation err=%v", err)
	}
}

func commandArgv(calls []serviceCommandCall) []string {
	result := make([]string, 0, len(calls))
	for _, call := range calls {
		result = append(result, strings.Join(call.argv, " "))
	}
	return result
}
