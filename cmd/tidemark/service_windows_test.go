// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// The SID of NT SERVICE\Tidemark (S-1-5-80 and the SHA-1 of the UTF-16 upper-case service
// name, as `sc.exe showsid Tidemark` prints it).
const testServiceSID = "S-1-5-80-1322640522-3715000780-4052383903-578039740-102746081"

func TestUpdateServiceSDDL(t *testing.T) {
	sddl := updateServiceSDDL(testServiceSID)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sd.DACL(); err != nil {
		t.Fatal(err)
	}
	// The Tidemark service may start and query the helper, nothing else.
	if !strings.HasSuffix(sddl, "(A;;LCRP;;;"+testServiceSID+")") {
		t.Errorf("sddl %s", sddl)
	}
	if got := strings.Count(sddl, testServiceSID); got != 1 {
		t.Errorf("service SID appears %d times", got)
	}
	// The round trip keeps exactly these ACEs.
	if sd.String() != sddl {
		t.Errorf("round trip %s, want %s", sd.String(), sddl)
	}
}

// fakeService records the SCM calls of stopService and restartService.
type fakeService struct {
	stopErr   error
	startErr  error
	states    []svc.State // returned by successive queries
	calls     []string
	queryErrs error
}

func (f *fakeService) Control(c svc.Cmd) (svc.Status, error) {
	f.calls = append(f.calls, "control")
	if f.stopErr != nil {
		return svc.Status{}, f.stopErr
	}
	return svc.Status{State: svc.StopPending}, nil
}

func (f *fakeService) Query() (svc.Status, error) {
	f.calls = append(f.calls, "query")
	if f.queryErrs != nil {
		return svc.Status{}, f.queryErrs
	}
	st := f.states[0]
	if len(f.states) > 1 {
		f.states = f.states[1:]
	}
	return svc.Status{State: st}, nil
}

func (f *fakeService) Start(...string) error {
	f.calls = append(f.calls, "start")
	return f.startErr
}

func TestRestartService(t *testing.T) {
	tests := []struct {
		name      string
		svc       *fakeService
		timeout   time.Duration
		wantCalls string
		wantErr   bool
	}{
		{"running", &fakeService{states: []svc.State{svc.StopPending, svc.Stopped}}, time.Second, "control query query start", false},
		{"not running", &fakeService{stopErr: windows.ERROR_SERVICE_NOT_ACTIVE}, time.Second, "control start", false},
		{"started meanwhile", &fakeService{states: []svc.State{svc.Stopped}, startErr: windows.ERROR_SERVICE_ALREADY_RUNNING},
			time.Second, "control query start", false},
		{"stop refused", &fakeService{stopErr: windows.ERROR_ACCESS_DENIED}, time.Second, "control", true},
		{"stop hangs", &fakeService{states: []svc.State{svc.StopPending}}, -time.Second, "control", true},
		{"start fails", &fakeService{states: []svc.State{svc.Stopped}, startErr: errors.New("boom")}, time.Second, "control query start", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := restartService(tc.svc, tc.timeout, time.Millisecond)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v", err)
			}
			if got := strings.Join(tc.svc.calls, " "); got != tc.wantCalls {
				t.Errorf("calls %q, want %q", got, tc.wantCalls)
			}
		})
	}
}
